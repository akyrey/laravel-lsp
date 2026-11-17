package idehelper

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/akyrey/laravel-lsp/internal/indexer/eloquent"
	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

var (
	// @property[-read|-write] <type> $<name>
	// Group 1 = type, Group 2 = name.
	propertyTagRe = regexp.MustCompile(`@property(?:-read|-write)?\s+(\S+)\s+\$(\w+)`)
	// @method [static] <returnType> <name>(...)
	methodTagRe = regexp.MustCompile(`@method\b[^\(]+\b(\w+)\s*\(`)
)

// ideProperty holds the name and optional type string for a @property tag.
type ideProperty struct {
	name    string
	typeStr string
}

// Merge parses _ide_helper_models.php (if present at path) and adds
// SourceIdeHelper entries into idx for any attribute name not already present
// from SourceAST.
//
// Conflict-resolution rule: SourceAST wins. IdeHelper entries for a name that
// already has at least one SourceAST entry are silently dropped.
//
// Jump-target policy (option b from the plan): IdeHelper-only entries have a
// zero Location so the LSP layer returns nothing for them.
func Merge(path string, idx *eloquent.ModelIndex) error {
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("idehelper: read %s: %w", path, err)
	}

	tree, parseErr := phpnode.ParseBytes(src)
	if parseErr != nil {
		return nil
	}
	defer tree.Close()

	mv := &mergeVisitor{
		src: src,
		idx: idx,
		fc:  &phputil.FileContext{Path: path, Uses: make(phputil.UseMap)},
	}
	phpwalk.Walk(path, src, tree, mv)
	return nil
}

// mergeVisitor walks the ide-helper AST and applies doc-comment entries.
type mergeVisitor struct {
	phpwalk.NullVisitor
	src []byte
	idx *eloquent.ModelIndex
	fc  *phputil.FileContext
}

func (v *mergeVisitor) VisitNamespace(ns string) { v.fc.Namespace = phputil.FQN(ns) }
func (v *mergeVisitor) VisitUseItem(alias, fqn string) {
	v.fc.Uses[alias] = phputil.FQN(fqn)
}

func (v *mergeVisitor) VisitClass(n phpwalk.ClassInfo) {
	fqn := mixinTarget(v.fc.Resolve(n.NameText))
	if fqn == "" {
		return
	}

	doc := extractDocComment(v.src, int(n.Raw.StartByte()))
	if doc == "" {
		return
	}
	props, methods := parseDocComment(doc)

	// LookupDeclared, not Lookup: the entries must land on the real catalog,
	// not on an inheritance-merged view.
	cat := v.idx.LookupDeclared(fqn)
	if cat == nil {
		cat = &eloquent.ModelCatalog{
			Class:     fqn,
			ByExposed: make(map[string][]eloquent.ModelAttribute),
		}
	}
	additions := v.newEntries(cat, props, methods)
	if len(additions) == 0 {
		return
	}
	// Copy-on-write: cat may already be published to concurrent readers
	// (incremental reindex carries catalogs over), so never append into it.
	updated := cat.Clone()
	for _, a := range additions {
		updated.ByExposed[a.ExposedName] = append(updated.ByExposed[a.ExposedName], a)
	}
	v.idx.Add(updated)
}

// newEntries returns the ide-helper attributes cat does not already have.
// AST entries win: names declared in source are skipped.
func (v *mergeVisitor) newEntries(cat *eloquent.ModelCatalog, props []ideProperty, methods []string) []eloquent.ModelAttribute {
	var out []eloquent.ModelAttribute
	for _, pe := range props {
		if hasSourceAST(cat, pe.name) || hasIdeHelperEntry(cat, pe.name, eloquent.IdeHelperProperty) {
			continue
		}
		out = append(out, eloquent.ModelAttribute{
			ExposedName: pe.name,
			Kind:        eloquent.IdeHelperProperty,
			Source:      eloquent.SourceIdeHelper,
			// Zero Location: policy is "return nothing for ide-helper-only names".
			// A class-typed property lets the chain resolver follow
			// $this->rel->attr even without an AST relationship.
			RelatedFQN: resolveClassType(pe.typeStr, v.fc),
		})
	}
	for _, name := range methods {
		if hasSourceAST(cat, name) || hasIdeHelperEntry(cat, name, eloquent.IdeHelperMethod) {
			continue
		}
		out = append(out, eloquent.ModelAttribute{
			ExposedName: name,
			MethodName:  name,
			Kind:        eloquent.IdeHelperMethod,
			Source:      eloquent.SourceIdeHelper,
		})
	}
	return out
}

// resolveClassType returns the FQN for a PHP type string if it looks like a
// class reference (not a primitive, not a union/generic). Returns "" otherwise.
func resolveClassType(typeStr string, fc *phputil.FileContext) phputil.FQN {
	// Strip nullable prefix.
	typeStr = strings.TrimPrefix(typeStr, "?")
	// Skip unions, generics, and array shapes — too complex to resolve safely.
	if strings.ContainsAny(typeStr, "|&<>[") {
		return ""
	}
	if phputil.IsPrimitiveType(typeStr) {
		return ""
	}
	return fc.Resolve(typeStr)
}

// hasSourceAST reports whether cat already has a SourceAST entry for name.
func hasSourceAST(cat *eloquent.ModelCatalog, name string) bool {
	for _, a := range cat.ByExposed[name] {
		if a.Source == eloquent.SourceAST {
			return true
		}
	}
	return false
}

// hasIdeHelperEntry reports whether cat already has a SourceIdeHelper entry of
// the given kind for name. This makes Merge idempotent, so the server can
// re-apply it after an incremental reindex without duplicating entries on
// catalogs that were carried over unchanged.
func hasIdeHelperEntry(cat *eloquent.ModelCatalog, name string, kind eloquent.AttributeKind) bool {
	for _, a := range cat.ByExposed[name] {
		if a.Source == eloquent.SourceIdeHelper && a.Kind == kind {
			return true
		}
	}
	return false
}

// mixinStubPrefix is the class-name prefix ide-helper uses in
// --write-mixin mode: the stub for App\Models\User is App\Models\IdeHelperUser,
// linked from the model by `@mixin IdeHelperUser`.
const mixinStubPrefix = "IdeHelper"

// mixinTarget maps a --write-mixin stub class to the model it describes.
// Classes without the prefix (the default ide-helper layout, which reuses the
// model's own name) are returned unchanged.
func mixinTarget(fqn phputil.FQN) phputil.FQN {
	s := string(fqn)
	nsEnd := strings.LastIndex(s, "\\") + 1
	short := s[nsEnd:]
	if len(short) <= len(mixinStubPrefix) || !strings.HasPrefix(short, mixinStubPrefix) {
		return fqn
	}
	return phputil.FQN(s[:nsEnd] + short[len(mixinStubPrefix):])
}

// extractDocComment returns the `/** ... */` block immediately preceding the
// byte at position before in src, or "" if none is found.
func extractDocComment(src []byte, before int) string {
	if before <= 0 || before > len(src) {
		return ""
	}
	chunk := src[:before]

	end := bytes.LastIndex(chunk, []byte("*/"))
	if end < 0 {
		return ""
	}
	end += 2 // advance past */

	// Only whitespace may separate the comment from the class keyword.
	for _, b := range chunk[end:] {
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' {
			return ""
		}
	}

	start := bytes.LastIndex(chunk[:end-2], []byte("/**"))
	if start < 0 {
		return ""
	}

	return string(chunk[start:end])
}

// parseDocComment extracts @property entries (with types) and @method names.
func parseDocComment(doc string) (properties []ideProperty, methods []string) {
	for _, m := range propertyTagRe.FindAllStringSubmatch(doc, -1) {
		// m[1] = type string, m[2] = property name
		properties = append(properties, ideProperty{typeStr: m[1], name: m[2]})
	}
	for _, m := range methodTagRe.FindAllStringSubmatch(doc, -1) {
		methods = append(methods, m[1])
	}
	return
}
