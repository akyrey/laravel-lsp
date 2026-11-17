package eloquent_test

import (
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
)

// Laravel 12 lets a scope be declared as a plain method marked #[Scope],
// called by its own name: Post::popular() → popular().
func TestWalk_ScopeAttribute(t *testing.T) {
	root := writeProject(t, map[string]string{
		"app/Traits/HasOrdering.php": `<?php
namespace App\Traits;
use Illuminate\Database\Eloquent\Attributes\Scope;
use Illuminate\Database\Eloquent\Builder;
trait HasOrdering {
    #[Scope]
    protected function ordered(Builder $query, array $sorting): void {}
}`,
		"app/Models/Post.php": `<?php
namespace App\Models;
use App\Traits\HasOrdering;
use Illuminate\Database\Eloquent\Attributes\Scope;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Database\Eloquent\Model;
class Post extends Model {
    use HasOrdering;

    #[Scope]
    protected function popular(Builder $query): void {}

    // Fully-qualified attribute name.
    #[\Illuminate\Database\Eloquent\Attributes\Scope]
    protected function recent(Builder $query): void {}

    // An unrelated attribute named Scope from another namespace is not a scope.
    #[\App\Attributes\Scope]
    protected function notAScope(): void {}

    // Plain helper method, no attribute.
    protected function helper(): void {}
}`,
	})

	idx, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	cat := idx.Lookup("App\\Models\\Post")
	if cat == nil {
		t.Fatal("Post not indexed")
	}

	for _, name := range []string{"popular", "recent", "ordered"} {
		loc, ok := cat.Scopes[name]
		if !ok {
			t.Errorf("scope %q missing; got %v", name, cat.Scopes)
			continue
		}
		if loc.Zero() {
			t.Errorf("scope %q has zero location", name)
		}
	}
	for _, name := range []string{"notAScope", "helper"} {
		if _, ok := cat.Scopes[name]; ok {
			t.Errorf("%q indexed as a scope", name)
		}
	}
	// Scopes are never exposed as properties.
	if len(cat.ByExposed["popular"]) != 0 {
		t.Error("#[Scope] method leaked into ByExposed")
	}
}
