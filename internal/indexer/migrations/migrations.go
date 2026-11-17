// Package migrations indexes the database columns declared in Laravel
// migrations, so a model attribute backed only by a column (the most common
// kind, invisible in the model class itself) still has a jump target.
//
// Only the `up()` direction is read: `down()` re-creates dropped columns to
// roll back, and indexing those would resurrect columns that no longer exist.
package migrations

import (
	"io/fs"
	"path/filepath"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// DefaultDirs are Laravel's conventional migration directories; the server
// config defaults to them.
var DefaultDirs = []string{"database/migrations"}

// Schema maps table → column → the location of the column's first
// definition. It is immutable once Walk returns; a nil *Schema has no columns.
type Schema struct {
	tables map[string]map[string]phputil.Location
}

// Columns returns the columns of table, or nil when the table is unknown.
// The returned map must not be modified.
func (s *Schema) Columns(table string) map[string]phputil.Location {
	if s == nil {
		return nil
	}
	return s.tables[table]
}

// TableCount returns the number of tables with at least one column, for
// diagnostics logging.
func (s *Schema) TableCount() int {
	if s == nil {
		return 0
	}
	return len(s.tables)
}

// Walk parses every .php file under dirs (relative to root); no dirs means
// no columns. Unreadable files and directories are skipped: the schema is
// best-effort, like the other string indexes, and callers log TableCount so
// an empty result is visible. Files are visited in lexical order, which for
// Laravel's timestamped names is chronological, so the earliest definition
// of a column wins.
func Walk(root string, dirs []string) *Schema {
	s := &Schema{tables: make(map[string]map[string]phputil.Location)}
	for _, dir := range dirs {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".php") {
				return nil
			}
			src, tree, parseErr := phpnode.ParseFile(path)
			if parseErr != nil {
				return nil
			}
			defer tree.Close()
			s.scan(tree.RootNode(), src, path)
			return nil
		})
	}
	return s
}

// scan walks n depth-first looking for Schema::create/table calls, skipping
// down() methods entirely.
func (s *Schema) scan(n *ts.Node, src []byte, path string) {
	if n.Kind() == "method_declaration" {
		if name := n.ChildByFieldName("name"); name != nil && phpnode.NodeText(name, src) == "down" {
			return
		}
	}
	if table, closure := schemaCall(n, src); table != "" && closure != nil {
		s.collectBlueprint(table, closure, src, path)
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		s.scan(n.Child(i), src, path)
	}
}

// schemaCall matches `Schema::create|table('t', <closure>)`, optionally
// through `Schema::connection('x')->...`, and returns the table name and the
// closure node.
func schemaCall(n *ts.Node, src []byte) (string, *ts.Node) {
	name := n.ChildByFieldName("name")
	if name == nil {
		return "", nil
	}
	if method := phpnode.NodeText(name, src); method != "create" && method != "table" {
		return "", nil
	}
	if !isSchemaReceiver(n, src) {
		return "", nil
	}
	argsNode := n.ChildByFieldName("arguments")
	if argsNode == nil {
		return "", nil
	}
	args := phpwalk.ArgExprs(argsNode, src)
	if len(args) < 2 || !isClosure(args[1]) {
		return "", nil
	}
	return phpwalk.StringValue(args[0], src), args[1]
}

// isSchemaReceiver reports whether call n is invoked on the Schema facade,
// either directly (Schema::create) or via Schema::connection(...)->create.
func isSchemaReceiver(n *ts.Node, src []byte) bool {
	switch n.Kind() {
	case "scoped_call_expression":
		return isSchemaClass(n.ChildByFieldName("scope"), src)
	case "member_call_expression":
		obj := n.ChildByFieldName("object")
		if obj == nil || obj.Kind() != "scoped_call_expression" {
			return false
		}
		conn := obj.ChildByFieldName("name")
		return conn != nil && phpnode.NodeText(conn, src) == "connection" &&
			isSchemaClass(obj.ChildByFieldName("scope"), src)
	}
	return false
}

// isSchemaClass matches the facade by its short name, so `Schema`,
// `\Illuminate\Support\Facades\Schema`, and the full FQN all qualify.
func isSchemaClass(n *ts.Node, src []byte) bool {
	if n == nil {
		return false
	}
	text := phpnode.NodeText(n, src)
	return text == "Schema" || strings.HasSuffix(text, "\\Schema")
}

func isClosure(n *ts.Node) bool {
	k := n.Kind()
	return k == "anonymous_function" || k == "arrow_function"
}

// collectBlueprint records every column defined on the closure's Blueprint
// parameter.
func (s *Schema) collectBlueprint(table string, closure *ts.Node, src []byte, path string) {
	param := firstParamName(closure, src)
	body := closure.ChildByFieldName("body")
	if param == "" || body == nil {
		return
	}
	cols := s.tables[table]
	if cols == nil {
		cols = make(map[string]phputil.Location)
		s.tables[table] = cols
	}
	visitBlueprintCalls(body, src, param, func(method string, nameNode *ts.Node, args []*ts.Node) {
		target := nameNode
		switch {
		case method == "renameColumn" && len(args) > 1:
			target = args[1] // the new name is the one models use
		case len(args) > 0:
			target = args[0]
		}
		for _, col := range columnsFor(method, args, src) {
			if _, seen := cols[col]; !seen {
				cols[col] = phpnode.FromNode(path, target)
			}
		}
	})
}

// firstParamName returns the `$name` of a closure's first parameter.
func firstParamName(closure *ts.Node, src []byte) string {
	params := closure.ChildByFieldName("parameters")
	if params == nil {
		return ""
	}
	for i := uint(0); i < params.ChildCount(); i++ {
		p := params.Child(i)
		if p.Kind() != "simple_parameter" {
			continue
		}
		if name := p.ChildByFieldName("name"); name != nil {
			return phpnode.NodeText(name, src)
		}
	}
	return ""
}

// visitBlueprintCalls calls fn for each `$param->method(...)` call under n.
// Chained modifiers (`->nullable()`) have a call, not $param, as their object
// and are therefore skipped naturally.
func visitBlueprintCalls(n *ts.Node, src []byte, param string, fn func(method string, nameNode *ts.Node, args []*ts.Node)) {
	if n.Kind() == "member_call_expression" {
		obj := n.ChildByFieldName("object")
		name := n.ChildByFieldName("name")
		if obj != nil && name != nil && obj.Kind() == "variable_name" && phpnode.NodeText(obj, src) == param {
			var args []*ts.Node
			if argsNode := n.ChildByFieldName("arguments"); argsNode != nil {
				args = phpwalk.ArgExprs(argsNode, src)
			}
			fn(phpnode.NodeText(name, src), name, args)
		}
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		visitBlueprintCalls(n.Child(i), src, param, fn)
	}
}
