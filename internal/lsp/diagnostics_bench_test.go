package lsp

import (
	"fmt"
	"strings"
	"testing"
)

// largeControllerSrc builds a ~2,300-line class with 6,000 property fetches,
// mixing relationship chains and $this->prop lookups.
func largeControllerSrc() []byte {
	var b strings.Builder
	b.WriteString("<?php\nnamespace App\\Http;\nuse App\\Models\\User;\nclass Big {\n")
	for m := 0; m < 100; m++ {
		fmt.Fprintf(&b, "  public function m%d(User $u) {\n    $p = $u->posts->author;\n", m)
		for i := 0; i < 20; i++ {
			fmt.Fprintf(&b, "    $x%d = $p->email_address . $this->foo->bar . $u->posts->author->email_address;\n", i)
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// Diagnostics run on every keystroke (DidChange), so they must stay fast on
// large files: per-file resolver caches keep this linear.
func BenchmarkCollectDiagnostics_LargeFile(b *testing.B) {
	src := largeControllerSrc()
	models := testModelIndexB(b)
	opts := defaultDiagOptions()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collectDiagnostics(src, "/x.php", models, nil, opts)
	}
}
