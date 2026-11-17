package phpwalk_test

import (
	"testing"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// firstOfKind returns the first node of kind in a depth-first walk of n.
func firstOfKind(n *ts.Node, kind string) *ts.Node {
	if n.Kind() == kind {
		return n
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		if found := firstOfKind(n.Child(i), kind); found != nil {
			return found
		}
	}
	return nil
}

func TestAttributes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		kind string
		want []string
	}{
		{
			name: "method with single attribute",
			src:  `<?php class A { #[Scope] protected function popular($q) {} }`,
			kind: "method_declaration",
			want: []string{"Scope"},
		},
		{
			name: "grouped and stacked attributes keep source order",
			src:  `<?php class A { #[Foo, \Bar\Baz(1)] #[Qux] public function m() {} }`,
			kind: "method_declaration",
			want: []string{"Foo", "\\Bar\\Baz", "Qux"},
		},
		{
			name: "class attribute with arguments",
			src:  `<?php #[Bind(Impl::class)] interface I {}`,
			kind: "interface_declaration",
			want: []string{"Bind"},
		},
		{
			name: "no attributes",
			src:  `<?php class A { public function m() {} }`,
			kind: "method_declaration",
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := []byte(tc.src)
			tree, err := phpnode.ParseBytes(src)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			decl := firstOfKind(tree.RootNode(), tc.kind)
			if decl == nil {
				t.Fatalf("no %s node", tc.kind)
			}

			got := phpwalk.Attributes(decl, src)

			var names []string
			for _, a := range got {
				names = append(names, a.Name)
				if a.Node == nil || a.Node.Kind() != "attribute" {
					t.Errorf("attribute %q: Node kind = %v, want attribute", a.Name, a.Node)
				}
			}
			if len(names) != len(tc.want) {
				t.Fatalf("names = %v, want %v", names, tc.want)
			}
			for i := range names {
				if names[i] != tc.want[i] {
					t.Errorf("names[%d] = %q, want %q", i, names[i], tc.want[i])
				}
			}
		})
	}
}

func TestUnwrapTypeName_NullableUnion(t *testing.T) {
	tests := []struct{ decl, want string }{
		{`function f(User|null $u) {}`, "User"},
		{`function f(null|\App\User $u) {}`, "\\App\\User"},
		{`function f(int|string $u) {}`, ""},
		{`function f(User|Admin|null $u) {}`, ""},
		{`function f(?User $u) {}`, "User"},
	}
	for _, tc := range tests {
		t.Run(tc.decl, func(t *testing.T) {
			src := []byte("<?php " + tc.decl)
			tree, err := phpnode.ParseBytes(src)
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Close()
			param := firstOfKind(tree.RootNode(), "simple_parameter")
			got := phpwalk.UnwrapTypeName(param.ChildByFieldName("type"), src)
			if got != tc.want {
				t.Errorf("UnwrapTypeName = %q, want %q", got, tc.want)
			}
		})
	}
}
