// Package composer locates PHP classes outside the indexed scan directories
// (typically under vendor/) using Composer's PSR-4 autoload metadata.
//
// The indexers only walk the project's own source directories, so a model
// like `class User extends Illuminate\Foundation\Auth\User` would otherwise
// look parentless: its ancestor chain leaves the project before it reaches
// Illuminate\Database\Eloquent\Model. Locating and parsing just the needed
// vendor files lets the hierarchy walk continue without indexing all of vendor.
package composer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/akyrey/laravel-lsp/internal/phputil"
)

// psr4Entry maps one namespace prefix ("Illuminate\") to the absolute
// directories that hold its classes.
type psr4Entry struct {
	prefix string
	dirs   []string
}

// Autoload resolves class FQNs to file paths using PSR-4 mappings collected
// from the root composer.json and vendor/composer/installed.json.
// The zero value (and a nil pointer) locates nothing.
type Autoload struct {
	entries []psr4Entry // longest prefix first
}

// autoloadSection is the "autoload" object shared by composer.json and each
// installed.json package. Each PSR-4 value is either a string or a list.
type autoloadSection struct {
	PSR4 map[string]json.RawMessage `json:"psr-4"`
}

type packageManifest struct {
	Name        string          `json:"name"`
	InstallPath string          `json:"install-path"`
	Autoload    autoloadSection `json:"autoload"`
	AutoloadDev autoloadSection `json:"autoload-dev"`
}

// Load reads the PSR-4 mappings for the project at root. Missing composer
// files are not an error — the result simply locates fewer classes. A file
// that exists but cannot be decoded is reported, yet the mappings from the
// other file are still returned: a half-written installed.json must not cost
// the project its own App\ mapping. The returned Autoload is never nil.
func Load(root string) (*Autoload, error) {
	a := &Autoload{}
	var errs []error

	rootManifest, err := readManifest(filepath.Join(root, "composer.json"))
	if err != nil {
		errs = append(errs, err)
	}
	if rootManifest != nil {
		a.add(root, rootManifest.Autoload)
		a.add(root, rootManifest.AutoloadDev)
	}

	vendorComposer := filepath.Join(root, "vendor", "composer")
	packages, err := readInstalled(filepath.Join(vendorComposer, "installed.json"))
	if err != nil {
		errs = append(errs, err)
	}
	for _, pkg := range packages {
		a.add(packageDir(vendorComposer, pkg), pkg.Autoload)
	}

	sort.SliceStable(a.entries, func(i, j int) bool {
		return len(a.entries[i].prefix) > len(a.entries[j].prefix)
	})
	return a, errors.Join(errs...)
}

// Size returns the number of PSR-4 prefixes known, for diagnostics logging.
func (a *Autoload) Size() int {
	if a == nil {
		return 0
	}
	return len(a.entries)
}

// packageDir returns the absolute install directory of a vendor package.
// Composer 2 records it relative to vendor/composer; Composer 1 did not
// record it, and packages lived at vendor/<name>.
func packageDir(vendorComposer string, pkg packageManifest) string {
	if pkg.InstallPath != "" {
		return filepath.Join(vendorComposer, filepath.FromSlash(pkg.InstallPath))
	}
	return filepath.Join(filepath.Dir(vendorComposer), filepath.FromSlash(pkg.Name))
}

// readManifest decodes a composer.json, returning nil when it does not exist.
func readManifest(path string) (*packageManifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("composer: read %s: %w", path, err)
	}
	var m packageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("composer: decode %s: %w", path, err)
	}
	return &m, nil
}

// readInstalled decodes vendor/composer/installed.json in either the
// Composer 2 ({"packages": [...]}) or Composer 1 ([...]) layout. A missing
// file yields no packages.
func readInstalled(path string) ([]packageManifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("composer: read %s: %w", path, err)
	}
	var v2 struct {
		Packages []packageManifest `json:"packages"`
	}
	errV2 := json.Unmarshal(data, &v2)
	if errV2 == nil {
		return v2.Packages, nil
	}
	var v1 []packageManifest
	if err := json.Unmarshal(data, &v1); err != nil {
		// Report the Composer 2 error: that is the layout every current
		// Composer writes, so it is the one that explains the problem.
		return nil, fmt.Errorf("composer: decode %s: %w", path, errV2)
	}
	return v1, nil
}

// add registers the PSR-4 mappings of one autoload section, resolving its
// relative directories against base.
func (a *Autoload) add(base string, section autoloadSection) {
	for prefix, raw := range section.PSR4 {
		var dirs []string
		for _, rel := range decodeDirs(raw) {
			dirs = append(dirs, filepath.Join(base, filepath.FromSlash(rel)))
		}
		if len(dirs) > 0 {
			a.entries = append(a.entries, psr4Entry{prefix: prefix, dirs: dirs})
		}
	}
}

// decodeDirs accepts a PSR-4 value written as a single string or a list.
func decodeDirs(raw json.RawMessage) []string {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{one}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	return nil
}

// Locate returns the absolute path of the file declaring fqn, or "" when no
// PSR-4 mapping produces an existing file. Longer prefixes are tried first,
// and every directory of a matching prefix is checked in order.
func (a *Autoload) Locate(fqn phputil.FQN) string {
	if a == nil {
		return ""
	}
	name := strings.TrimPrefix(string(fqn), "\\")
	for _, e := range a.entries {
		if !strings.HasPrefix(name, e.prefix) {
			continue
		}
		rel := filepath.FromSlash(strings.ReplaceAll(name[len(e.prefix):], "\\", "/")) + ".php"
		for _, dir := range e.dirs {
			candidate := filepath.Join(dir, rel)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}
