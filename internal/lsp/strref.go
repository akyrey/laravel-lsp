package lsp

import (
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// stringRefCall is a call whose first argument names a Laravel string
// reference: a config key, view, route, or env key.
type stringRefCall struct {
	kind   string   // "config", "view", "route", or "env" (see stringRefTargets)
	callee *ts.Node // the function or method name; also a jump trigger
	arg    *ts.Node // the first argument, possibly not a literal
	// existenceCheck marks has()/exists() calls, which legitimately probe
	// for keys that may be missing and so are never diagnosed.
	existenceCheck bool
}

// cursorOn reports whether offset is on the call's name or its first argument.
func (c stringRefCall) cursorOn(offset int) bool {
	return cursorOnNode(offset, c.callee) || cursorOnNode(offset, c.arg)
}

// helperFunctions maps global helper functions to their reference kind.
var helperFunctions = map[string]string{
	"config": "config", "view": "view", "route": "route", "env": "env",
	"to_route": "route",
}

// facadeMethods maps Facade::method to its reference kind, keyed by the
// facade's short name. A call matches only when the class resolves (through
// the file's imports) to Illuminate\Support\Facades\<name> or to the global
// alias <name> — never to a project class that happens to share the name.
var facadeMethods = map[string]map[string]string{
	"Config": {
		"get": "config", "has": "config", "string": "config", "integer": "config",
		"float": "config", "boolean": "config", "array": "config",
	},
	"View":     {"make": "view", "exists": "view", "first": "view"},
	"Route":    {"has": "route"},
	"Redirect": {"route": "route"},
	"URL":      {"route": "route"},
}

// existenceMethods never produce diagnostics.
var existenceMethods = map[string]bool{"has": true, "exists": true}

// routeReceivers are helpers whose result has a ->route('name') method that
// takes a route name (unlike $request->route('param')).
var routeReceivers = map[string]bool{"redirect": true, "url": true}

// facadeNamespace is where Laravel's facades live.
const facadeNamespace = "Illuminate\\Support\\Facades\\"

// findStringRefCalls returns every recognised string-reference call in tree,
// in source order. fc must hold the file's namespace and imports (pass the
// one a visitor already built, or fileContextOf).
func findStringRefCalls(tree *ts.Tree, src []byte, fc *phputil.FileContext) []stringRefCall {
	var out []stringRefCall
	walkNamed(tree.RootNode(), func(n *ts.Node) bool {
		if c, ok := matchStringRefCall(n, src, fc); ok {
			out = append(out, c)
		}
		return true
	})
	return out
}

// matchStringRefCall recognises one call node.
func matchStringRefCall(n *ts.Node, src []byte, fc *phputil.FileContext) (stringRefCall, bool) {
	var kind, method string
	var callee *ts.Node
	switch n.Kind() {
	case "function_call_expression":
		callee = n.ChildByFieldName("function")
		if callee == nil {
			return stringRefCall{}, false
		}
		kind = helperFunctions[helperName(callee, src)]
	case "scoped_call_expression":
		callee = n.ChildByFieldName("name")
		scope := n.ChildByFieldName("scope")
		if callee == nil || scope == nil {
			return stringRefCall{}, false
		}
		method = phpnode.NodeText(callee, src)
		kind = facadeMethods[facadeName(phpnode.NodeText(scope, src), fc)][method]
	case "member_call_expression":
		callee = n.ChildByFieldName("name")
		if callee != nil && phpnode.NodeText(callee, src) == "route" && isRouteReceiver(n.ChildByFieldName("object"), src) {
			kind = "route"
		}
	}
	if kind == "" {
		return stringRefCall{}, false
	}
	argsNode := n.ChildByFieldName("arguments")
	if argsNode == nil {
		return stringRefCall{}, false
	}
	args := phpwalk.ArgExprs(argsNode, src)
	if len(args) == 0 {
		return stringRefCall{}, false
	}
	return stringRefCall{kind: kind, callee: callee, arg: args[0], existenceCheck: existenceMethods[method]}, true
}

// isRouteReceiver matches `redirect()` / `url()` call expressions.
func isRouteReceiver(obj *ts.Node, src []byte) bool {
	if obj == nil || obj.Kind() != "function_call_expression" {
		return false
	}
	fn := obj.ChildByFieldName("function")
	return fn != nil && routeReceivers[helperName(fn, src)]
}

// helperName normalises a global function callee: PHP function names are
// case-insensitive and may be written fully qualified (\config).
func helperName(callee *ts.Node, src []byte) string {
	return strings.ToLower(strings.TrimPrefix(phpnode.NodeText(callee, src), "\\"))
}

// facadeName returns the facade short name ("Config") when class resolves to
// a Laravel facade or its global alias, or "" otherwise.
func facadeName(class string, fc *phputil.FileContext) string {
	fqn := string(fc.Resolve(class))
	if short, ok := strings.CutPrefix(fqn, facadeNamespace); ok {
		return short
	}
	if !strings.Contains(fqn, "\\") {
		return fqn // global alias: \Config, or Config in an un-namespaced file
	}
	return ""
}
