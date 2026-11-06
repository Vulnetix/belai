package agent

import (
	"context"
	"crypto/sha256"
	"path/filepath"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/symindex"
	"github.com/vulnetix/belai/internal/tools"
)

// The Outline and Symbol tools serve a symbol index of the working directory
// (internal/symindex, docs/symbol-map.md). The index is built lazily, on the
// first call of either tool, so a session that never asks pays nothing, and it
// is shared by every session in the process that works in the same directory:
// the explore subagents of one turn would otherwise each walk the tree.
//
// The index holds facts about files the inventory judged eligible. What this
// file adds is the session's own filters, applied to every answer: a file
// whose Read the classifier withheld is not listed (its rows are replaced by a
// count that points at no other tool), and a file a Read deny rule covers is
// not listed at all.

// symbolRetry is how soon a failed build is tried again; a build that ran out
// of time must not make every call wait for the full timeout.
const symbolRetry = 5 * time.Second

// symbolMissRefresh is the least time between two refreshes that an Outline of
// a file the index does not hold may force, so a file the model just wrote is
// found without letting a stream of typos walk the tree on every call.
const symbolMissRefresh = 2 * time.Second

// symbolOver is how many more candidate rows than the answer needs a lookup
// asks the index for, so rows the session's filters drop do not leave the
// answer short.
const symbolOver = 4

// symbolHolder is one working directory's shared index.
type symbolHolder struct {
	mu       sync.Mutex
	ix       *symindex.Index
	at       time.Time
	forcedAt time.Time
	failedAt time.Time
	saved    [sha256.Size]byte
}

var (
	symbolHoldersMu sync.Mutex
	symbolHolders   = map[string]*symbolHolder{}
)

// symbolHolderFor returns the process-wide holder for a resolved root.
func symbolHolderFor(root string) *symbolHolder {
	symbolHoldersMu.Lock()
	defer symbolHoldersMu.Unlock()
	h := symbolHolders[root]
	if h == nil {
		h = &symbolHolder{}
		symbolHolders[root] = h
	}
	return h
}

// get returns the index, built or refreshed when it is older than refresh (or
// when force is set and it is older than symbolMissRefresh). A nil index means
// the index is unavailable for this call. The on-disk cache seeds the first
// build, so only changed files are parsed again; it is written back after a
// build that changed something, and a failure to write it is ignored.
func (h *symbolHolder) get(ctx context.Context, workdir, root string, lim symindex.Limits, refresh time.Duration, force bool) *symindex.Index {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ix != nil && time.Since(h.at) < refresh && !(force && time.Since(h.forcedAt) >= symbolMissRefresh) {
		return h.ix
	}
	if force {
		h.forcedAt = time.Now()
	}
	if !h.failedAt.IsZero() && time.Since(h.failedAt) < symbolRetry {
		return nil
	}
	cache, cacheErr := config.ProjectSymbolIndexPath(workdir)
	if h.ix == nil && cacheErr == nil {
		if loaded, err := symindex.Load(cache, root, lim.MaxFiles); err == nil {
			h.ix = loaded
		}
	}
	ix, err := symindex.Build(ctx, root, h.ix, lim)
	if err != nil {
		h.failedAt = time.Now()
		return nil
	}
	h.ix, h.at, h.failedAt = ix, time.Now(), time.Time{}
	if cacheErr == nil {
		if data, err := ix.Encode(); err == nil {
			if sum := sha256.Sum256(data); sum != h.saved {
				if ix.Save(cache) == nil {
					h.saved = sum
				}
			}
		}
	}
	return ix
}

// symbolAdapter is the tools.SymbolSource a session installs in its registry's
// hub. Everything it needs from the session is a function, so the user's
// /symbols view can build one with no session at all.
type symbolAdapter struct {
	workdir  string
	settings func() config.Settings
	perms    func() permissions.Settings
	roots    func() []string
	// flagged reports the verdict label of an earlier withheld Read of the
	// absolute path, or false. Nil means no file is flagged.
	flagged func(abs string) (string, bool)
}

// NewSymbolSource returns the symbol index source for workdir under the given
// settings and permission rules, with no withheld-file state. The /symbols
// command uses it before a session exists; it applies the same Read deny
// filter a session does, so the user is never shown more than a model would be.
func NewSymbolSource(workdir string, set config.Settings, perms permissions.Settings) tools.SymbolSource {
	return symbolAdapter{
		workdir:  workdir,
		settings: func() config.Settings { return set },
		perms:    func() permissions.Settings { return perms },
		roots:    func() []string { return []string{workdir} },
	}
}

// SymbolSource returns the session's own source, which also drops files whose
// Read the classifier withheld, or nil when the index is off for it.
func (s *Session) SymbolSource() tools.SymbolSource {
	if !s.settings.SymbolIndexEnabled() {
		return nil
	}
	return s.symbolAdapter()
}

func (s *Session) symbolAdapter() symbolAdapter {
	return symbolAdapter{
		workdir:  s.workdir,
		settings: func() config.Settings { return s.settings },
		perms:    func() permissions.Settings { return s.perms },
		roots: func() []string {
			if cwd := s.registry.Cwd(); cwd != nil {
				return cwd.Roots()
			}
			return []string{s.workdir}
		},
		flagged: func(abs string) (string, bool) {
			v, hit := s.flagged.lookup(abs, "", "")
			if !hit {
				return "", false
			}
			return v.Label(), true
		},
	}
}

// hasSymbolTools reports whether the Outline and Symbol tools are on the
// session's registry, for the one line of the work discipline that names them.
func (s *Session) hasSymbolTools() bool {
	if s.registry == nil {
		return false
	}
	_, o := s.registry.Find(tools.OutlineName)
	_, y := s.registry.Find(tools.SymbolName)
	return o && y
}

// installSymbols points the registry's Outline and Symbol tools at this
// session. A registry built without them is left alone.
func (s *Session) installSymbols() {
	hub := s.registry.SymbolHub()
	if hub == nil || !s.settings.SymbolIndexEnabled() {
		return
	}
	hub.Set(s.symbolAdapter())
}

func (a symbolAdapter) limits() (symindex.Limits, time.Duration) {
	set := a.settings()
	return symindex.Limits{MaxFiles: set.SymbolIndexMaxFiles()},
		time.Duration(set.SymbolIndexRefreshSecs()) * time.Second
}

// index returns the shared index for the session's working directory, or nil.
func (a symbolAdapter) index(ctx context.Context, force bool) *symindex.Index {
	root, err := filepath.EvalSymlinks(a.workdir)
	if err != nil {
		return nil
	}
	lim, refresh := a.limits()
	return symbolHolderFor(root).get(ctx, a.workdir, root, lim, refresh, force)
}

// denied reports whether a Read deny rule covers the indexed file rel.
func (a symbolAdapter) denied(root, rel string) bool {
	ad := &knowledgeAdapter{perms: a.perms, roots: a.roots}
	return ad.deny(filepath.Join(root, filepath.FromSlash(rel)))
}

// flagged reports the verdict of an earlier withheld Read of the indexed file.
func (a symbolAdapter) flaggedFile(root, rel string) (string, bool) {
	if a.flagged == nil {
		return "", false
	}
	return a.flagged(filepath.Join(root, filepath.FromSlash(rel)))
}

func rowOf(path string, s symindex.Symbol) tools.SymbolRow {
	return tools.SymbolRow{Path: path, Name: s.Name, Kind: s.Kind, Container: s.Container, Line: s.Line, EndLine: s.EndLine}
}

// Outline implements tools.SymbolSource.
func (a symbolAdapter) Outline(ctx context.Context, rel string) tools.SymbolAnswer {
	ix := a.index(ctx, false)
	if ix == nil {
		return tools.SymbolAnswer{Unavailable: true}
	}
	if !ix.Has(rel) {
		if ix = a.index(ctx, true); ix == nil {
			return tools.SymbolAnswer{Unavailable: true}
		}
	}
	if !ix.Has(rel) || a.denied(ix.Root(), rel) {
		return tools.SymbolAnswer{NotIndexed: true}
	}
	ix.Touch(rel)
	syms, ok := ix.Outline(rel)
	if !ok {
		return tools.SymbolAnswer{NotIndexed: true}
	}
	if verdict, hit := a.flaggedFile(ix.Root(), rel); hit {
		return tools.SymbolAnswer{Withheld: []tools.SymbolWithheld{{Path: rel, Rows: len(syms), Verdict: verdict}}}
	}
	ans := coverage(ix, tools.SymbolAnswer{})
	for _, s := range syms {
		ans.Rows = append(ans.Rows, rowOf(rel, s))
	}
	return ans
}

// Lookup implements tools.SymbolSource.
func (a symbolAdapter) Lookup(ctx context.Context, name, kind string, limit int) tools.SymbolAnswer {
	ix := a.index(ctx, false)
	if ix == nil {
		return tools.SymbolAnswer{Unavailable: true}
	}
	fetch := min(limit*symbolOver, 400)
	hits := ix.Lookup(name, kind, fetch)
	// A row's line comes from when its file was last parsed: re-read the files
	// named by the rows, and look again if one had changed.
	changed := false
	seen := map[string]bool{}
	for _, h := range hits {
		if !seen[h.Path] {
			seen[h.Path] = true
			changed = ix.Touch(h.Path) || changed
		}
	}
	if changed {
		hits = ix.Lookup(name, kind, fetch)
	}
	ans := coverage(ix, tools.SymbolAnswer{})
	held := map[string]*tools.SymbolWithheld{}
	for _, h := range hits {
		if a.denied(ix.Root(), h.Path) {
			continue
		}
		if verdict, hit := a.flaggedFile(ix.Root(), h.Path); hit {
			w := held[h.Path]
			if w == nil {
				w = &tools.SymbolWithheld{Path: h.Path, Verdict: verdict}
				held[h.Path] = w
			}
			w.Rows++
			continue
		}
		if len(ans.Rows) < limit {
			ans.Rows = append(ans.Rows, rowOf(h.Path, h.Symbol))
		}
	}
	for _, w := range held {
		ans.Withheld = append(ans.Withheld, *w)
	}
	return ans
}

// coverage fills in how much of the working directory the index holds.
func coverage(ix *symindex.Index, ans tools.SymbolAnswer) tools.SymbolAnswer {
	st := ix.Stats()
	ans.Files, ans.Eligible, ans.Truncated = st.Files, st.Eligible, st.Truncated
	return ans
}

// SymbolIndexStats reports the size of the shared index for workdir, building
// it first if it is not there. The second result is false when the index is
// unavailable.
func SymbolIndexStats(ctx context.Context, workdir string, set config.Settings) (symindex.Stats, bool) {
	ix := NewSymbolSource(workdir, set, permissions.Settings{}).(symbolAdapter).index(ctx, false)
	if ix == nil {
		return symindex.Stats{}, false
	}
	return ix.Stats(), true
}

// RefreshSymbolIndex re-reads workdir's index now, ignoring the refresh
// interval and an earlier failure, and reports the new size.
func RefreshSymbolIndex(ctx context.Context, workdir string, set config.Settings) (symindex.Stats, bool) {
	root, err := filepath.EvalSymlinks(workdir)
	if err != nil {
		return symindex.Stats{}, false
	}
	h := symbolHolderFor(root)
	h.mu.Lock()
	h.failedAt = time.Time{}
	h.at = time.Time{}
	h.mu.Unlock()
	return SymbolIndexStats(ctx, workdir, set)
}
