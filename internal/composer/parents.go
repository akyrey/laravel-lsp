package composer

import (
	"sync"

	"github.com/akyrey/laravel-lsp/internal/phpnode"
	"github.com/akyrey/laravel-lsp/internal/phputil"
	"github.com/akyrey/laravel-lsp/internal/phpwalk"
)

// Parents answers "what does class X extend?" for classes outside the
// indexed directories by locating and parsing their declaring file on
// demand. Results (including misses) are memoized, and the type is safe for
// concurrent use — the container and Eloquent indexers share one instance
// while walking in parallel. A nil *Parents resolves nothing.
type Parents struct {
	auto *Autoload

	mu   sync.Mutex
	memo map[phputil.FQN]phputil.FQN
}

// NewParents returns a resolver backed by auto.
func NewParents(auto *Autoload) *Parents {
	return &Parents{auto: auto, memo: make(map[phputil.FQN]phputil.FQN)}
}

// ParentOf returns the FQN of fqn's direct parent class, or "" when fqn has
// no parent or its declaration cannot be located.
func (p *Parents) ParentOf(fqn phputil.FQN) phputil.FQN {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	parent, seen := p.memo[fqn]
	p.mu.Unlock()
	if seen {
		return parent
	}

	parent, ok := parseParent(p.auto.Locate(fqn), fqn)
	if !ok {
		return "" // unreadable right now (e.g. mid-write): retry next time
	}
	p.mu.Lock()
	p.memo[fqn] = parent
	p.mu.Unlock()
	return parent
}

// parseParent parses path and returns the resolved extends clause of the
// class named fqn declared in it. ok is false only when a located file could
// not be read or parsed — a transient condition worth retrying. A class that
// cannot be located at all is a stable answer (until `composer update`
// triggers a full reindex, which builds a fresh Parents).
func parseParent(path string, fqn phputil.FQN) (parent phputil.FQN, ok bool) {
	if path == "" {
		return "", true
	}
	src, tree, err := phpnode.ParseFile(path)
	if err != nil {
		return "", false
	}
	defer tree.Close()

	v := &parentVisitor{
		target: phputil.FQN(trimLeadingBackslash(string(fqn))),
		fc:     &phputil.FileContext{Path: path, Uses: make(phputil.UseMap)},
	}
	phpwalk.Walk(path, src, tree, v)
	return v.parent, true
}

func trimLeadingBackslash(s string) string {
	if len(s) > 0 && s[0] == '\\' {
		return s[1:]
	}
	return s
}

type parentVisitor struct {
	phpwalk.NullVisitor
	target phputil.FQN
	fc     *phputil.FileContext
	parent phputil.FQN
}

func (v *parentVisitor) VisitNamespace(ns string) { v.fc.Namespace = phputil.FQN(ns) }
func (v *parentVisitor) VisitUseItem(alias, fqn string) {
	v.fc.Uses[alias] = phputil.FQN(fqn)
}

func (v *parentVisitor) VisitClass(n phpwalk.ClassInfo) {
	if n.ExtendsText == "" || v.fc.Resolve(n.NameText) != v.target {
		return
	}
	v.parent = v.fc.Resolve(n.ExtendsText)
}
