package phpwalk

import (
	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// ClassConstFQN extracts the FQN from a class_constant_access_expression node
// like `StripeGateway::class`, resolving via fc. Returns "" for non-class-const
// expressions or when the constant is not "class".
func ClassConstFQN(n *ts.Node, src []byte, fc *phputil.FileContext) phputil.FQN {
	if n == nil || n.Kind() != "class_constant_access_expression" {
		return ""
	}
	var first, second string
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child.Kind() == "name" || child.Kind() == "qualified_name" {
			if first == "" {
				first = phpnode.NodeText(child, src)
			} else {
				second = phpnode.NodeText(child, src)
				break
			}
		}
	}
	if second != "class" {
		return ""
	}
	return fc.Resolve(first)
}

// UnwrapTypeName returns the base type name from a type annotation node,
// stripping any nullable wrapper. E.g. optional_type(?Foo) → "Foo".
// This is the exported form of the internal unwrapTypeName used by the walker.
func UnwrapTypeName(n *ts.Node, src []byte) string {
	return unwrapTypeName(src, n)
}

// IsStringLiteral reports whether n is a PHP string literal node: a
// single-quoted "string" or a double-quoted "encapsed_string".
func IsStringLiteral(n *ts.Node) bool {
	return n != nil && (n.Kind() == "string" || n.Kind() == "encapsed_string")
}

// StringValue extracts the unquoted content of a PHP string literal node.
// tree-sitter-php represents 'email' as string { ' string_content ' } and
// "email" as encapsed_string { " string_content " }. Returns "" when n is
// not a string literal or contains interpolation ("$var") — an interpolated
// value is unknowable statically.
func StringValue(n *ts.Node, src []byte) string {
	if !IsStringLiteral(n) {
		return ""
	}
	content := ""
	for i := uint(0); i < n.ChildCount(); i++ {
		child := n.Child(i)
		switch {
		case child.Kind() == "string_content":
			if content != "" {
				return "" // split content implies interpolation between parts
			}
			content = phpnode.NodeText(child, src)
		case child.IsNamed() && child.Kind() != "string_content":
			return "" // interpolation or escape sequence node
		}
	}
	if content != "" {
		return content
	}
	// Fallback: strip surrounding quote characters from the raw node text.
	v := phpnode.NodeText(n, src)
	if len(v) >= 2 && (v[0] == '\'' || v[0] == '"') {
		return v[1 : len(v)-1]
	}
	return v
}

// AttributeRef is one PHP 8 attribute attached to a declaration.
type AttributeRef struct {
	// Name is the attribute class as written in source ("Scope",
	// "\Illuminate\...\Scope"); resolve it with FileContext.Resolve.
	Name string
	// Node is the `attribute` node; its "parameters" field holds the arguments.
	Node *ts.Node
}

// Attributes returns the attributes attached to decl (a class, interface,
// method, property, ... declaration) in source order, flattening grouped
// (#[A, B]) and stacked (#[A] #[B]) forms.
func Attributes(decl *ts.Node, src []byte) []AttributeRef {
	list := decl.ChildByFieldName("attributes")
	if list == nil {
		return nil
	}
	var out []AttributeRef
	for i := uint(0); i < list.ChildCount(); i++ {
		group := list.Child(i)
		if group.Kind() != "attribute_group" {
			continue
		}
		for j := uint(0); j < group.ChildCount(); j++ {
			attr := group.Child(j)
			if attr.Kind() != "attribute" {
				continue
			}
			if name := attributeName(attr, src); name != "" {
				out = append(out, AttributeRef{Name: name, Node: attr})
			}
		}
	}
	return out
}

// attributeName returns the class name of an attribute node: its first
// name-like child.
func attributeName(attr *ts.Node, src []byte) string {
	for k := uint(0); k < attr.ChildCount(); k++ {
		child := attr.Child(k)
		if child.Kind() == "qualified_name" || child.Kind() == "name" {
			return child.Utf8Text(src)
		}
	}
	return ""
}

// UsedTraits returns the trait names (as written, unresolved) imported by
// `use A, B;` statements directly inside a class or trait body.
func UsedTraits(body *ts.Node, src []byte) []string {
	return collectUseTraits(src, body)
}

// ExtendsName returns the parent class of a class declaration as written
// (unresolved), or "" when it has no extends clause.
func ExtendsName(classDecl *ts.Node, src []byte) string {
	for i := uint(0); i < classDecl.ChildCount(); i++ {
		if child := classDecl.Child(i); child.Kind() == "base_clause" {
			return baseClauseText(src, child)
		}
	}
	return ""
}
