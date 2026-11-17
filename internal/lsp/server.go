package lsp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/tliron/commonlog"
	"github.com/tliron/glsp"
	protocol "github.com/tliron/glsp/protocol_3_16"

	"github.com/akyrey/laravel-lsp/internal/composer"
	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/indexer/idehelper"
	"github.com/akyrey/laravel-lsp/internal/indexer/migrations"
	"github.com/akyrey/laravel-lsp/internal/indexer/strindex"
)

// Config holds settings passed via initializationOptions in the LSP initialize
// request. All directory lists are relative to the project root and support
// single-level glob patterns such as "Modules/*/app".
type Config struct {
	// ScanDirs are the directories walked when building the container and
	// Eloquent indexes. Defaults to ["app"].
	ScanDirs []string `json:"scanDirs"`

	// ReferenceDirs are the directories walked when searching for references
	// and rename sites. Defaults to ["app", "routes"].
	ReferenceDirs []string `json:"referenceDirs"`

	// MigrationDirs are the directories whose migrations supply model columns.
	// Defaults to ["database/migrations"].
	MigrationDirs []string `json:"migrationDirs"`

	// Diagnostics tunes the unknown-property diagnostics.
	Diagnostics DiagnosticsConfig `json:"diagnostics"`
}

// DiagnosticsConfig holds the diagnostics section of initializationOptions.
type DiagnosticsConfig struct {
	// Enabled toggles unknown-property diagnostics. Defaults to true.
	Enabled *bool `json:"enabled"`
	// Severity is "error", "warning", "information", or "hint".
	// Defaults to "warning"; unknown values fall back to it.
	Severity string `json:"severity"`
	// IgnoreProperties lists property names that are never flagged, on any
	// model — a project-level escape hatch for magic attributes the indexer
	// cannot see.
	IgnoreProperties []string `json:"ignoreProperties"`
}

// diagOptions converts the user-facing config into its runtime form.
func (c Config) diagOptions() diagOptions {
	opts := defaultDiagOptions()
	if c.Diagnostics.Enabled != nil {
		opts.enabled = *c.Diagnostics.Enabled
	}
	switch c.Diagnostics.Severity {
	case "error":
		opts.severity = protocol.DiagnosticSeverityError
	case "information":
		opts.severity = protocol.DiagnosticSeverityInformation
	case "hint":
		opts.severity = protocol.DiagnosticSeverityHint
	}
	if len(c.Diagnostics.IgnoreProperties) > 0 {
		opts.ignore = make(map[string]bool, len(c.Diagnostics.IgnoreProperties))
		for _, name := range c.Diagnostics.IgnoreProperties {
			opts.ignore[name] = true
		}
	}
	return opts
}

func defaultConfig() Config {
	return Config{
		ScanDirs:      []string{"app"},
		ReferenceDirs: []string{"app", "routes"},
		MigrationDirs: migrations.DefaultDirs,
	}
}

// parseConfig decodes initializationOptions into a Config and fills in any
// fields that were not explicitly set:
//   - scanDirs defaults to ["app"]
//   - referenceDirs defaults to scanDirs + ["routes"], so that adding module
//     directories to scanDirs automatically covers them for reference search too.
//
// Setting referenceDirs explicitly in initializationOptions overrides the
// auto-derived value.
func parseConfig(opts any) (Config, error) {
	// Unmarshal into a zero struct so we can detect absent fields.
	var raw struct {
		ScanDirs      []string          `json:"scanDirs"`
		ReferenceDirs []string          `json:"referenceDirs"`
		MigrationDirs []string          `json:"migrationDirs"`
		Diagnostics   DiagnosticsConfig `json:"diagnostics"`
	}
	data, err := json.Marshal(opts)
	if err != nil {
		return defaultConfig(), err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return defaultConfig(), err
	}

	cfg := Config{
		ScanDirs:      raw.ScanDirs,
		ReferenceDirs: raw.ReferenceDirs,
		MigrationDirs: raw.MigrationDirs,
		Diagnostics:   raw.Diagnostics,
	}
	if len(cfg.ScanDirs) == 0 {
		cfg.ScanDirs = []string{"app"}
	}
	if len(cfg.ReferenceDirs) == 0 {
		// Auto-derive: same dirs as scanning + routes.
		cfg.ReferenceDirs = append(append([]string{}, cfg.ScanDirs...), "routes")
	}
	if len(cfg.MigrationDirs) == 0 {
		cfg.MigrationDirs = migrations.DefaultDirs
	}
	return cfg, nil
}

// expandDirs expands glob patterns in patterns relative to root and returns
// a deduplicated list of directories that exist on disk.
// Non-glob patterns are included as-is (Walk handles missing dirs silently).
func expandDirs(root string, patterns []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, p := range patterns {
		if !strings.ContainsAny(p, "*?[") {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, p))
		if err != nil {
			continue
		}
		for _, m := range matches {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				continue
			}
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	return out
}

// reindexDebounce is how long to wait after the last PHP file change before
// triggering a reindex. Chosen to avoid thrashing during multi-file saves.
const reindexDebounce = 500 * time.Millisecond

// Server holds all LSP state: open document cache, indexes, and the project
// root. Handler methods are registered in main.go.
type Server struct {
	version string
	root    string
	cfg     Config
	docs    *DocumentStore
	log     commonlog.Logger

	mu       sync.RWMutex
	scanOnce sync.Once
	bindings *container.BindingIndex
	models   *eloquent.ModelIndex
	strIndex *strindex.Index
	schema   *migrations.Schema
}

// NewServer creates a Server ready to accept LSP requests.
func NewServer(log commonlog.Logger, version string) *Server {
	return &Server{
		version: version,
		cfg:     defaultConfig(),
		docs:    newDocumentStore(),
		log:     log,
	}
}

// Initialize detects the project root, reads initializationOptions, and
// returns server capabilities.
func (s *Server) Initialize(_ *glsp.Context, p *protocol.InitializeParams) (any, error) {
	root := detectRoot(p)

	cfg := defaultConfig()
	if p.InitializationOptions != nil {
		var err error
		cfg, err = parseConfig(p.InitializationOptions)
		if err != nil {
			s.log.Errorf("laravel-lsp: invalid initializationOptions: %v", err)
			cfg = defaultConfig()
		}
	}

	s.mu.Lock()
	s.root = root
	s.cfg = cfg
	s.mu.Unlock()
	s.log.Infof("laravel-lsp: root=%s scanDirs=%v referenceDirs=%v",
		root, cfg.ScanDirs, cfg.ReferenceDirs)

	syncKind := protocol.TextDocumentSyncKindFull
	ver := s.version
	// ">" completes model attributes after ->; the quotes complete string
	// references inside config('...') / view('...') / route('...') calls.
	triggerChars := []string{">", "'", "\""}
	return protocol.InitializeResult{
		Capabilities: protocol.ServerCapabilities{
			TextDocumentSync:   syncKind,
			DefinitionProvider: true,
			ReferencesProvider: true,
			HoverProvider:      true,
			CodeActionProvider: &protocol.CodeActionOptions{
				CodeActionKinds: []protocol.CodeActionKind{
					protocol.CodeActionKindQuickFix,
				},
			},
			RenameProvider: &protocol.RenameOptions{
				PrepareProvider: boolPtr(true),
			},
			CompletionProvider: &protocol.CompletionOptions{
				TriggerCharacters: triggerChars,
			},
			DocumentSymbolProvider:  true,
			WorkspaceSymbolProvider: true,
		},
		ServerInfo: &protocol.InitializeResultServerInfo{
			Name:    "laravel-lsp",
			Version: &ver,
		},
	}, nil
}

// Initialized kicks off background indexing then starts the file-watcher.
// The editor can send requests immediately; handlers return nil until indexing
// finishes.
func (s *Server) Initialized(_ *glsp.Context, _ *protocol.InitializedParams) error {
	s.scanOnce.Do(func() {
		go func() {
			s.mu.RLock()
			root := s.root
			s.mu.RUnlock()
			if root == "" {
				return
			}
			s.reindex(root)
			s.startWatcher(root)
		}()
	})
	return nil
}

// DidOpen caches the newly opened document and pushes diagnostics.
func (s *Server) DidOpen(ctx *glsp.Context, p *protocol.DidOpenTextDocumentParams) error {
	src := []byte(p.TextDocument.Text)
	s.docs.Set(p.TextDocument.URI, src)
	s.mu.RLock()
	models, strs, opts := s.models, s.strIndex, s.cfg.diagOptions()
	s.mu.RUnlock()
	path := URIToPath(p.TextDocument.URI)
	publishDiagnostics(ctx, p.TextDocument.URI, src, path, models, strs, opts)
	return nil
}

// DidChange updates the cached document and pushes diagnostics.
func (s *Server) DidChange(ctx *glsp.Context, p *protocol.DidChangeTextDocumentParams) error {
	src := contentFromChanges(p.ContentChanges)
	if src == nil {
		return nil
	}
	s.docs.Set(p.TextDocument.URI, src)
	s.mu.RLock()
	models, strs, opts := s.models, s.strIndex, s.cfg.diagOptions()
	s.mu.RUnlock()
	path := URIToPath(p.TextDocument.URI)
	publishDiagnostics(ctx, p.TextDocument.URI, src, path, models, strs, opts)
	return nil
}

// contentFromChanges extracts the full new document content from a change
// set. The server negotiates Full sync, so every change should be a whole-
// document replacement; when several are present the last one wins. An event
// carrying a Range is an incremental change this server cannot apply — it is
// skipped rather than cached as if it were the whole document.
func contentFromChanges(changes []any) []byte {
	var src []byte
	for _, ch := range changes {
		switch c := ch.(type) {
		case protocol.TextDocumentContentChangeEventWhole:
			src = []byte(c.Text)
		case protocol.TextDocumentContentChangeEvent:
			if c.Range == nil {
				src = []byte(c.Text)
			}
		}
	}
	return src
}

// DidClose removes the document from the cache and clears its diagnostics.
func (s *Server) DidClose(ctx *glsp.Context, p *protocol.DidCloseTextDocumentParams) error {
	s.docs.Delete(p.TextDocument.URI)
	// Clear diagnostics so they don't linger after the file is closed.
	publishDiagnostics(ctx, p.TextDocument.URI, nil, "", nil, nil, defaultDiagOptions())
	return nil
}

// Shutdown is a no-op; cleanup happens at process exit.
func (s *Server) Shutdown(_ *glsp.Context) error { return nil }

// SetTrace is a no-op.
func (s *Server) SetTrace(_ *glsp.Context, _ *protocol.SetTraceParams) error { return nil }

// effectiveReferenceDirs returns the expanded reference scan directories for
// the given root, ready to pass to scanReferences / scanRenameRefs.
func (s *Server) effectiveReferenceDirs(root string) []string {
	s.mu.RLock()
	dirs := s.cfg.ReferenceDirs
	s.mu.RUnlock()
	return expandDirs(root, dirs)
}

// reindex rebuilds both indexes in parallel and atomically swaps them in.
func (s *Server) reindex(root string) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	scanDirs := expandDirs(root, cfg.ScanDirs)
	s.log.Infof("laravel-lsp: indexing %s (dirs: %v)", root, scanDirs)
	auto := s.loadAutoload(root)
	parents := composer.NewParents(auto).ParentOf

	var wg sync.WaitGroup
	var bindings *container.BindingIndex
	var models *eloquent.ModelIndex
	var strIdx *strindex.Index
	var schema *migrations.Schema
	var err1, err2 error

	wg.Add(4)
	go func() {
		defer wg.Done()
		bindings, err1 = container.Walk(root, scanDirs, container.WithExternalParents(parents))
	}()
	go func() {
		defer wg.Done()
		models, err2 = eloquent.Walk(root, scanDirs, eloquent.WithExternalParents(parents))
	}()
	go func() {
		defer wg.Done()
		strIdx = strindex.Walk(root)
	}()
	go func() {
		defer wg.Done()
		schema = migrations.Walk(root, expandDirs(root, cfg.MigrationDirs))
	}()
	wg.Wait()

	if err1 != nil {
		s.log.Errorf("laravel-lsp: container index: %v", err1)
	}
	if err2 != nil {
		s.log.Errorf("laravel-lsp: eloquent index: %v", err2)
	}

	// Augment the Eloquent index with ide-helper entries (no-op if file absent).
	if err2 == nil {
		ideHelperPath := filepath.Join(root, "_ide_helper_models.php")
		if err := idehelper.Merge(ideHelperPath, models); err != nil {
			s.log.Errorf("laravel-lsp: idehelper merge: %v", err)
		}
		// Last: these share the catalog maps, so no Add may follow.
		models = models.WithSchema(schema)
	}

	s.mu.Lock()
	s.schema = schema
	if err1 == nil {
		s.bindings = bindings
	}
	if err2 == nil {
		s.models = models
	}
	s.strIndex = strIdx
	s.mu.Unlock()
	// One summary line makes "navigation returns nothing" diagnosable from
	// the editor's LSP log: zero models usually means wrong scanDirs or a
	// missing `composer install`, zero tables wrong migrationDirs.
	s.log.Infof("laravel-lsp: indexing complete: %d models, %d bindings, %d migration tables, %d composer PSR-4 prefixes",
		countModels(models), countBindings(bindings), schema.TableCount(), auto.Size())
	if models != nil {
		for _, c := range models.All() {
			s.log.Debugf("laravel-lsp: indexed model %s (%s)", c.Class, c.Path)
		}
	}
}

// loadAutoload reads the project's Composer autoload metadata, used to
// follow extends chains into vendor/ and to look up property types of
// classes outside the open buffer. It is reloaded on every full reindex so
// `composer update` is picked up. On a decode error it logs and continues
// with whatever could be read; an empty map makes chains stop at the
// project boundary.
func (s *Server) loadAutoload(root string) *composer.Autoload {
	auto, err := composer.Load(root)
	if err != nil {
		s.log.Errorf("laravel-lsp: composer autoload (continuing with what could be read): %v", err)
	}
	if auto.Size() == 0 {
		s.log.Warningf("laravel-lsp: no Composer PSR-4 mappings under %s: models extending vendor classes "+
			"(e.g. User extends Authenticatable) will not be recognised until `composer install` runs", root)
	}
	return auto
}

// startWatcher watches the union of scan and reference directories under root
// for PHP file changes and triggers a debounced reindex on any write/create/remove.
func (s *Server) startWatcher(root string) {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()

	// Watch the union of scan dirs and reference dirs so we catch changes
	// everywhere the server reads from, plus the string-reference sources
	// (config keys and blade views; routes are already in referenceDirs).
	allPatterns := append(append([]string{}, cfg.ScanDirs...), cfg.ReferenceDirs...)
	allPatterns = append(allPatterns, "config", filepath.Join("resources", "views"))
	allPatterns = append(allPatterns, cfg.MigrationDirs...)
	watchSet := expandDirs(root, allPatterns)

	w, err := fsnotify.NewWatcher()
	if err != nil {
		s.log.Errorf("laravel-lsp: watcher init: %v", err)
		return
	}

	failed := 0
	add := func(dir string) {
		if err := w.Add(dir); err != nil {
			failed++
			if failed == 1 { // usually an OS watch limit: report once, not per dir
				s.log.Warningf("laravel-lsp: cannot watch %s (changes there will need a restart): %v", dir, err)
			}
		}
	}
	for _, dir := range watchSet {
		scanDir := filepath.Join(root, dir)
		if _, err := os.Stat(scanDir); err != nil {
			continue
		}
		if err := filepath.WalkDir(scanDir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				add(path)
			}
			return nil
		}); err != nil {
			s.log.Warningf("laravel-lsp: watcher walk %s: %v", scanDir, err)
		}
	}
	// Non-recursive: the project root for .env / .env.example / composer.json,
	// and vendor/composer for installed.json (rewritten by composer install).
	add(root)
	if vendorComposer := filepath.Join(root, "vendor", "composer"); dirExists(vendorComposer) {
		add(vendorComposer)
	}
	if failed > 0 {
		s.log.Warningf("laravel-lsp: %d directories are not watched", failed)
	}

	go s.watchLoop(w, root)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// watchLoop is the event loop for the fsnotify watcher. It accumulates changed
// PHP file paths during the debounce window, then calls reindexFiles with just
// those paths instead of a full walk.
func (s *Server) watchLoop(w *fsnotify.Watcher, root string) {
	defer w.Close()

	timer := time.NewTimer(reindexDebounce)
	timer.Stop()
	changedPaths := make(map[string]bool)

	for {
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Create) {
				if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
					_ = w.Add(event.Name)
				}
			}
			if isWatchedFile(event.Name) {
				changedPaths[event.Name] = true
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(reindexDebounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			s.log.Errorf("laravel-lsp: watcher: %v", err)
		case <-timer.C:
			paths := make([]string, 0, len(changedPaths))
			for p := range changedPaths {
				paths = append(paths, p)
			}
			changedPaths = make(map[string]bool)
			s.reindexFiles(root, paths)
		}
	}
}

// reindexFiles performs per-file incremental reindex for each changed path.
// Falls back to a full reindex when the index has no retained symbol table
// (e.g., first startup before any index exists).
func (s *Server) reindexFiles(root string, paths []string) {
	s.mu.RLock()
	bindings, models, schema, cfg := s.bindings, s.models, s.schema, s.cfg
	s.mu.RUnlock()

	if bindings == nil || models == nil || bindings.Syms() == nil || models.Syms() == nil ||
		needsFullReindex(root, paths) {
		s.reindex(root)
		return
	}

	newBindings := bindings
	newModels := models

	for _, path := range paths {
		if !strings.HasSuffix(path, ".php") {
			continue // .env changes only affect the string index below
		}
		if nb, e := container.ReindexFile(path, newBindings); e != nil {
			s.log.Errorf("laravel-lsp: container reindex %s: %v", path, e)
		} else {
			newBindings = nb
		}

		if nm, e := eloquent.ReindexFile(path, newModels); e != nil {
			s.log.Errorf("laravel-lsp: eloquent reindex %s: %v", path, e)
		} else {
			newModels = nm
		}
	}

	// Rebuild the string-reference index when any changed file lives in one
	// of its source dirs. The full walk is cheap: a handful of config and
	// route files plus a directory listing of the views.
	if strChanged(root, paths) {
		newStr := strindex.Walk(root)
		s.mu.Lock()
		s.strIndex = newStr
		s.mu.Unlock()
	}

	// Re-apply ide-helper entries for the freshly extracted catalogs before
	// publishing. Merge is idempotent, so carried-over catalogs (which still
	// hold their entries from the previous merge) are left untouched.
	if newModels != models {
		ideHelperPath := filepath.Join(root, "_ide_helper_models.php")
		if err := idehelper.Merge(ideHelperPath, newModels); err != nil {
			s.log.Errorf("laravel-lsp: idehelper merge: %v", err)
		}
	}

	// A changed migration rebuilds the schema (a cheap walk) and re-attaches
	// it. This must stay after the ide-helper merge: WithSchema shares maps.
	migrationDirs := expandDirs(root, cfg.MigrationDirs)
	if underAny(root, migrationDirs, paths) {
		schema = migrations.Walk(root, migrationDirs)
		newModels = newModels.WithSchema(schema)
		s.mu.Lock()
		s.schema = schema
		s.mu.Unlock()
	}
	if newBindings != bindings || newModels != models {
		s.mu.Lock()
		s.bindings = newBindings
		s.models = newModels
		s.mu.Unlock()
	}
	s.log.Infof("laravel-lsp: incremental reindex complete (%d files)", len(paths))
}

func countModels(idx *eloquent.ModelIndex) int {
	if idx == nil {
		return 0
	}
	return len(idx.All())
}

func countBindings(idx *container.BindingIndex) int {
	if idx == nil {
		return 0
	}
	return len(idx.All())
}

// needsFullReindex reports whether a changed file affects every catalog
// rather than one file's: the ide-helper stub (entries may have been
// removed, which per-file merging cannot undo) or Composer metadata
// (autoload and vendor ancestry are loaded once per full reindex).
func needsFullReindex(root string, paths []string) bool {
	full := map[string]bool{
		filepath.Join(root, "_ide_helper_models.php"):               true,
		filepath.Join(root, "composer.json"):                        true,
		filepath.Join(root, "vendor", "composer", "installed.json"): true,
	}
	for _, p := range paths {
		if full[p] {
			return true
		}
	}
	return false
}

// underAny reports whether any of paths lives under one of dirs (relative
// to root).
func underAny(root string, dirs, paths []string) bool {
	for _, dir := range dirs {
		prefix := filepath.Join(root, dir) + string(filepath.Separator)
		for _, p := range paths {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
	}
	return false
}

// strChanged reports whether any of paths lives under one of the
// string-reference source directories.
func strChanged(root string, paths []string) bool {
	prefixes := []string{
		filepath.Join(root, "config") + string(filepath.Separator),
		filepath.Join(root, "resources", "views") + string(filepath.Separator),
		filepath.Join(root, "routes") + string(filepath.Separator),
	}
	for _, p := range paths {
		if isEnvFile(p) {
			return true
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(p, prefix) {
				return true
			}
		}
	}
	return false
}

// isWatchedFile reports whether a change to path can affect the indexes.
func isWatchedFile(path string) bool {
	return strings.HasSuffix(path, ".php") || isEnvFile(path) ||
		filepath.Base(path) == "composer.json" ||
		strings.HasSuffix(path, filepath.Join("vendor", "composer", "installed.json"))
}

// isEnvFile reports whether path is a dotenv file the string index reads.
func isEnvFile(path string) bool {
	base := filepath.Base(path)
	return base == ".env" || base == ".env.example"
}

// detectRoot extracts the project root from InitializeParams.
// Priority: WorkspaceFolders[0] > RootURI > RootPath > cwd.
func detectRoot(p *protocol.InitializeParams) string {
	if len(p.WorkspaceFolders) > 0 {
		return URIToPath(p.WorkspaceFolders[0].URI)
	}
	if p.RootURI != nil && *p.RootURI != "" {
		return URIToPath(*p.RootURI)
	}
	if p.RootPath != nil && *p.RootPath != "" {
		return *p.RootPath
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
