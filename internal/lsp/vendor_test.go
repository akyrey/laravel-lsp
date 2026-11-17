package lsp

import (
	"path/filepath"
	"testing"
)

// writeVendorLaravel lays down the slice of vendor/ that a typical
// `User extends Authenticatable` model depends on, plus Composer metadata.
func writeVendorLaravel(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "vendor/composer/installed.json"), `{"packages": [
		{"name": "laravel/framework", "install-path": "../laravel/framework",
		 "autoload": {"psr-4": {"Illuminate\\": "src/Illuminate/"}}}
	]}`)
	writeFile(t, filepath.Join(root, "vendor/laravel/framework/src/Illuminate/Foundation/Auth/User.php"), `<?php
namespace Illuminate\Foundation\Auth;
use Illuminate\Database\Eloquent\Model;
class User extends Model {}`)
	writeFile(t, filepath.Join(root, "vendor/laravel/framework/src/Illuminate/Database/Eloquent/Model.php"), `<?php
namespace Illuminate\Database\Eloquent;
abstract class Model {}`)
}

func TestReindex_ResolvesVendorAncestorsThroughComposer(t *testing.T) {
	root := t.TempDir()
	writeVendorLaravel(t, root)
	userPath := filepath.Join(root, "app/Models/User.php")
	writeFile(t, userPath, `<?php
namespace App\Models;
use Illuminate\Foundation\Auth\User as Authenticatable;
class User extends Authenticatable {
    protected $fillable = ['email'];
}`)

	s := newRootedTestServer(t, root)
	s.reindex(root)

	if s.models.Lookup("App\\Models\\User") == nil {
		t.Fatal("User (extends vendor Authenticatable) not indexed after reindex")
	}

	// The incremental path must keep the resolver too.
	s.reindexFiles(root, []string{userPath})
	if s.models.Lookup("App\\Models\\User") == nil {
		t.Fatal("User dropped after incremental reindex")
	}
}
