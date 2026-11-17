package lsp

import (
	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// functionScopeKinds are the nodes that open a new variable scope.
var functionScopeKinds = map[string]bool{
	"method_declaration":  true,
	"function_definition": true,
	"anonymous_function":  true,
	"arrow_function":      true,
}

// scopeBarrierKinds are nodes whose bodies never share variables with the
// surrounding code, so binding scans must not descend into them.
var scopeBarrierKinds = map[string]bool{
	"method_declaration":    true,
	"function_definition":   true,
	"anonymous_function":    true,
	"arrow_function":        true,
	"class_declaration":     true,
	"anonymous_class":       true,
	"trait_declaration":     true,
	"interface_declaration": true,
	"enum_declaration":      true,
}

// binding is one place a scope assigns a variable.
type binding struct {
	name  string
	start int      // the binding takes effect once execution passes end
	end   int      //
	bound *ts.Node // expression whose type the variable takes; nil = untypable
}

// nodeKey identifies a node within one tree (ts.Node values are not
// comparable across wrapper instances).
type nodeKey struct {
	start, end uint
	kind       string
}

func keyOf(n *ts.Node) nodeKey {
	return nodeKey{start: n.StartByte(), end: n.EndByte(), kind: n.Kind()}
}

// scopeBindings returns every binding made directly in scope (not in nested
// functions or classes), computed once per scope.
func (r *typeResolver) scopeBindings(scope *ts.Node) []binding {
	key := keyOf(scope)
	if bs, ok := r.bindings[key]; ok {
		return bs
	}
	body := scope
	if b := scope.ChildByFieldName("body"); b != nil {
		body = b
	}
	var out []binding
	walkNamed(body, func(n *ts.Node) bool {
		if n != body && scopeBarrierKinds[n.Kind()] {
			return false
		}
		out = append(out, r.bindingsAt(n)...)
		return true
	})
	r.bindings[key] = out
	return out
}

// bindingsAt returns the bindings node n itself introduces. Forms whose type
// cannot be inferred (destructuring, compound and reference assignment,
// catch, global, static) still count: they make the variable unknown rather
// than letting an earlier, now stale, type leak through.
func (r *typeResolver) bindingsAt(n *ts.Node) []binding {
	switch n.Kind() {
	case "assignment_expression":
		left := n.ChildByFieldName("left")
		if left == nil {
			return nil
		}
		if left.Kind() == "variable_name" {
			return []binding{r.newBinding(left, n, n.ChildByFieldName("right"))}
		}
		return r.opaqueVars(left, n) // [$a, $b] = ... / list($a) = ...
	case "augmented_assignment_expression", "reference_assignment_expression":
		if left := n.ChildByFieldName("left"); left != nil && left.Kind() == "variable_name" {
			return []binding{r.newBinding(left, n, nil)}
		}
	case "foreach_statement":
		return r.foreachBindings(n)
	case "catch_clause":
		// Only the exception variable, not everything in the catch body.
		if v := n.ChildByFieldName("name"); v != nil {
			return []binding{r.newBinding(v, v, nil)}
		}
	case "global_declaration", "static_variable_declaration":
		return r.opaqueVars(n, n)
	}
	return nil
}

// foreachBindings types the value variable as the iterable's element model.
// Iterating a relationship yields its related model, which is exactly what
// exprType returns for the iterable, so no element unwrapping is needed.
func (r *typeResolver) foreachBindings(n *ts.Node) []binding {
	if n.NamedChildCount() < 2 {
		return nil
	}
	value := n.NamedChild(1)
	var out []binding
	if value.Kind() == "pair" && value.NamedChildCount() == 2 {
		out = r.opaqueVars(value.NamedChild(0), value.NamedChild(0)) // the key
		value = value.NamedChild(1)
	}
	if value.Kind() == "by_ref" && value.NamedChildCount() > 0 {
		value = value.NamedChild(0)
	}
	if value.Kind() != "variable_name" {
		return append(out, r.opaqueVars(value, value)...) // destructured value
	}
	return append(out, r.newBinding(value, value, n.NamedChild(0)))
}

// opaqueVars returns an untypable binding, effective after at, for every
// variable named inside n.
func (r *typeResolver) opaqueVars(n, at *ts.Node) []binding {
	var out []binding
	walkNamed(n, func(c *ts.Node) bool {
		if c.Kind() == "variable_name" {
			out = append(out, r.newBinding(c, at, nil))
		}
		return true
	})
	return out
}

func (r *typeResolver) newBinding(variable, at, bound *ts.Node) binding {
	return binding{
		name:  phpnode.NodeText(variable, r.src),
		start: int(at.StartByte()),
		end:   int(at.EndByte()),
		bound: bound,
	}
}

// assignedType resolves name from the bindings in scope that complete before
// offset. All such candidates must agree: a branch-dependent reassignment
// (`if (...) { $u = new Post; }`) or any untypable rebinding yields "".
// found reports whether name is bound in scope at all before offset.
func (r *typeResolver) assignedType(scope *ts.Node, name string, offset, depth int) (fqn phputil.FQN, found bool) {
	for _, b := range r.scopeBindings(scope) {
		if b.name != name || b.end > offset {
			continue
		}
		var t phputil.FQN
		if b.bound != nil {
			t = r.exprType(b.bound, depth+1)
		}
		if t == "" || (found && t != fqn) {
			return "", true
		}
		fqn, found = t, true
	}
	return fqn, found
}

// closureCaptures reports whether an anonymous function's use() clause
// imports name (by value or by reference).
func closureCaptures(closure *ts.Node, name string, src []byte) bool {
	for i := uint(0); i < closure.NamedChildCount(); i++ {
		clause := closure.NamedChild(i)
		if clause.Kind() != "anonymous_function_use_clause" {
			continue
		}
		found := false
		walkNamed(clause, func(n *ts.Node) bool {
			if n.Kind() == "variable_name" && phpnode.NodeText(n, src) == name {
				found = true
			}
			return !found
		})
		return found
	}
	return false
}

// walkNamed visits n and its named descendants depth-first; fn returns false
// to skip a node's children.
func walkNamed(n *ts.Node, fn func(*ts.Node) bool) {
	if !fn(n) {
		return
	}
	for i := uint(0); i < n.NamedChildCount(); i++ {
		walkNamed(n.NamedChild(i), fn)
	}
}
