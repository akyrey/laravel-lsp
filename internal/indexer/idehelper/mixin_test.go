package idehelper_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/indexer/idehelper"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// mixinHelper mirrors `php artisan ide-helper:models --write-mixin` output:
// the stub class is IdeHelper<Model>, carries #[\AllowDynamicProperties]
// between its docblock and the class keyword, and the real model links to it
// with `@mixin IdeHelperAccount`.
const mixinHelper = `<?php

namespace Scrooge\Models{
/**
 * @property string $id
 * @property string $name
 * @property-read \Scrooge\Models\User $user
 * @mixin \Eloquent
 */
	#[\AllowDynamicProperties]
	final class IdeHelperAccount {}
}

namespace Scrooge\Models{
/**
 * @property string $email
 */
	#[\AllowDynamicProperties]
	final class IdeHelperUser {}
}
`

func TestMerge_WriteMixinFormatTargetsRealModel(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "_ide_helper_models.php")
	if err := os.WriteFile(path, []byte(mixinHelper), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := eloquent.NewModelIndex()
	idx.Add(&eloquent.ModelCatalog{
		Class:     "Scrooge\\Models\\Account",
		ByExposed: map[string][]eloquent.ModelAttribute{},
	})

	// Act
	if err := idehelper.Merge(path, idx); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	// Assert
	account := idx.Lookup("Scrooge\\Models\\Account")
	if account == nil {
		t.Fatal("Account catalog missing")
	}
	for _, name := range []string{"id", "name", "user"} {
		if len(account.ByExposed[name]) == 0 {
			t.Errorf("Account missing ide-helper property %q", name)
		}
	}
	var userRel eloquent.ModelAttribute
	for _, a := range account.ByExposed["user"] {
		userRel = a
	}
	if userRel.RelatedFQN != "Scrooge\\Models\\User" {
		t.Errorf("user RelatedFQN = %q, want Scrooge\\Models\\User", userRel.RelatedFQN)
	}

	// A model not yet indexed still gets its entries under the real name.
	if u := idx.Lookup("Scrooge\\Models\\User"); u == nil || len(u.ByExposed["email"]) == 0 {
		t.Error("User missing ide-helper property 'email'")
	}
	// The stub classes themselves never become models.
	for _, stub := range []string{"Scrooge\\Models\\IdeHelperAccount", "Scrooge\\Models\\IdeHelperUser"} {
		if idx.Lookup(phputil.FQN(stub)) != nil {
			t.Errorf("stub class %s indexed as a model", stub)
		}
	}
}

// Merge must never write into a catalog that may already be published to
// concurrent readers: it replaces it with an updated copy instead.
func TestMerge_CopiesCatalogsInsteadOfMutating(t *testing.T) {
	// Arrange
	path := filepath.Join(t.TempDir(), "_ide_helper_models.php")
	if err := os.WriteFile(path, []byte(mixinHelper), 0o644); err != nil {
		t.Fatal(err)
	}
	original := &eloquent.ModelCatalog{
		Class:     "Scrooge\\Models\\Account",
		ByExposed: map[string][]eloquent.ModelAttribute{},
	}
	idx := eloquent.NewModelIndex()
	idx.Add(original)

	// Act
	if err := idehelper.Merge(path, idx); err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(original.ByExposed) != 0 {
		t.Errorf("original catalog mutated: %v", original.ByExposed)
	}
	merged := idx.LookupDeclared("Scrooge\\Models\\Account")
	if merged == original || len(merged.ByExposed["name"]) == 0 {
		t.Error("index does not hold an updated copy with the ide-helper entries")
	}
}
