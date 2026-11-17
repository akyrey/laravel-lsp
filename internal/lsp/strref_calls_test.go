package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/indexer/strindex"
)

func TestFindDefinition_StringReferenceCallForms(t *testing.T) {
	root := writeStrRefFixture(t)
	strs := strindex.Walk(root)
	configFile := filepath.Join(root, "config", "app.php")
	viewFile := filepath.Join(root, "resources", "views", "users", "index.blade.php")
	routeFile := filepath.Join(root, "routes", "web.php")

	tests := []struct {
		name     string
		code     string
		cursorAt string // cursor goes one byte into the first occurrence
		wantPath string // "" = no result
	}{
		{"cursor on the config helper name", `config('app.name')`, "config", configFile},
		{"cursor on the view helper name", `view('users.index')`, "view", viewFile},
		{"to_route helper", `to_route('home')`, "home", routeFile},
		{"redirect()->route()", `redirect()->route('home')`, "home", routeFile},
		{"cursor on ->route method name", `redirect()->route('home')`, "route(", routeFile},
		{"url()->route()", `url()->route('home')`, "home", routeFile},
		{"Config facade", `Config::get('app.name')`, "app.name", configFile},
		{"fully-qualified Config facade", `\Illuminate\Support\Facades\Config::string('app.name')`, "app.name", configFile},
		{"View facade", `View::make('users.index')`, "users.index", viewFile},
		{"Redirect facade", `Redirect::route('home')`, "home", routeFile},
		{"request route parameter is not a route name", `$request->route('home')`, "home", ""},
		{"unrelated static call", `Cache::get('app.name')`, "app.name", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "<?php\n$x = " + tc.code + ";\n"
			offset := strings.Index(src, tc.cursorAt) + 1

			locs := findDefinition([]byte(src), "/fake/Ctrl.php", offset,
				container.NewBindingIndex(), eloquent.NewModelIndex(), strs, newDocumentStore())

			if tc.wantPath == "" {
				if len(locs) != 0 {
					t.Errorf("locations = %+v, want none", locs)
				}
				return
			}
			if len(locs) != 1 || URIToPath(locs[0].URI) != tc.wantPath {
				t.Errorf("locations = %+v, want %s", locs, tc.wantPath)
			}
		})
	}
}

func TestDiagnostics_StringReferenceCallForms(t *testing.T) {
	root := writeStrRefFixture(t)
	strs := strindex.Walk(root)
	src := `<?php
$a = Config::get('app.missing');
$b = to_route('nowhere');
$c = View::exists('maybe.missing');
$d = Route::has('maybe');
$e = Config::has('maybe');
$f = $request->route('param');
$g = config('app.name');
`
	diags := collectDiagnostics([]byte(src), "/fake/Ctrl.php", eloquent.NewModelIndex(), strs, defaultDiagOptions())

	var got []string
	for _, d := range diags {
		got = append(got, d.Message)
	}
	want := []string{"unknown config key 'app.missing'", "unknown route 'nowhere'"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("diagnostics = %q, want %q", got, want)
	}
}

func TestHoverAndCompletion_StringReferenceCallForms(t *testing.T) {
	root := writeStrRefFixture(t)
	strs := strindex.Walk(root)

	src := []byte("<?php\n$x = to_route('home');\n")
	ref := identifyStringRef(src, "/fake/Ctrl.php", strings.Index(string(src), "home")+1, strs)
	if ref == nil || !ref.found || ref.fnName != "route" {
		t.Errorf("hover ref = %+v, want resolved route", ref)
	}

	partial := "<?php\n$x = Config::get('ap');\n"
	items := stringRefCompletions([]byte(partial), strings.Index(partial, "ap")+2, strs)
	found := false
	for _, it := range items {
		if it.Label == "app.name" {
			found = true
		}
	}
	if !found {
		t.Errorf("Config::get completion lacks app.name: %d items", len(items))
	}
}

func TestStringRefCalls_FacadesResolveThroughImports(t *testing.T) {
	root := writeStrRefFixture(t)
	strs := strindex.Walk(root)
	configFile := filepath.Join(root, "config", "app.php")

	tests := []struct {
		name     string
		src      string
		wantPath string
	}{
		{"imported Laravel facade", `<?php
namespace App\Http;
use Illuminate\Support\Facades\Config;
$x = Config::get('app.name');`, configFile},
		{"global alias with leading backslash", `<?php
namespace App\Http;
$x = \Config::get('app.name');`, configFile},
		{"global alias in an un-namespaced file", `<?php
$x = Config::get('app.name');`, configFile},
		{"project class that happens to be called Config", `<?php
namespace App\Http;
use App\Support\Config;
$x = Config::get('app.name');`, ""},
		{"helper names are case-insensitive", `<?php
$x = CONFIG('app.name');`, configFile},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			offset := strings.Index(tc.src, "app.name") + 1
			locs := findDefinition([]byte(tc.src), "/fake/C.php", offset,
				container.NewBindingIndex(), eloquent.NewModelIndex(), strs, newDocumentStore())
			if tc.wantPath == "" {
				if len(locs) != 0 {
					t.Errorf("locations = %+v, want none", locs)
				}
				diags := collectDiagnostics([]byte(strings.Replace(tc.src, "app.name", "app.nope", 1)),
					"/fake/C.php", eloquent.NewModelIndex(), strs, defaultDiagOptions())
				if len(diags) != 0 {
					t.Errorf("diagnostics on a non-Laravel class: %+v", diags)
				}
				return
			}
			if len(locs) != 1 || URIToPath(locs[0].URI) != tc.wantPath {
				t.Errorf("locations = %+v, want %s", locs, tc.wantPath)
			}
		})
	}
}
