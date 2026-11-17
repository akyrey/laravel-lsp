package migrations

import (
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// nonColumnMethods are Blueprint methods whose first argument names an
// existing column (indexes, keys, table options) rather than defining one.
// Methods starting with "drop" or "rename" are excluded separately.
var nonColumnMethods = map[string]bool{
	"index": true, "unique": true, "primary": true, "foreign": true,
	"fullText": true, "spatialIndex": true, "rawIndex": true,
	"comment": true, "engine": true, "charset": true, "collation": true,
	"temporary": true, "after": true, "create": true,
}

// implicitColumns lists Blueprint helpers that define fixed columns without
// (or regardless of) a name argument.
var implicitColumns = map[string][]string{
	"timestamps":         {"created_at", "updated_at"},
	"timestampsTz":       {"created_at", "updated_at"},
	"nullableTimestamps": {"created_at", "updated_at"},
	"rememberToken":      {"remember_token"},
}

// defaultNamedColumns are helpers whose column name argument is optional,
// with Laravel's default when it is omitted.
var defaultNamedColumns = map[string]string{
	"id":            "id",
	"softDeletes":   "deleted_at",
	"softDeletesTz": "deleted_at",
	"uuid":          "uuid",
	"ulid":          "ulid",
	"ipAddress":     "ip_address",
	"macAddress":    "mac_address",
}

// morphMethods define a `<name>_id` / `<name>_type` pair.
var morphMethods = map[string]bool{
	"morphs": true, "nullableMorphs": true,
	"uuidMorphs": true, "nullableUuidMorphs": true,
	"ulidMorphs": true, "nullableUlidMorphs": true,
}

// columnsFor returns the columns a `$table->method(args...)` call defines.
// Dropped columns are kept (their first definition still documents them);
// renameColumn('from', 'to') defines `to`, since models use the new name.
func columnsFor(method string, args []*ts.Node, src []byte) []string {
	if method == "renameColumn" {
		if len(args) > 1 {
			if to := phpwalk.StringValue(args[1], src); to != "" {
				return []string{to}
			}
		}
		return nil
	}
	if nonColumnMethods[method] || strings.HasPrefix(method, "drop") || strings.HasPrefix(method, "rename") {
		return nil
	}
	if cols, ok := implicitColumns[method]; ok {
		return cols
	}
	if method == "foreignIdFor" {
		return foreignIdForColumn(args, src)
	}
	name := ""
	if len(args) > 0 {
		name = phpwalk.StringValue(args[0], src)
	}
	if morphMethods[method] {
		if name == "" {
			return nil
		}
		return []string{name + "_id", name + "_type"}
	}
	if name == "" {
		if def, ok := defaultNamedColumns[method]; ok {
			return []string{def}
		}
		return nil
	}
	return []string{name}
}

// foreignIdForColumn mirrors Blueprint::foreignIdFor($model, $column = null):
// an explicit column wins, otherwise the model's foreign key, which is
// snake_case(class_basename) + "_id".
func foreignIdForColumn(args []*ts.Node, src []byte) []string {
	if len(args) > 1 {
		if name := phpwalk.StringValue(args[1], src); name != "" {
			return []string{name}
		}
	}
	if len(args) == 0 || args[0].Kind() != "class_constant_access_expression" {
		return nil
	}
	class := strings.TrimSuffix(args[0].Utf8Text(src), "::class")
	if class == args[0].Utf8Text(src) {
		return nil // Foo::CONST, not Foo::class
	}
	if i := strings.LastIndex(class, "\\"); i >= 0 {
		class = class[i+1:]
	}
	return []string{phputil.Snake(class) + "_id"}
}
