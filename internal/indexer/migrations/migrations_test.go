package migrations_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/migrations"
)

const createAccounts = `<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        Schema::create('accounts', function (Blueprint $table) {
            $table->id();
            $table->string('name')->nullable();
            $table->foreignId('user_id')->constrained();
            $table->morphs('owner');
            $table->rememberToken();
            $table->timestamps();
            $table->softDeletes();
            $table->index('name');
            $table->unique(['name', 'user_id']);
            $table->foreign('user_id')->references('id')->on('users');
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('accounts');
    }
};
`

const alterAccounts = `<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class extends Migration
{
    public function up(): void
    {
        // Arrow function with a non-standard parameter name.
        Schema::table('accounts', fn (Blueprint $t) => $t->string('color')->default('#fff'));
        Schema::table('accounts', function (Blueprint $t) {
            $t->dropColumn('legacy');
            $t->renameColumn('name', 'title');
            $t->string('name')->change(); // redefining keeps the first location
        });
    }

    public function down(): void
    {
        // Re-adding a column while rolling back must not index it.
        Schema::table('accounts', function (Blueprint $table) {
            $table->string('legacy');
            $table->dropColumn('color');
        });
    }
};
`

func writeMigrations(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, "database", "migrations", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestWalk_CollectsColumnsPerTable(t *testing.T) {
	// Arrange
	root := writeMigrations(t, map[string]string{
		"2024_01_01_000000_create_accounts_table.php": createAccounts,
		"2024_02_01_000000_alter_accounts_table.php":  alterAccounts,
	})

	// Act
	schema := migrations.Walk(root, migrations.DefaultDirs)

	// Assert
	cols := schema.Columns("accounts")
	var got []string
	for name := range cols {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{
		"color", "created_at", "deleted_at", "id", "name", "owner_id", "owner_type",
		"remember_token", "title", "updated_at", "user_id",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("columns = %v\nwant      %v", got, want)
	}

	// The first definition wins and points into the create migration.
	name := cols["name"]
	if !strings.HasSuffix(name.Path, "create_accounts_table.php") || name.StartLine != 13 {
		t.Errorf("name location = %s:%d, want create_accounts_table.php:13", name.Path, name.StartLine)
	}
	if color := cols["color"]; !strings.HasSuffix(color.Path, "alter_accounts_table.php") {
		t.Errorf("color location = %s, want alter migration", color.Path)
	}

	if schema.Columns("missing") != nil {
		t.Error("Columns(missing) should be nil")
	}
}

func TestWalk_ConnectionAndCustomDirs(t *testing.T) {
	// Arrange
	root := t.TempDir()
	path := filepath.Join(root, "Modules", "Blog", "database", "migrations", "x.php")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `<?php
use Illuminate\Support\Facades\Schema;
return new class {
    public function up() {
        Schema::connection('tenant')->create('posts', function ($table) {
            $table->string('title');
        });
    }
};`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// Act
	schema := migrations.Walk(root, []string{"Modules/Blog/database/migrations"})

	// Assert
	if _, ok := schema.Columns("posts")["title"]; !ok {
		t.Errorf("posts.title missing; got %v", schema.Columns("posts"))
	}
}

func TestWalk_NoMigrationsDirIsEmpty(t *testing.T) {
	schema := migrations.Walk(t.TempDir(), migrations.DefaultDirs)
	if schema.Columns("users") != nil {
		t.Error("expected no columns")
	}
}

func TestNilSchemaIsSafe(t *testing.T) {
	var schema *migrations.Schema
	if schema.Columns("users") != nil {
		t.Error("nil schema returned columns")
	}
}

func TestWalk_ForeignIdForDerivesColumnFromModel(t *testing.T) {
	// Arrange
	root := writeMigrations(t, map[string]string{"x.php": `<?php
use App\Models\BlogPost;
return new class {
    public function up() {
        Schema::create('comments', function ($table) {
            $table->foreignIdFor(\App\Models\User::class);
            $table->foreignIdFor(BlogPost::class)->constrained();
            $table->foreignIdFor(BlogPost::class, 'parent_post_id');
        });
    }
};`})

	// Act
	cols := migrations.Walk(root, migrations.DefaultDirs).Columns("comments")

	// Assert
	for _, want := range []string{"user_id", "blog_post_id", "parent_post_id"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("column %q missing; got %v", want, cols)
		}
	}
}

// No directories means no columns — never a silent fallback to the default,
// which would hide a misconfigured migrationDirs glob.
func TestWalk_EmptyDirListScansNothing(t *testing.T) {
	root := writeMigrations(t, map[string]string{"x.php": createAccounts})
	if n := migrations.Walk(root, nil).TableCount(); n != 0 {
		t.Errorf("TableCount = %d, want 0", n)
	}
	if n := migrations.Walk(root, migrations.DefaultDirs).TableCount(); n != 1 {
		t.Errorf("TableCount = %d, want 1", n)
	}
}

func TestWalk_DefaultNamedHelpers(t *testing.T) {
	root := writeMigrations(t, map[string]string{"x.php": `<?php
return new class {
    public function up() {
        Schema::create('visits', function ($table) {
            $table->ipAddress();
            $table->macAddress();
            $table->ipAddress('client_ip');
        });
    }
};`})
	cols := migrations.Walk(root, migrations.DefaultDirs).Columns("visits")
	for _, want := range []string{"ip_address", "mac_address", "client_ip"} {
		if _, ok := cols[want]; !ok {
			t.Errorf("column %q missing; got %v", want, cols)
		}
	}
}
