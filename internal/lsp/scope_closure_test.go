package lsp

import (
	"strings"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// fetchCollector gathers every property fetch while maintaining the file
// context, exactly as the LSP visitors do.
type fetchCollector struct {
	phpwalk.NullVisitor
	fc      *phputil.FileContext
	fetches []phpwalk.PropertyFetchInfo
}

func (v *fetchCollector) VisitNamespace(ns string) { v.fc.Namespace = phputil.FQN(ns) }
func (v *fetchCollector) VisitUseItem(alias, fqn string) {
	v.fc.Uses[alias] = phputil.FQN(fqn)
}
func (v *fetchCollector) VisitPropertyFetch(n phpwalk.PropertyFetchInfo) {
	v.fetches = append(v.fetches, n)
}

// typesOfFetches resolves the LHS type of every `->prop` fetch named prop,
// in source order.
func typesOfFetches(t *testing.T, src, prop string) []phputil.FQN {
	t.Helper()
	b := []byte(src)
	tree, err := phpnode.ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	c := &fetchCollector{fc: &phputil.FileContext{Uses: make(phputil.UseMap)}}
	phpwalk.Walk("", b, tree, c)

	models := testModelIndex(t)
	var out []phputil.FQN
	for _, f := range c.fetches {
		if f.PropName == prop {
			out = append(out, resolveExprType(f.VarRaw, b, c.fc, models))
		}
	}
	if len(out) == 0 {
		t.Fatalf("no ->%s fetch in fixture", prop)
	}
	return out
}

const user = phputil.FQN("App\\Models\\User")

func TestResolveExprType_Scopes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []phputil.FQN // one per ->email_address fetch, in order
	}{
		{
			name: "closure parameter",
			src: `<?php
use App\Models\User;
Gate::define('x', function (?User $u) { return $u->email_address; });`,
			want: []phputil.FQN{user},
		},
		{
			name: "arrow function parameter, nullsafe access",
			src: `<?php
use App\Models\User;
Gate::define('x', fn (?User $u = null) => $u?->email_address);`,
			want: []phputil.FQN{user},
		},
		{
			name: "arrow function captures outer variable",
			src: `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function m(User $u) {
        return collect()->map(fn ($x) => $u->email_address);
    }
}`,
			want: []phputil.FQN{user},
		},
		{
			name: "closure sees outer variable only through use()",
			src: `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function m(User $u) {
        $a = function () use ($u) { return $u->email_address; };
        $b = function () { return $u->email_address; };
    }
}`,
			want: []phputil.FQN{user, ""},
		},
		{
			name: "plain function and top-level route file",
			src: `<?php
use App\Models\User;
function helper(User $u) { return $u->email_address; }
$v = User::findOrFail(1);
echo $v->email_address;`,
			want: []phputil.FQN{user, user},
		},
		{
			name: "assignment only visible after it happens",
			src: `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function m() {
        echo $u->email_address;
        $u = new User();
        echo $u->email_address;
    }
}`,
			want: []phputil.FQN{"", user},
		},
		{
			name: "assignment from a relation chain",
			src: `<?php
namespace App\Http;
use App\Models\Post;
class C {
    public function m(Post $p) {
        $author = $p->author;
        return $author->email_address;
    }
}`,
			want: []phputil.FQN{user},
		},
		{
			name: "self-referencing reassignment does not loop",
			src: `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function m(User $u) {
        $u = $u->posts->author;
        return $u->email_address;
    }
}`,
			want: []phputil.FQN{user},
		},
		{
			name: "foreach over a relation",
			src: `<?php
namespace App\Http;
use App\Models\Post;
class C {
    public function m(Post $p) {
        foreach ($p->author->posts as $i => $post) {
            echo $post->author->email_address;
        }
    }
}`,
			want: []phputil.FQN{user},
		},
		{
			name: "parameter of inner method does not leak",
			src: `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function a(User $u) {}
    public function b() { return $u->email_address; }
}`,
			want: []phputil.FQN{""},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := typesOfFetches(t, tc.src, "email_address")
			if len(got) != len(tc.want) {
				t.Fatalf("got %d fetches %v, want %v", len(got), got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("fetch %d: type = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestCompletion_InsideClosureOnIncompleteCode(t *testing.T) {
	// "|" marks the cursor; code after it is what an editor buffer holds.
	tests := []struct{ name, src string }{
		{"closure param", `<?php
use App\Models\User;
Route::get('/', function (User $u) {
    return $u->|
});
Route::get('/b', fn () => 1);`},
		{"arrow fn param", `<?php
use App\Models\User;
$f = fn (User $u) => $u->|;
$g = 2;`},
		{"method param, partial name", `<?php
namespace App\Http;
use App\Models\User;
class C {
    public function m(User $u) {
        $x = $u->em|
        return 1;
    }
}`},
		{"top-level assignment, buffer ends at cursor", `<?php
use App\Models\User;
$u = User::find(1);
echo $u->em|`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			offset := strings.Index(tc.src, "|")
			src := strings.Replace(tc.src, "|", "", 1)
			items := eloquentCompletions([]byte(src), "/routes/web.php", offset, testModelIndex(t))
			found := false
			for _, it := range items {
				if it.Label == "email_address" {
					found = true
				}
			}
			if !found {
				t.Errorf("email_address not offered; got %d items", len(items))
			}
		})
	}
}

// fakeMembers stands in for classinfo.Index (traits/parents in other files).
type fakeMembers map[phputil.FQN]map[string]phputil.FQN

func (f fakeMembers) PropertyType(class phputil.FQN, prop string) phputil.FQN {
	return f[class][prop]
}

func TestResolveExprType_TypedProperties(t *testing.T) {
	src := `<?php
namespace App\Http;
use App\Models\User;
use App\Traits\Userable;
class Repo extends Base {
    use Userable;
    private ?User $owner = null;
    public function __construct(private User $author) {}
    public function m() {
        echo $this->owner->email_address;   // same-file typed property
        echo $this->author->email_address;  // same-file promoted param
        echo $this->user->email_address;    // trait in another file
        echo $this->admin->email_address;   // parent in another file
        echo $this->nothing->email_address; // unknown
    }
}`
	b := []byte(src)
	tree, err := phpnode.ParseBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	c := &fetchCollector{fc: &phputil.FileContext{Uses: make(phputil.UseMap)}}
	phpwalk.Walk("", b, tree, c)

	models := testModelIndex(t).WithClassMembers(fakeMembers{
		"App\\Traits\\Userable": {"user": user},
		"App\\Http\\Base":       {"admin": user},
	})

	var got []phputil.FQN
	for _, f := range c.fetches {
		if f.PropName == "email_address" {
			got = append(got, resolveExprType(f.VarRaw, b, c.fc, models))
		}
	}
	want := []phputil.FQN{user, user, user, user, ""}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("fetch %d: %q, want %q", i, got[i], want[i])
		}
	}
}

// Review findings: rebinding forms the resolver cannot type must make the
// variable unknown rather than keep a stale earlier type (which would cause
// false "unknown property" diagnostics), and conflicting candidate types
// must not pick one arbitrarily.
func TestResolveExprType_RebindingIsConservative(t *testing.T) {
	tests := []struct {
		name string
		body string // inside `function m(Post $p) { $u = new User(); ... }`
		want phputil.FQN
	}{
		{"array destructuring", `[$u, $v] = $rows; echo $u->email_address;`, ""},
		{"list destructuring", `list($u) = $rows; echo $u->email_address;`, ""},
		{"foreach destructuring", `foreach ($rows as [$u, $v]) { echo $u->email_address; }`, ""},
		{"compound assignment", `$u .= 'x'; echo $u->email_address;`, ""},
		{"catch variable", `try {} catch (\Exception $u) { echo $u->email_address; }`, ""},
		{"global", `global $u; echo $u->email_address;`, ""},
		{"static var", `static $u; echo $u->email_address;`, ""},
		{"conflicting branch reassignment", `if ($x) { $u = new \App\Models\Post(); } echo $u->email_address;`, ""},
		{"agreeing reassignment keeps type", `if ($x) { $u = User::find(2); } echo $u->email_address;`, user},
		{"plain use keeps type", `echo $u->email_address;`, user},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := `<?php
namespace App\Http;
use App\Models\Post;
use App\Models\User;
class C {
    public function m(Post $p) {
        $u = new User();
        ` + tc.body + `
    }
}`
			got := typesOfFetches(t, src, "email_address")
			if got[len(got)-1] != tc.want {
				t.Errorf("type = %q, want %q", got[len(got)-1], tc.want)
			}
		})
	}
}

func TestResolveExprType_TypedParamReassigned(t *testing.T) {
	got := typesOfFetches(t, `<?php
namespace App\Http;
use App\Models\Post;
use App\Models\User;
class C {
    public function m(Post $m) {
        $m = new User();
        echo $m->email_address;
    }
}`, "email_address")
	if got[0] != user {
		t.Errorf("type = %q, want %q (assignment overrides the param type)", got[0], user)
	}
}

func TestResolveExprType_ThisInAnonymousClass(t *testing.T) {
	got := typesOfFetches(t, `<?php
namespace App\Models;
class User extends \Illuminate\Database\Eloquent\Model {
    public function m() {
        return new class {
            public function f() { return $this->email_address; }
        };
    }
}`, "email_address")
	if got[0] != "" {
		t.Errorf("$this in anonymous class = %q, want unknown", got[0])
	}
}
