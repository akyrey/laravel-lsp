// Package classinfo answers "what class is property P of class C?" for any
// class the project's Composer autoloader can locate: typed properties
// (including PHP 8.4 hooked ones), promoted constructor parameters, and
// members inherited from used traits and parent classes.
//
// The LSP type resolver uses it to follow `$this->user->email` when `user`
// is a typed property rather than an Eloquent relationship.
package classinfo

import (
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/composer"
	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// maxHierarchyClasses caps how many classes and traits one lookup visits, as a
// guard against pathological hierarchies (cycles are also cut by visited).
const maxHierarchyClasses = 32

// Decl is the member summary of one class or trait declaration.
type Decl struct {
	// Props maps property name (without $) to its class type. Untyped and
	// primitive-typed properties map to "".
	Props  map[string]phputil.FQN
	Traits []phputil.FQN
	Parent phputil.FQN
}

// DeclOf summarises a class_declaration or trait_declaration node. fc must
// hold the declaring file's namespace and use-statements.
func DeclOf(decl *ts.Node, src []byte, fc *phputil.FileContext) Decl {
	d := Decl{Props: make(map[string]phputil.FQN)}
	if parent := phpwalk.ExtendsName(decl, src); parent != "" {
		d.Parent = fc.Resolve(parent)
	}
	body := decl.ChildByFieldName("body")
	if body == nil {
		return d
	}
	for _, name := range phpwalk.UsedTraits(body, src) {
		d.Traits = append(d.Traits, fc.Resolve(name))
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		member := body.NamedChild(i)
		switch member.Kind() {
		case "property_declaration":
			addProperties(d.Props, member, src, fc)
		case "method_declaration":
			addPromotedParams(d.Props, member, src, fc)
		}
	}
	return d
}

// addProperties records every property_element of a typed declaration.
func addProperties(props map[string]phputil.FQN, decl *ts.Node, src []byte, fc *phputil.FileContext) {
	typ := classType(decl.ChildByFieldName("type"), src, fc)
	for i := uint(0); i < decl.NamedChildCount(); i++ {
		elem := decl.NamedChild(i)
		if elem.Kind() != "property_element" {
			continue
		}
		if name := elem.ChildByFieldName("name"); name != nil {
			props[varName(name, src)] = typ
		}
	}
}

// addPromotedParams records `__construct(private User $user)` parameters.
func addPromotedParams(props map[string]phputil.FQN, method *ts.Node, src []byte, fc *phputil.FileContext) {
	name := method.ChildByFieldName("name")
	params := method.ChildByFieldName("parameters")
	if name == nil || params == nil || phpnode.NodeText(name, src) != "__construct" {
		return
	}
	for i := uint(0); i < params.NamedChildCount(); i++ {
		p := params.NamedChild(i)
		if p.Kind() != "property_promotion_parameter" {
			continue
		}
		if pn := p.ChildByFieldName("name"); pn != nil {
			props[varName(pn, src)] = classType(p.ChildByFieldName("type"), src, fc)
		}
	}
}

// classType resolves a type node to a class FQN, or "" for primitives,
// unions of several classes, and missing types.
func classType(typeNode *ts.Node, src []byte, fc *phputil.FileContext) phputil.FQN {
	if typeNode == nil {
		return ""
	}
	name := phpwalk.UnwrapTypeName(typeNode, src)
	if name == "" || phputil.IsPrimitiveType(name) {
		return ""
	}
	return fc.Resolve(name)
}

func varName(n *ts.Node, src []byte) string {
	text := phpnode.NodeText(n, src)
	if len(text) > 0 && text[0] == '$' {
		return text[1:]
	}
	return text
}

// Index lazily parses and memoizes class declarations located through
// Composer. Safe for concurrent use; a nil *Index knows nothing.
type Index struct {
	auto *composer.Autoload

	mu   sync.Mutex
	memo map[phputil.FQN]*Decl // nil value = could not be located
}

// New returns an Index backed by auto.
func New(auto *composer.Autoload) *Index {
	return &Index{auto: auto, memo: make(map[phputil.FQN]*Decl)}
}

// PropertyType returns the class type of property prop on class (searching
// the class itself, then its traits, then its ancestors), or "" if unknown.
func (ix *Index) PropertyType(class phputil.FQN, prop string) phputil.FQN {
	if ix == nil {
		return ""
	}
	return ix.lookup(class, prop, make(map[phputil.FQN]bool))
}

// LookupFrom resolves prop starting from an already-summarised declaration
// (e.g. the class in the editor's unsaved buffer), falling back to the index
// for its traits and ancestors.
func (ix *Index) LookupFrom(d Decl, prop string) phputil.FQN {
	if typ, ok := d.Props[prop]; ok {
		return typ
	}
	if ix == nil {
		return ""
	}
	return ix.inherited(d, prop, make(map[phputil.FQN]bool))
}

func (ix *Index) lookup(class phputil.FQN, prop string, visited map[phputil.FQN]bool) phputil.FQN {
	if class == "" || visited[class] || len(visited) > maxHierarchyClasses {
		return ""
	}
	visited[class] = true
	d := ix.decl(class)
	if d == nil {
		return ""
	}
	if typ, ok := d.Props[prop]; ok {
		return typ
	}
	return ix.inherited(*d, prop, visited)
}

// inherited searches d's traits, then its parent chain.
func (ix *Index) inherited(d Decl, prop string, visited map[phputil.FQN]bool) phputil.FQN {
	for _, tr := range d.Traits {
		if typ := ix.lookup(tr, prop, visited); typ != "" {
			return typ
		}
	}
	return ix.lookup(d.Parent, prop, visited)
}

// decl returns the memoized summary of class, parsing its file on first use.
func (ix *Index) decl(class phputil.FQN) *Decl {
	ix.mu.Lock()
	d, seen := ix.memo[class]
	ix.mu.Unlock()
	if seen {
		return d
	}
	d, ok := parseDecl(ix.auto.Locate(class), class)
	if !ok {
		return nil // unreadable right now: do not cache the miss
	}
	ix.mu.Lock()
	ix.memo[class] = d
	ix.mu.Unlock()
	return d
}

// parseDecl finds the class or trait named class in path and summarises it.
// ok is false when a located file could not be read or parsed.
func parseDecl(path string, class phputil.FQN) (d *Decl, ok bool) {
	if path == "" {
		return nil, true
	}
	src, tree, err := phpnode.ParseFile(path)
	if err != nil {
		return nil, false
	}
	defer tree.Close()
	v := &declFinder{target: class, fc: &phputil.FileContext{Path: path, Uses: make(phputil.UseMap)}}
	phpwalk.Walk(path, src, tree, v)
	return v.found, true
}

type declFinder struct {
	phpwalk.NullVisitor
	target phputil.FQN
	fc     *phputil.FileContext
	found  *Decl
}

func (v *declFinder) VisitNamespace(ns string) { v.fc.Namespace = phputil.FQN(ns) }
func (v *declFinder) VisitUseItem(alias, fqn string) {
	v.fc.Uses[alias] = phputil.FQN(fqn)
}

func (v *declFinder) VisitClass(n phpwalk.ClassInfo) { v.match(n.NameText, n.Raw, n.Src) }
func (v *declFinder) VisitTrait(n phpwalk.TraitInfo) { v.match(n.NameText, n.Raw, n.Src) }

func (v *declFinder) match(name string, raw *ts.Node, src []byte) {
	if v.found != nil || v.fc.Resolve(name) != v.target {
		return
	}
	d := DeclOf(raw, src, v.fc)
	v.found = &d
}
