package composer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/composer"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// writeFiles materialises files (path relative to root → content) on disk.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// laravelProject returns a minimal project with a Composer 2 installed.json
// that maps Illuminate\ (single dir) and Illuminate\Support\ (dir list), plus
// a root composer.json mapping App\ to app/.
func laravelProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"composer.json": `{"autoload": {"psr-4": {"App\\": "app/"}},
			"autoload-dev": {"psr-4": {"Tests\\": "tests/"}}}`,
		"vendor/composer/installed.json": `{"packages": [
			{"name": "laravel/framework", "install-path": "../laravel/framework",
			 "autoload": {"psr-4": {
				"Illuminate\\": "src/Illuminate/",
				"Illuminate\\Support\\": ["src/Illuminate/Macroable/", "src/Illuminate/Collections/"]}}},
			{"name": "spatie/laravel-permission", "install-path": "../spatie/laravel-permission",
			 "autoload": {"psr-4": {"Spatie\\Permission\\": "src"}}}
		]}`,
		"vendor/laravel/framework/src/Illuminate/Database/Eloquent/Model.php": `<?php
namespace Illuminate\Database\Eloquent;
abstract class Model {}`,
		"vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php": `<?php
namespace Illuminate\Foundation\Auth;
use Illuminate\Database\Eloquent\Model;
class User extends Model {}`,
		"vendor/laravel/framework/src/Illuminate/Collections/Collection.php": `<?php
namespace Illuminate\Support;
class Collection {}`,
		"vendor/spatie/laravel-permission/src/Models/Role.php": `<?php
namespace Spatie\Permission\Models;
class Role extends \Illuminate\Database\Eloquent\Model {}`,
		"app/Models/Base.php": `<?php
namespace App\Models;
abstract class Base extends \Illuminate\Database\Eloquent\Model {}`,
	})
	return root
}

func TestLocate(t *testing.T) {
	root := laravelProject(t)
	auto, err := composer.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct {
		fqn  phputil.FQN
		want string // relative to root, "" = not found
	}{
		{"Illuminate\\Foundation\\Auth\\User", "vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php"},
		{"Illuminate\\Database\\Eloquent\\Model", "vendor/laravel/framework/src/Illuminate/Database/Eloquent/Model.php"},
		// Second directory of a multi-dir mapping.
		{"Illuminate\\Support\\Collection", "vendor/laravel/framework/src/Illuminate/Collections/Collection.php"},
		// Mapping without trailing slash.
		{"Spatie\\Permission\\Models\\Role", "vendor/spatie/laravel-permission/src/Models/Role.php"},
		// Root package mapping from composer.json.
		{"App\\Models\\Base", "app/Models/Base.php"},
		// Leading backslash is tolerated.
		{"\\App\\Models\\Base", "app/Models/Base.php"},
		{"Illuminate\\Nope\\Missing", ""},
		{"Unknown\\Vendor\\Thing", ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.fqn), func(t *testing.T) {
			got := auto.Locate(tc.fqn)
			want := ""
			if tc.want != "" {
				want = filepath.Join(root, tc.want)
			}
			if got != want {
				t.Errorf("Locate(%s) = %q, want %q", tc.fqn, got, want)
			}
		})
	}
}

func TestLoad_Composer1InstalledJSON(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		// Composer 1 wrote a bare array and had no install-path; packages
		// live at vendor/<name>.
		"vendor/composer/installed.json": `[
			{"name": "acme/lib", "autoload": {"psr-4": {"Acme\\": "src/"}}}
		]`,
		"vendor/acme/lib/src/Thing.php": `<?php namespace Acme; class Thing {}`,
	})
	auto, err := composer.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(root, "vendor/acme/lib/src/Thing.php")
	if got := auto.Locate("Acme\\Thing"); got != want {
		t.Errorf("Locate = %q, want %q", got, want)
	}
}

func TestLoad_NoComposerFilesIsEmptyNotError(t *testing.T) {
	auto, err := composer.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load on bare dir: %v", err)
	}
	if got := auto.Locate("Illuminate\\Database\\Eloquent\\Model"); got != "" {
		t.Errorf("Locate on empty autoload = %q, want empty", got)
	}
}

func TestLoad_MalformedInstalledJSONIsError(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"vendor/composer/installed.json": `{not json`})
	if _, err := composer.Load(root); err == nil {
		t.Error("Load with malformed installed.json: want error, got nil")
	}
}

func TestParentOf(t *testing.T) {
	root := laravelProject(t)
	auto, err := composer.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	parents := composer.NewParents(auto)

	tests := []struct {
		fqn  phputil.FQN
		want phputil.FQN
	}{
		// Parent resolved through a use statement.
		{"Illuminate\\Foundation\\Auth\\User", "Illuminate\\Database\\Eloquent\\Model"},
		// Parent written fully qualified.
		{"Spatie\\Permission\\Models\\Role", "Illuminate\\Database\\Eloquent\\Model"},
		// Class with no parent.
		{"Illuminate\\Database\\Eloquent\\Model", ""},
		// Class that cannot be located.
		{"Illuminate\\Nope\\Missing", ""},
	}
	for _, tc := range tests {
		t.Run(string(tc.fqn), func(t *testing.T) {
			if got := parents.ParentOf(tc.fqn); got != tc.want {
				t.Errorf("ParentOf(%s) = %q, want %q", tc.fqn, got, tc.want)
			}
			// Memoized second call must agree.
			if got := parents.ParentOf(tc.fqn); got != tc.want {
				t.Errorf("memoized ParentOf(%s) = %q, want %q", tc.fqn, got, tc.want)
			}
		})
	}
}

func TestParentOf_NilSafe(t *testing.T) {
	var parents *composer.Parents
	if got := parents.ParentOf("Anything"); got != "" {
		t.Errorf("nil Parents.ParentOf = %q, want empty", got)
	}
}

// A broken installed.json (e.g. mid `composer install`) must not discard the
// project's own composer.json mappings.
func TestLoad_MalformedInstalledJSONKeepsRootMappings(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"composer.json":                  `{"autoload": {"psr-4": {"App\\": "app/"}}}`,
		"vendor/composer/installed.json": `{not json`,
		"app/Models/User.php":            `<?php namespace App\Models; class User {}`,
	})

	auto, err := composer.Load(root)

	if err == nil {
		t.Error("want an error for the malformed installed.json")
	}
	if got := auto.Locate("App\\Models\\User"); got == "" {
		t.Error("root PSR-4 mapping lost because installed.json was malformed")
	}
}

// A file that cannot be parsed right now (e.g. being written) must not be
// cached as "no parent" forever.
func TestParentOf_DoesNotMemoizeUnreadableFiles(t *testing.T) {
	root := laravelProject(t)
	auto, err := composer.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	parents := composer.NewParents(auto)
	path := filepath.Join(root, "vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	if got := parents.ParentOf("Illuminate\\Foundation\\Auth\\User"); got != "" {
		t.Skipf("file still readable (running as root?): %q", got)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := parents.ParentOf("Illuminate\\Foundation\\Auth\\User"); got != "Illuminate\\Database\\Eloquent\\Model" {
		t.Errorf("after the file became readable ParentOf = %q, want Model", got)
	}
}
