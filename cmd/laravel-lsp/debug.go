package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/akyrey/laravel-lsp/internal/indexer/container"
	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phputil"
)

const debugUsage = `Usage: laravel-lsp debug [flags] [project-root]

Indexes the project at project-root (default: current directory) and prints
every model attribute and container binding to stdout.  Useful for verifying
what the LSP server would see without starting a full editor session.

Flags:
  -dirs <dirs>  comma-separated scan dirs relative to root (default: app)
  -json         emit JSON instead of human-readable text
  -models       print models only
  -bindings     print bindings only

Examples:
  laravel-lsp debug /path/to/my-app
  laravel-lsp debug -models /path/to/my-app
  laravel-lsp debug -json /path/to/my-app
  laravel-lsp debug -dirs app,src /path/to/my-app

Exit codes:
  0  success
  1  indexing error
`

func runDebug(args []string) {
	fs := flag.NewFlagSet("debug", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, debugUsage) }

	jsonOut := fs.Bool("json", false, "emit JSON")
	onlyModels := fs.Bool("models", false, "print models only")
	onlyBindings := fs.Bool("bindings", false, "print bindings only")
	dirsFlag := fs.String("dirs", "", "comma-separated scan dirs relative to root (default: app)")

	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	root := "."
	if fs.NArg() > 0 {
		root = fs.Arg(0)
	}

	showModels := !*onlyBindings
	showBindings := !*onlyModels

	// Resolve scan directories.
	var dirs []string
	if *dirsFlag != "" {
		for _, d := range splitComma(*dirsFlag) {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	if len(dirs) == 0 {
		dirs = []string{"app"}
	}

	// Walk both indexes.
	modelIdx, err := eloquent.Walk(root, dirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "laravel-lsp debug: eloquent index: %v\n", err)
		os.Exit(1)
	}

	bindingIdx, err := container.Walk(root, dirs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "laravel-lsp debug: container index: %v\n", err)
		os.Exit(1)
	}

	if len(modelIdx.All()) == 0 && len(bindingIdx.All()) == 0 {
		fmt.Fprintf(os.Stderr, "laravel-lsp debug: no entries found (scanned %v under %s)\n", dirs, root)
		fmt.Fprintf(os.Stderr, "  use -dirs flag to specify a different scan path, e.g. -dirs .\n")
	}

	if *jsonOut {
		printJSON(modelIdx, bindingIdx, showModels, showBindings)
	} else {
		printText(modelIdx, bindingIdx, showModels, showBindings)
	}
}

// ── text output ──────────────────────────────────────────────────────────────

func printText(modelIdx *eloquent.ModelIndex, bindingIdx *container.BindingIndex, showModels, showBindings bool) {
	if showModels {
		cats := modelIdx.All()
		sort.Slice(cats, func(i, j int) bool { return string(cats[i].Class) < string(cats[j].Class) })

		fmt.Printf("=== Models (%d) ===\n", len(cats))
		for _, cat := range cats {
			fmt.Printf("\n%s\n  %s\n", cat.Class, cat.Path)

			// Sort attribute names for stable output.
			names := make([]string, 0, len(cat.ByExposed))
			for k := range cat.ByExposed {
				names = append(names, k)
			}
			sort.Strings(names)

			for _, name := range names {
				attrs := cat.ByExposed[name]
				for _, a := range attrs {
					fmt.Printf("  %-30s %s\n", name, attrKindLabel(a))
				}
			}
		}
	}

	if showBindings {
		all := bindingIdx.All()
		sort.Slice(all, func(i, j int) bool {
			if all[i].Abstract != all[j].Abstract {
				return string(all[i].Abstract) < string(all[j].Abstract)
			}
			return string(all[i].Concrete) < string(all[j].Concrete)
		})

		fmt.Printf("\n=== Container bindings (%d) ===\n", len(all))
		for _, b := range all {
			concrete := string(b.Concrete)
			if concrete == "" {
				concrete = "(opaque closure)"
			}
			fmt.Printf("\n%s\n  → %s  [%s, %s]\n", b.Abstract, concrete, b.Lifetime, bindingKindLabel(b.Kind))
			if !b.Source.Zero() {
				fmt.Printf("  source: %s:%d\n", b.Source.Path, b.Source.StartLine)
			}
			if !b.Location.Zero() {
				fmt.Printf("  target: %s:%d\n", b.Location.Path, b.Location.StartLine)
			}
		}
	}
}

func attrKindLabel(a eloquent.ModelAttribute) string {
	switch a.Kind {
	case eloquent.ModernAccessor:
		return fmt.Sprintf("accessor  via %s()", a.MethodName)
	case eloquent.LegacyAccessor:
		return "accessor  (legacy getter)"
	case eloquent.LegacyMutator:
		return "mutator   (legacy setter)"
	case eloquent.Relationship:
		rel := string(a.RelatedFQN)
		if rel == "" {
			rel = "?"
		}
		return fmt.Sprintf("relation  → %s", rel)
	case eloquent.FillableArray:
		return "$fillable"
	case eloquent.CastArray:
		return "$casts"
	case eloquent.AppendsArray:
		return "$appends"
	case eloquent.HiddenArray:
		return "$hidden"
	case eloquent.IdeHelperProperty:
		return "ide-helper @property"
	case eloquent.IdeHelperMethod:
		return "ide-helper @method"
	}
	return "unknown"
}

func bindingKindLabel(k container.BindingKind) string {
	switch k {
	case container.BindCall:
		return "call"
	case container.BindClosure:
		return "closure"
	case container.BindAttribute:
		return "attribute"
	}
	return "unknown"
}

// ── JSON output ──────────────────────────────────────────────────────────────

type jsonOutput struct {
	Models   []jsonModel   `json:"models,omitempty"`
	Bindings []jsonBinding `json:"bindings,omitempty"`
}

type jsonModel struct {
	Class      string          `json:"class"`
	Path       string          `json:"path"`
	Attributes []jsonAttribute `json:"attributes"`
}

type jsonAttribute struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	MethodName string `json:"method_name,omitempty"`
	RelatedFQN string `json:"related_fqn,omitempty"`
	Source     string `json:"source"`
}

type jsonBinding struct {
	Abstract phputil.FQN `json:"abstract"`
	Concrete phputil.FQN `json:"concrete,omitempty"`
	Kind     string      `json:"kind"`
	Lifetime string      `json:"lifetime"`
	Source   string      `json:"source,omitempty"`
	Target   string      `json:"target,omitempty"`
}

func printJSON(modelIdx *eloquent.ModelIndex, bindingIdx *container.BindingIndex, showModels, showBindings bool) {
	out := jsonOutput{}

	if showModels {
		cats := modelIdx.All()
		sort.Slice(cats, func(i, j int) bool { return string(cats[i].Class) < string(cats[j].Class) })
		for _, cat := range cats {
			m := jsonModel{Class: string(cat.Class), Path: cat.Path}
			names := make([]string, 0, len(cat.ByExposed))
			for k := range cat.ByExposed {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, name := range names {
				for _, a := range cat.ByExposed[name] {
					src := "ast"
					if a.Source == eloquent.SourceIdeHelper {
						src = "ide-helper"
					}
					m.Attributes = append(m.Attributes, jsonAttribute{
						Name:       name,
						Kind:       attrKindLabel(a),
						MethodName: a.MethodName,
						RelatedFQN: string(a.RelatedFQN),
						Source:     src,
					})
				}
			}
			out.Models = append(out.Models, m)
		}
	}

	if showBindings {
		all := bindingIdx.All()
		sort.Slice(all, func(i, j int) bool { return string(all[i].Abstract) < string(all[j].Abstract) })
		for _, b := range all {
			jb := jsonBinding{
				Abstract: b.Abstract,
				Concrete: b.Concrete,
				Kind:     bindingKindLabel(b.Kind),
				Lifetime: b.Lifetime,
			}
			if !b.Source.Zero() {
				jb.Source = fmt.Sprintf("%s:%d", b.Source.Path, b.Source.StartLine)
			}
			if !b.Location.Zero() {
				jb.Target = fmt.Sprintf("%s:%d", b.Location.Path, b.Location.StartLine)
			}
			out.Bindings = append(out.Bindings, jb)
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(out) //nolint:errcheck
}

func splitComma(s string) []string {
	return strings.Split(s, ",")
}
