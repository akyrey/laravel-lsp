package lsp

import (
	"path/filepath"
	"strings"
	"testing"

	protocol "github.com/tliron/glsp/protocol_3_16"
)

const columnsModel = `<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Model;
class Account extends Model {
    protected $fillable = ['title'];
}`

const columnsMigration = `<?php
use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;
return new class extends Migration {
    public function up(): void {
        Schema::create('accounts', function (Blueprint $table) {
            $table->id();
            $table->string('name');
            $table->string('title');
        });
    }
};`

const columnsController = `<?php
namespace App\Http;
use App\Models\Account;
class C {
    public function show(Account $account) {
        return $account->name;
    }
}`

// columnsProject indexes a project whose Account.name exists only as a
// migration column.
func columnsProject(t *testing.T) (*Server, string, string) {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "app/Models/Account.php"), columnsModel)
	migration := filepath.Join(root, "database/migrations/2024_01_01_000000_create_accounts_table.php")
	writeFile(t, migration, columnsMigration)
	ctrl := filepath.Join(root, "app/Http/C.php")
	writeFile(t, ctrl, columnsController)
	s := newRootedTestServer(t, root)
	s.reindex(root)
	return s, ctrl, migration
}

func TestDefinition_JumpsToMigrationColumn(t *testing.T) {
	// Arrange
	s, ctrl, migration := columnsProject(t)
	src := []byte(columnsController)
	offset := strings.Index(columnsController, "->name") + 3

	// Act
	locs := findDefinition(src, ctrl, offset, s.bindings, s.models, s.strIndex, s.docs)

	// Assert
	if len(locs) != 1 {
		t.Fatalf("locations = %+v, want exactly one", locs)
	}
	if locs[0].URI != PathToURI(migration) || locs[0].Range.Start.Line != 8 {
		t.Errorf("location = %s:%d, want migration line 9", locs[0].URI, locs[0].Range.Start.Line+1)
	}
}

func TestDiagnostics_MigrationColumnIsKnown(t *testing.T) {
	s, ctrl, _ := columnsProject(t)

	diags := collectDiagnostics([]byte(columnsController), ctrl, s.models, s.strIndex, defaultDiagOptions())

	if len(diags) != 0 {
		t.Errorf("diagnostics = %+v, want none for a migration column", diags)
	}
}

func TestHover_LabelsMigrationColumn(t *testing.T) {
	s, _, _ := columnsProject(t)
	sym := &refSymbol{modelFQN: "App\\Models\\Account", propName: "name"}

	md := hoverMarkdown(sym, s.bindings, s.models)

	if !strings.Contains(md, "column") || !strings.Contains(md, "create_accounts_table.php") {
		t.Errorf("hover = %q, want column label with migration file", md)
	}
}

func TestCompletion_IncludesMigrationColumns(t *testing.T) {
	s, ctrl, _ := columnsProject(t)
	src := strings.Replace(columnsController, "$account->name;", "$account->", 1)
	offset := strings.Index(src, "$account->") + len("$account->")

	items := eloquentCompletions([]byte(src), ctrl, offset, s.models)

	found := false
	for _, it := range items {
		if it.Label == "name" {
			found = true
		}
	}
	if !found {
		t.Errorf("completion items lack migration column 'name': %+v", items)
	}
}

func TestRename_NeverEditsMigrations(t *testing.T) {
	// Arrange: 'title' is both $fillable and a column.
	s, ctrl, migration := columnsProject(t)
	src := strings.Replace(columnsController, "->name", "->title", 1)
	writeFile(t, ctrl, src)
	pos := byteOffsetToPosition([]byte(src), strings.Index(src, "->title")+3)

	// Act
	edit, err := s.Rename(nil, &protocol.RenameParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: PathToURI(ctrl)},
			Position:     pos,
		},
		NewName: "headline",
	})

	// Assert
	if err != nil || edit == nil {
		t.Fatalf("Rename: edit=%v err=%v", edit, err)
	}
	if _, touched := edit.Changes[PathToURI(migration)]; touched {
		t.Error("rename edited a migration file")
	}
	if len(edit.Changes[PathToURI(ctrl)]) == 0 {
		t.Error("rename did not edit the reference site")
	}
}

// byteOffsetToPosition converts an ASCII byte offset to an LSP position.
func byteOffsetToPosition(src []byte, offset int) protocol.Position {
	before := string(src[:offset])
	line := strings.Count(before, "\n")
	col := offset - (strings.LastIndex(before, "\n") + 1)
	return protocol.Position{Line: uint32(line), Character: uint32(col)}
}

func TestParseConfig_MigrationDirs(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		want []string
	}{
		{"default", map[string]any{}, []string{"database/migrations"}},
		{"explicit", map[string]any{"migrationDirs": []string{"database/migrations", "Modules/*/database/migrations"}},
			[]string{"database/migrations", "Modules/*/database/migrations"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := parseConfig(tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(cfg.MigrationDirs, tc.want) {
				t.Errorf("MigrationDirs = %v, want %v", cfg.MigrationDirs, tc.want)
			}
		})
	}
}

// A migration edit must refresh the columns on the incremental path.
func TestReindexFiles_MigrationChangeRefreshesColumns(t *testing.T) {
	s, _, migration := columnsProject(t)
	writeFile(t, migration, strings.Replace(columnsMigration, "$table->id();", "$table->id();\n            $table->string('iban');", 1))

	s.reindexFiles(s.root, []string{migration})

	if len(s.models.Lookup("App\\Models\\Account").ByExposed["iban"]) == 0 {
		t.Error("new column 'iban' missing after incremental reindex")
	}
}
