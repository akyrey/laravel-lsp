package eloquent_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// vendorParents fakes the composer-backed resolver: it knows the parents of
// classes that live outside the scanned directories.
func vendorParents(fqn phputil.FQN) phputil.FQN {
	return map[phputil.FQN]phputil.FQN{
		"Illuminate\\Foundation\\Auth\\User":    "Illuminate\\Database\\Eloquent\\Model",
		"Spatie\\Permission\\Models\\Role":      "Illuminate\\Database\\Eloquent\\Model",
		"Illuminate\\Database\\Eloquent\\Model": "",
	}[fqn]
}

func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

var vendorParentFiles = map[string]string{
	"app/Models/User.php": `<?php
namespace App\Models;
use Illuminate\Foundation\Auth\User as Authenticatable;
class User extends Authenticatable {
    protected $fillable = ['email'];
}`,
	"app/Models/Admin.php": `<?php
namespace App\Models;
class Admin extends User {
    protected $fillable = ['level'];
}`,
	"app/Models/Role.php": `<?php
namespace App\Models;
class Role extends \Spatie\Permission\Models\Role {
    protected $fillable = ['guard_name'];
}`,
	"app/Services/Mailer.php": `<?php
namespace App\Services;
class Mailer extends \Some\Vendor\Base {}`,
}

func TestWalk_ModelsWithVendorAncestors(t *testing.T) {
	root := writeProject(t, vendorParentFiles)

	idx, err := eloquent.Walk(root, []string{"app"}, eloquent.WithExternalParents(vendorParents))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	for _, fqn := range []phputil.FQN{"App\\Models\\User", "App\\Models\\Admin", "App\\Models\\Role"} {
		if idx.Lookup(fqn) == nil {
			t.Errorf("%s not indexed as a model", fqn)
		}
	}
	if idx.Lookup("App\\Services\\Mailer") != nil {
		t.Error("App\\Services\\Mailer indexed as a model, want skipped")
	}
}

func TestWalk_WithoutExternalParentsKeepsOldBehaviour(t *testing.T) {
	root := writeProject(t, vendorParentFiles)

	idx, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if idx.Lookup("App\\Models\\User") != nil {
		t.Error("User indexed without an external resolver; its chain leaves the project")
	}
}

func TestReindexFile_KeepsExternalParents(t *testing.T) {
	root := writeProject(t, vendorParentFiles)
	idx, err := eloquent.Walk(root, []string{"app"}, eloquent.WithExternalParents(vendorParents))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	userPath := filepath.Join(root, "app/Models/User.php")
	if err := os.WriteFile(userPath, []byte(`<?php
namespace App\Models;
use Illuminate\Foundation\Auth\User as Authenticatable;
class User extends Authenticatable {
    protected $fillable = ['email', 'nickname'];
}`), 0o644); err != nil {
		t.Fatal(err)
	}

	newIdx, err := eloquent.ReindexFile(userPath, idx)
	if err != nil {
		t.Fatalf("ReindexFile: %v", err)
	}
	cat := newIdx.Lookup("App\\Models\\User")
	if cat == nil {
		t.Fatal("User dropped after reindex: external parents not retained")
	}
	if len(cat.ByExposed["nickname"]) == 0 {
		t.Error("reindexed User missing new 'nickname' attribute")
	}
}
