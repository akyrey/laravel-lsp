package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
)

const builderPost = `<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Attributes\Scope;
use Illuminate\Database\Eloquent\Model;
class Post extends Model {
    protected $fillable = ['title'];
    public function scopePublished($q) {}
    #[Scope]
    protected function popular($q) {}
}`

const builderUser = `<?php
namespace App\Models;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;
class User extends Model {
    public function posts(): HasMany { return $this->hasMany(Post::class); }
}`

func builderModels(t *testing.T) (*eloquent.ModelIndex, string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"app/Models/Post.php": builderPost,
		"app/Models/User.php": builderUser,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}
	return idx, filepath.Join(root, "app/Models/Post.php")
}

func TestDefinition_ScopeInBuilderChain(t *testing.T) {
	// Lines (1-based) of the scope declarations in builderPost.
	const publishedLine, popularLine = 7, 9

	tests := []struct {
		name     string
		code     string // the cursor goes on the first occurrence of target
		target   string
		wantLine int // 0 = no result
	}{
		{"after query()", `Post::query()->published();`, "published", publishedLine},
		{"after where()", `Post::where('a', 1)->popular()->get();`, "popular", popularLine},
		{"scope after scope", `Post::published()->popular();`, "popular", popularLine},
		{"on relation method", `$user->posts()->published();`, "published", publishedLine},
		{"on model instance", `$post->popular();`, "popular", popularLine},
		{"nullsafe on instance", `$post?->popular();`, "popular", popularLine},
		{"after terminal get() is a collection", `Post::query()->get()->published();`, "published", 0},
		{"not a scope", `Post::query()->latest();`, "latest", 0},
	}
	models, postPath := builderModels(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := `<?php
namespace App\Http;
use App\Models\Post;
use App\Models\User;
class C {
    public function m(User $user, Post $post) {
        ` + tc.code + `
    }
}`
			offset := strings.Index(src, tc.code) + strings.Index(tc.code, tc.target) + 1
			if tc.name == "scope after scope" {
				offset = strings.Index(src, "->popular") + 3
			}

			locs := findDefinition([]byte(src), "/app/Http/C.php", offset,
				container.NewBindingIndex(), models, nil, newDocumentStore())

			if tc.wantLine == 0 {
				if len(locs) != 0 {
					t.Errorf("locations = %+v, want none", locs)
				}
				return
			}
			if len(locs) != 1 || locs[0].URI != PathToURI(postPath) || int(locs[0].Range.Start.Line)+1 != tc.wantLine {
				t.Errorf("locations = %+v, want Post.php:%d", locs, tc.wantLine)
			}
		})
	}
}

func TestResolveExprType_BuilderTerminals(t *testing.T) {
	got := typesOfFetches(t, `<?php
use App\Models\User;
$a = User::where('x', 1)->first();
echo $a->email_address;
foreach (User::query()->get() as $b) { echo $b->email_address; }
$c = User::query()->count();
echo $c->email_address;`, "email_address")
	want := []string{"App\\Models\\User", "App\\Models\\User", ""}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Errorf("fetch %d: %q, want %q", i, got[i], want[i])
		}
	}
}
