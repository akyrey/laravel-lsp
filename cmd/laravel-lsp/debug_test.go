package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
)

// captureStdout runs fn and returns what it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestPrintText_ListsScopesIncludingInherited(t *testing.T) {
	// Arrange
	root := t.TempDir()
	files := map[string]string{
		"app/Traits/HasOrdering.php": `<?php
namespace App\Traits;
trait HasOrdering {
    #[\Illuminate\Database\Eloquent\Attributes\Scope]
    protected function ordered($q) {}
}`,
		"app/Models/Post.php": `<?php
namespace App\Models;
use App\Traits\HasOrdering;
class Post extends \Illuminate\Database\Eloquent\Model {
    use HasOrdering;
    public function scopePublished($q) {}
}`,
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
	models, err := eloquent.Walk(root, []string{"app"})
	if err != nil {
		t.Fatal(err)
	}

	// Act
	out := captureStdout(t, func() {
		printText(models, container.NewBindingIndex(), true, false)
	})

	// Assert
	for _, want := range []string{"published", "ordered"} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, want) && strings.Contains(line, "scope") {
				found = true
			}
		}
		if !found {
			t.Errorf("output missing scope %q:\n%s", want, out)
		}
	}
}
