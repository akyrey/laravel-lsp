package container

import "github.com/akyrey/laravel-lsp/internal/phputil"

// Option configures Walk.
type Option func(*symbolTable)

// WithExternalParents lets the hierarchy walk continue past the scanned
// directories: when a class's parent is not declared in them, fn is asked
// for that parent's own parent. Pass a composer-backed resolver so classes
// extending vendor bases are recognised. The resolver is retained by the
// index and reused by ReindexFile.
func WithExternalParents(fn phputil.ParentFunc) Option {
	return func(st *symbolTable) { st.external = fn }
}
