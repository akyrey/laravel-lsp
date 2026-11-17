package lsp

import (
	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// modelStaticMethods return a single model instance when called statically on
// a model or on its query builder.
var modelStaticMethods = map[string]bool{
	"find": true, "findOrFail": true, "findOrNew": true, "findOr": true,
	"first": true, "firstOrFail": true, "firstOrCreate": true, "firstOrNew": true,
	"firstOr": true, "firstWhere": true, "updateOrCreate": true, "create": true,
	"make": true, "forceCreate": true, "sole": true,
}

// collectionMethods return a collection (or paginator) of the builder's
// model. Like relationships, a collection resolves to its element model, so
// `foreach (Post::query()->get() as $p)` types $p.
var collectionMethods = map[string]bool{
	"get": true, "all": true, "paginate": true, "simplePaginate": true,
	"cursorPaginate": true, "cursor": true, "lazy": true, "findMany": true,
}

// nonBuilderMethods end a query chain without yielding models. Every other
// method keeps the chain a builder — a deny-list on purpose, so user scopes
// and macros chain without having to be enumerated.
var nonBuilderMethods = map[string]bool{
	"count": true, "exists": true, "doesntExist": true, "sum": true, "avg": true,
	"average": true, "min": true, "max": true, "value": true, "pluck": true,
	"delete": true, "forceDelete": true, "restore": true, "update": true,
	"upsert": true, "insert": true, "increment": true, "decrement": true,
	"truncate": true, "toSql": true, "toArray": true, "toJson": true,
	"implode": true, "chunk": true, "chunkById": true, "each": true,
}

// yieldsModels reports whether a query method returns model instances (or a
// collection of them) rather than a builder or a scalar.
func yieldsModels(method string) bool {
	return modelStaticMethods[method] || collectionMethods[method]
}

// isBuilderMethod reports whether calling method on a builder keeps a builder.
func isBuilderMethod(method string) bool {
	return !yieldsModels(method) && !nonBuilderMethods[method]
}

// builderModel returns X when expr evaluates to an Eloquent query builder or
// relation over model X: `X::query()`, `X::where(...)`, `X::scope()`, a
// chained builder call, or a relationship method call `$x->posts()`.
// Returns "" for anything else, including terminal calls (`->get()`).
func (r *typeResolver) builderModel(expr *ts.Node, depth int) phputil.FQN {
	if expr == nil || depth > maxTypeDepth || r.models == nil {
		return ""
	}
	switch expr.Kind() {
	case "parenthesized_expression":
		if expr.NamedChildCount() > 0 {
			return r.builderModel(expr.NamedChild(0), depth+1)
		}
	case "scoped_call_expression":
		nameNode := expr.ChildByFieldName("name")
		scopeNode := expr.ChildByFieldName("scope")
		if nameNode == nil || scopeNode == nil || !isBuilderMethod(phpnode.NodeText(nameNode, r.src)) {
			return ""
		}
		if class := r.fc.Resolve(phpnode.NodeText(scopeNode, r.src)); r.models.Lookup(class) != nil {
			return class
		}
	case "member_call_expression", "nullsafe_member_call_expression":
		nameNode := expr.ChildByFieldName("name")
		if nameNode == nil {
			return ""
		}
		method := phpnode.NodeText(nameNode, r.src)
		obj := expr.ChildByFieldName("object")
		if model := r.builderModel(obj, depth+1); model != "" {
			if !isBuilderMethod(method) {
				return ""
			}
			return model
		}
		return r.relationOrScopeOn(r.exprType(obj, depth+1), method)
	}
	return ""
}

// relationOrScopeOn resolves `$model->method()` called on an instance of
// model: a relationship method yields a builder of the related model, and a
// scope (forwarded to a fresh query) a builder of the model itself. A real
// relationship method wins over a same-named scope, as in PHP.
func (r *typeResolver) relationOrScopeOn(model phputil.FQN, method string) phputil.FQN {
	if model == "" {
		return ""
	}
	cat := r.models.Lookup(model)
	if cat == nil {
		return ""
	}
	for _, a := range cat.ByExposed[method] {
		if a.Kind == eloquent.Relationship && a.RelatedFQN != "" {
			return a.RelatedFQN
		}
	}
	if _, ok := cat.Scopes[method]; ok {
		return model
	}
	return ""
}

// isCollectionCall reports whether expr is a call returning a collection
// (`->get()`, `::all()`). exprType resolves those to their element model,
// which is right for iteration but wrong for calling methods on the result.
func (r *typeResolver) isCollectionCall(expr *ts.Node) bool {
	switch expr.Kind() {
	case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
		name := expr.ChildByFieldName("name")
		return name != nil && collectionMethods[phpnode.NodeText(name, r.src)]
	}
	return false
}
