package classinfo_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/classinfo"
	"github.com/akyrey/laravel-lsp/internal/composer"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

func project(t *testing.T) *classinfo.Index {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"composer.json": `{"autoload": {"psr-4": {"App\\": "app/"}}}`,
		"app/Traits/Userable.php": `<?php
namespace App\Traits;
use App\Models\User;
trait Userable {
    // PHP 8.4 property hook.
    protected User $user {
        get => Auth::user();
    }
}`,
		"app/Traits/Loops.php": `<?php
namespace App\Traits;
trait Loops { use Loops; }`,
		"app/Http/BaseController.php": `<?php
namespace App\Http;
use App\Services\Clock;
abstract class BaseController {
    protected Clock $clock;
}`,
		"app/Http/AccountRepository.php": `<?php
namespace App\Http;
use App\Traits\Userable;
use App\Traits\Loops;
use App\Models\Account;
final class AccountRepository extends BaseController {
    use Userable, Loops;
    private ?Account $cached = null;
    public $untyped;
    protected int $count = 0;
    public function __construct(private readonly \App\Services\Mailer $mailer, $plain) {}
}`,
		"app/Http/Cyclic.php": `<?php
namespace App\Http;
class Cyclic extends Cyclic {}`,
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	auto, err := composer.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return classinfo.New(auto)
}

func TestPropertyType(t *testing.T) {
	idx := project(t)
	const repo phputil.FQN = "App\\Http\\AccountRepository"

	tests := []struct {
		class phputil.FQN
		prop  string
		want  phputil.FQN
	}{
		{repo, "cached", "App\\Models\\Account"},  // nullable typed property
		{repo, "mailer", "App\\Services\\Mailer"}, // promoted constructor param
		{repo, "user", "App\\Models\\User"},       // from a trait, hooked property
		{repo, "clock", "App\\Services\\Clock"},   // from the parent class
		{repo, "untyped", ""},                     // no type
		{repo, "count", ""},                       // primitive type
		{repo, "plain", ""},                       // ordinary ctor param, not a property
		{repo, "missing", ""},                     // not declared anywhere
		{"App\\Http\\Cyclic", "anything", ""},     // extends itself: must terminate
		{"App\\Nope\\Unknown", "user", ""},        // class cannot be located
	}
	for _, tc := range tests {
		t.Run(string(tc.class)+"::"+tc.prop, func(t *testing.T) {
			if got := idx.PropertyType(tc.class, tc.prop); got != tc.want {
				t.Errorf("PropertyType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPropertyType_NilIndex(t *testing.T) {
	var idx *classinfo.Index
	if got := idx.PropertyType("A", "b"); got != "" {
		t.Errorf("nil index PropertyType = %q", got)
	}
}
