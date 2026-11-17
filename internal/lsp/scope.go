package lsp

import (
	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/classinfo"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// maxTypeDepth bounds the mutual recursion between expression and variable
// resolution ($a = $b->x; $b = $a->y; ...).
const maxTypeDepth = 16

// typeResolver infers model types for expressions purely from the syntax
// tree around them: scope is found by walking up from the expression, so
// callers need no enclosing-method bookkeeping. One resolver serves one
// parsed file; it caches per-scope bindings and class declarations, so
// reuse it for every lookup in that file (see typeCache).
type typeResolver struct {
	src    []byte
	fc     *phputil.FileContext
	models *eloquent.ModelIndex

	bindings   map[nodeKey][]binding
	classDecls map[phputil.FQN]*ts.Node // nil until first needed
	declInfo   map[phputil.FQN]classinfo.Decl
	spans      []span // scopes and class bodies in pre-order; nil until first needed
}

// span is a node that opens a variable scope or changes what $this means.
// Spans are found by byte offset instead of Node.Parent(), which tree-sitter
// implements by re-walking from the root (and crosses CGo) on every call.
type span struct {
	start, end int
	kind       string
	node       *ts.Node
	static     bool // a static closure / arrow fn: no $this inside
}

// spanKinds are the node kinds recorded as spans.
var spanKinds = map[string]bool{
	"program": true, "method_declaration": true, "function_definition": true,
	"anonymous_function": true, "arrow_function": true,
	"class_declaration": true, "anonymous_class": true, "trait_declaration": true,
	"interface_declaration": true, "enum_declaration": true,
}

// enclosingSpans returns the spans containing offset, innermost first. The
// span index is built on first use from the root of at's tree.
func (r *typeResolver) enclosingSpans(at *ts.Node, offset int) []span {
	if r.spans == nil {
		root := at
		for p := at.Parent(); p != nil; p = p.Parent() {
			root = p
		}
		walkNamed(root, func(n *ts.Node) bool {
			if spanKinds[n.Kind()] {
				r.spans = append(r.spans, span{
					start:  int(n.StartByte()),
					end:    int(n.EndByte()),
					kind:   n.Kind(),
					node:   n,
					static: n.ChildByFieldName("static_modifier") != nil,
				})
			}
			return true
		})
	}
	var out []span
	for i := len(r.spans) - 1; i >= 0; i-- { // pre-order reversed = inner first
		if sp := r.spans[i]; sp.start <= offset && offset < sp.end {
			out = append(out, sp)
		}
	}
	return out
}

func newTypeResolver(src []byte, fc *phputil.FileContext, models *eloquent.ModelIndex) *typeResolver {
	return &typeResolver{
		src:      src,
		fc:       fc,
		models:   models,
		bindings: make(map[nodeKey][]binding),
		declInfo: make(map[phputil.FQN]classinfo.Decl),
	}
}

// resolveExprType resolves expr to a model (or other class) FQN. Handles
// variables (parameters, prior assignments, foreach values, closure use()
// captures, arrow-function captures, $this), `new X`, `X::find()`-style
// static calls, query chains ending in `->first()`/`->get()`, relationship
// chains `$a->rel->...`, and typed properties. fc must hold the file's
// namespace and use-statements. Returns "" when unknown. For many lookups in
// one file, prefer a shared typeCache.
func resolveExprType(expr *ts.Node, src []byte, fc *phputil.FileContext, models *eloquent.ModelIndex) phputil.FQN {
	return newTypeResolver(src, fc, models).exprType(expr, 0)
}

// typeCache lazily creates one typeResolver per visited file so that every
// property fetch in the file shares its caches. Embed it in a visitor.
type typeCache struct{ r *typeResolver }

func (c *typeCache) resolver(src []byte, fc *phputil.FileContext, models *eloquent.ModelIndex) *typeResolver {
	if c.r == nil {
		c.r = newTypeResolver(src, fc, models)
	}
	return c.r
}

func (c *typeCache) resolve(expr *ts.Node, src []byte, fc *phputil.FileContext, models *eloquent.ModelIndex) phputil.FQN {
	return c.resolver(src, fc, models).exprType(expr, 0)
}

func (r *typeResolver) exprType(expr *ts.Node, depth int) phputil.FQN {
	if expr == nil || depth > maxTypeDepth {
		return ""
	}
	switch expr.Kind() {
	case "variable_name":
		name := phpnode.NodeText(expr, r.src)
		if name == "$this" {
			return r.enclosingClass(expr)
		}
		return r.varType(name, expr, int(expr.StartByte()), depth)
	case "member_access_expression", "nullsafe_member_access_expression":
		return r.memberType(expr, depth)
	case "parenthesized_expression":
		if expr.NamedChildCount() > 0 {
			return r.exprType(expr.NamedChild(0), depth+1)
		}
	case "object_creation_expression":
		for i := uint(0); i < expr.ChildCount(); i++ {
			if child := expr.Child(i); child.Kind() == "qualified_name" || child.Kind() == "name" {
				return r.fc.Resolve(phpnode.NodeText(child, r.src))
			}
		}
	case "scoped_call_expression":
		nameNode := expr.ChildByFieldName("name")
		scopeNode := expr.ChildByFieldName("scope")
		if nameNode != nil && scopeNode != nil && yieldsModels(phpnode.NodeText(nameNode, r.src)) {
			return r.fc.Resolve(phpnode.NodeText(scopeNode, r.src))
		}
	case "member_call_expression", "nullsafe_member_call_expression":
		// Post::where(...)->first() / ->get(): the builder's model.
		nameNode := expr.ChildByFieldName("name")
		if nameNode != nil && yieldsModels(phpnode.NodeText(nameNode, r.src)) {
			return r.builderModel(expr.ChildByFieldName("object"), depth+1)
		}
	}
	return ""
}

// memberType resolves `$obj->prop`: through a model relationship when $obj
// is a model, otherwise through the declared type of property prop.
func (r *typeResolver) memberType(expr *ts.Node, depth int) phputil.FQN {
	propNode := expr.ChildByFieldName("name")
	lhs := r.exprType(expr.ChildByFieldName("object"), depth+1)
	if propNode == nil || lhs == "" || r.models == nil {
		return ""
	}
	prop := phpnode.NodeText(propNode, r.src)
	if cat := r.models.Lookup(lhs); cat != nil {
		for _, a := range cat.ByExposed[prop] {
			if a.RelatedFQN != "" {
				return a.RelatedFQN
			}
		}
	}
	return r.propertyType(expr, lhs, prop)
}

// propertyType returns the declared class type of class::$prop. A class
// declared in the current buffer is read from the tree (so unsaved edits
// count); its traits, ancestors, and every other class come from the
// project-wide ClassMembers lookup.
func (r *typeResolver) propertyType(at *ts.Node, class phputil.FQN, prop string) phputil.FQN {
	d, ok := r.localDecl(at, class)
	if !ok {
		return r.models.PropertyType(class, prop)
	}
	if typ, ok := d.Props[prop]; ok {
		return typ
	}
	for _, tr := range d.Traits {
		if typ := r.models.PropertyType(tr, prop); typ != "" {
			return typ
		}
	}
	return r.models.PropertyType(d.Parent, prop)
}

// localDecl returns the member summary of class when it is declared in the
// current file, computing it once.
func (r *typeResolver) localDecl(at *ts.Node, class phputil.FQN) (classinfo.Decl, bool) {
	if d, ok := r.declInfo[class]; ok {
		return d, true
	}
	node := r.localClassDecl(at, class)
	if node == nil {
		return classinfo.Decl{}, false
	}
	d := classinfo.DeclOf(node, r.src, r.fc)
	r.declInfo[class] = d
	return d, true
}

// localClassDecl finds the declaration of class in the file containing at.
// All declarations are collected in one pass on first use.
func (r *typeResolver) localClassDecl(at *ts.Node, class phputil.FQN) *ts.Node {
	if r.classDecls == nil {
		r.classDecls = make(map[phputil.FQN]*ts.Node)
		r.enclosingSpans(at, 0) // ensure the span index exists
		for _, sp := range r.spans {
			if sp.kind != "class_declaration" && sp.kind != "trait_declaration" {
				continue
			}
			if name := sp.node.ChildByFieldName("name"); name != nil {
				r.classDecls[r.fc.Resolve(phpnode.NodeText(name, r.src))] = sp.node
			}
		}
	}
	return r.classDecls[class]
}

// enclosingClass returns the FQN of the class declaration containing n.
// Inside a trait, an anonymous class, or a static closure (or outside any
// class) $this is unknown.
func (r *typeResolver) enclosingClass(n *ts.Node) phputil.FQN {
	for _, sp := range r.enclosingSpans(n, int(n.StartByte())) {
		switch sp.kind {
		case "class_declaration":
			if name := sp.node.ChildByFieldName("name"); name != nil {
				return r.fc.Resolve(phpnode.NodeText(name, r.src))
			}
			return ""
		case "anonymous_class", "trait_declaration", "interface_declaration", "enum_declaration":
			return ""
		case "anonymous_function", "arrow_function":
			if sp.static {
				return ""
			}
		}
	}
	return ""
}

// varType resolves variable name as seen at byte offset, starting from node
// at and widening scope outward: arrow functions see their parent scope,
// closures only the variables listed in use(), and functions/methods nothing.
// Within a scope, a prior assignment overrides a parameter's declared type.
func (r *typeResolver) varType(name string, at *ts.Node, offset, depth int) phputil.FQN {
	for _, sp := range r.enclosingSpans(at, int(at.StartByte())) {
		if !functionScopeKinds[sp.kind] && sp.kind != "program" {
			continue
		}
		scope := sp.node
		if fqn, found := r.assignedType(scope, name, offset, depth); found {
			return fqn
		}
		if fqn, isParam := r.paramType(scope, name); isParam {
			return fqn // an untyped param still shadows outer scopes
		}
		switch scope.Kind() {
		case "arrow_function":
			// Auto-captures by value at creation time.
			offset = int(scope.StartByte())
		case "anonymous_function":
			if !closureCaptures(scope, name, r.src) {
				return ""
			}
			offset = int(scope.StartByte())
		default:
			return "" // method, function, or file boundary
		}
	}
	return ""
}

// paramType reports whether scope declares a parameter called name, and the
// FQN of its class type when it has one.
func (r *typeResolver) paramType(scope *ts.Node, name string) (phputil.FQN, bool) {
	params := scope.ChildByFieldName("parameters")
	if params == nil {
		return "", false
	}
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		nameNode := p.ChildByFieldName("name")
		if nameNode == nil || phpnode.NodeText(nameNode, r.src) != name {
			continue
		}
		typeNode := p.ChildByFieldName("type")
		if typeNode == nil {
			return "", true
		}
		typeName := phpwalk.UnwrapTypeName(typeNode, r.src)
		if typeName == "" || phputil.IsPrimitiveType(typeName) {
			return "", true
		}
		return r.fc.Resolve(typeName), true
	}
	return "", false
}
