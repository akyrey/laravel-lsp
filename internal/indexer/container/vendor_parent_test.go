package container_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

func TestWalk_ProviderWithVendorAncestor(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		// EventServiceProvider lives in vendor; only the resolver knows it
		// extends Illuminate\Support\ServiceProvider.
		"app/Providers/EventServiceProvider.php": `<?php
namespace App\Providers;
use Illuminate\Foundation\Support\Providers\EventServiceProvider as ServiceProvider;
use App\Contracts\Clock;
use App\Services\SystemClock;
class EventServiceProvider extends ServiceProvider {
    public function register(): void {
        $this->app->bind(Clock::class, SystemClock::class);
    }
}`,
		"app/Services/SystemClock.php": `<?php
namespace App\Services;
class SystemClock {}`,
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
	parents := func(fqn phputil.FQN) phputil.FQN {
		if fqn == "Illuminate\\Foundation\\Support\\Providers\\EventServiceProvider" {
			return "Illuminate\\Support\\ServiceProvider"
		}
		return ""
	}

	idx, err := container.Walk(root, []string{"app"}, container.WithExternalParents(parents))
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	got := idx.Lookup("App\\Contracts\\Clock")
	if len(got) != 1 || got[0].Concrete != "App\\Services\\SystemClock" {
		t.Fatalf("Lookup(Clock) = %+v, want one binding to SystemClock", got)
	}

	// ReindexFile must keep using the resolver.
	newIdx, err := container.ReindexFile(filepath.Join(root, "app/Providers/EventServiceProvider.php"), idx)
	if err != nil {
		t.Fatalf("ReindexFile: %v", err)
	}
	if len(newIdx.Lookup("App\\Contracts\\Clock")) != 1 {
		t.Error("binding lost after ReindexFile: external parents not retained")
	}
}
