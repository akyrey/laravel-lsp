package eloquent_test

import (
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// fakeSchema is a stand-in for migrations.Schema.
type fakeSchema map[string]map[string]phputil.Location

func (f fakeSchema) Columns(table string) map[string]phputil.Location { return f[table] }

func colLoc(line int) phputil.Location {
	return phputil.Location{Path: "/db/migrations/create.php", StartLine: line, StartByte: line * 10, EndLine: line, EndByte: line*10 + 4}
}

func TestLookup_AddsMigrationColumnsForModelTable(t *testing.T) {
	// Arrange
	root := writeProject(t, map[string]string{
		"app/Models/Account.php": `<?php
namespace App\Models;
class Account extends \Illuminate\Database\Eloquent\Model {
    protected $fillable = ['name'];
}`,
		"app/Models/Person.php": `<?php
namespace App\Models;
class Person extends \Illuminate\Database\Eloquent\Model {
    protected $table = 'humans';
}`,
		"app/Models/Admin.php": `<?php
namespace App\Models;
class Admin extends Person {}`,
	})
	idx, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	schema := fakeSchema{
		"accounts": {"id": colLoc(1), "name": colLoc(2)},
		"humans":   {"nickname": colLoc(3)},
		"people":   {"wrong": colLoc(4)},
		"admins":   {"wrong": colLoc(5)},
	}

	// Act
	withCols := idx.WithSchema(schema)

	// Assert: convention table name, column added after the declared entry.
	account := withCols.Lookup("App\\Models\\Account")
	if got := kinds(account.ByExposed["id"]); len(got) != 1 || got[0] != eloquent.MigrationColumn {
		t.Errorf("Account.id kinds = %v, want [MigrationColumn]", got)
	}
	name := account.ByExposed["name"]
	if len(name) != 2 || name[0].Kind != eloquent.FillableArray || name[1].Kind != eloquent.MigrationColumn {
		t.Errorf("Account.name entries = %v, want [Fillable, MigrationColumn]", kinds(name))
	}
	if name[1].Source != eloquent.SourceMigration || name[1].Location != colLoc(2) {
		t.Errorf("column entry = %+v, want SourceMigration at colLoc(2)", name[1])
	}

	// Explicit $table wins over the convention, and is inherited.
	for _, fqn := range []phputil.FQN{"App\\Models\\Person", "App\\Models\\Admin"} {
		cat := withCols.Lookup(fqn)
		if len(cat.ByExposed["nickname"]) != 1 {
			t.Errorf("%s missing column from $table 'humans'", fqn)
		}
		if len(cat.ByExposed["wrong"]) != 0 {
			t.Errorf("%s picked up columns from the conventional table", fqn)
		}
	}

	// The original index is untouched (WithSchema returns a new index).
	if len(idx.Lookup("App\\Models\\Account").ByExposed["id"]) != 0 {
		t.Error("WithSchema mutated the receiver")
	}
	// Declared catalogs never include columns.
	if len(withCols.LookupDeclared("App\\Models\\Account").ByExposed["id"]) != 0 {
		t.Error("LookupDeclared includes migration columns")
	}
}

func TestWithSchema_SurvivesReindexFile(t *testing.T) {
	root := writeProject(t, map[string]string{
		"app/Models/Account.php": `<?php
namespace App\Models;
class Account extends \Illuminate\Database\Eloquent\Model {}`,
	})
	idx, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	withCols := idx.WithSchema(fakeSchema{"accounts": {"id": colLoc(1)}})

	newIdx, err := eloquent.ReindexFile(root+"/app/Models/Account.php", withCols)
	if err != nil {
		t.Fatal(err)
	}
	if len(newIdx.Lookup("App\\Models\\Account").ByExposed["id"]) != 1 {
		t.Error("schema lost after ReindexFile")
	}
}

func kinds(attrs []eloquent.ModelAttribute) []eloquent.AttributeKind {
	out := make([]eloquent.AttributeKind, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, a.Kind)
	}
	return out
}
