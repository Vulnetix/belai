// Package symindex is the harness's symbol-level declaration index
// (docs/symbol-map.md). It parses the declarations of the files internal/locate
// already judged eligible (never an ignored, hidden, symlinked, binary,
// oversized, credential-bearing or dependency file) and answers two questions:
// what does this file declare, in order, and where is a name declared.
//
// An entry is a name, a kind from a closed set, a container, a line and an end
// line. It is never a body, a comment or a signature. The names are still the
// repository's own text, so they are served only by tools whose result is
// classified, and never reach the repo map, the system block or a directive.
//
// The index is bounded in time, files, declarations per file and declarations
// in all. A refresh re-parses only files whose size or modification time
// changed. The persisted cache is untrusted on load: it is checksummed, and
// every row is re-bounded and re-validated, or the whole file is discarded.
package symindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/locate"
)

// Default bounds. The file bound is the setting symbol_index.max_files; the
// others are fixed.
const (
	DefaultMaxFiles = 2000
	// MaxPerFile bounds the declarations kept for one file; it is
	// locate's own per-file bound.
	MaxPerFile = 200
	// MaxTotal bounds the declarations in one index.
	MaxTotal = 20000
	// BuildTimeout bounds one build or refresh, as the repo map's scan is.
	BuildTimeout = 5 * time.Second
	// MaxNameLen bounds a stored name.
	MaxNameLen = 120
	// maxPathLen bounds a stored path.
	maxPathLen = 512
)

// ErrUnavailable says an index could not be built within its bounds: the walk
// or a parse ran out of time, or the root could not be read. No partial rows
// are served after it.
var ErrUnavailable = errors.New("symbol index unavailable")

// Symbol is one declaration.
type Symbol struct {
	Name      string `json:"n"`
	Kind      string `json:"k,omitempty"`
	Container string `json:"c,omitempty"`
	Line      int    `json:"l"`
	EndLine   int    `json:"e,omitempty"`
}

// Entry is one indexed file and the stamp it was parsed at.
type Entry struct {
	Path    string   `json:"p"`
	Size    int64    `json:"s"`
	MTime   int64    `json:"m"`
	Symbols []Symbol `json:"y,omitempty"`
}

// Hit is a Symbol with the file it was found in.
type Hit struct {
	Path string
	Symbol
}

// Limits bound one build. The zero value uses the defaults.
type Limits struct {
	MaxFiles int
	Timeout  time.Duration
}

func (l Limits) files() int {
	if l.MaxFiles <= 0 {
		return DefaultMaxFiles
	}
	return l.MaxFiles
}

func (l Limits) timeout() time.Duration {
	if l.Timeout <= 0 {
		return BuildTimeout
	}
	return l.Timeout
}

// Index is a built symbol index. It is safe for concurrent use.
type Index struct {
	mu      sync.RWMutex
	root    string
	entries map[string]*Entry
	// eligible is how many indexable files the walk found, which is more than
	// the index holds when the file bound stopped it.
	eligible  int
	truncated bool
	symbols   int
	maxFiles  int
}

// notIndexed are extensions of data and prose files. A declaration scan over
// them finds lines that merely resemble code, so they are left out.
var notIndexed = map[string]bool{
	".md": true, ".mdx": true, ".markdown": true, ".txt": true, ".rst": true,
	".json": true, ".jsonl": true, ".yaml": true, ".yml": true, ".toml": true, ".xml": true,
	".html": true, ".htm": true, ".css": true, ".svg": true, ".csv": true, ".tsv": true,
	".lock": true, ".sum": true, ".log": true, ".ini": true, ".cfg": true, ".conf": true,
	".map": true, ".min.js": true,
}

// Indexable reports whether a repository-relative path is a file whose
// declarations are worth indexing.
func Indexable(rel string) bool {
	low := strings.ToLower(rel)
	if strings.HasSuffix(low, ".min.js") {
		return false
	}
	return !notIndexed[path.Ext(low)]
}

// Build walks root and returns its index. prev, when it covers the same root,
// supplies the entries of files whose size and modification time are
// unchanged, so only changed files are parsed again. It returns
// ErrUnavailable, wrapped, when the walk or a parse runs past the timeout or
// the root cannot be read; no partial index is returned then.
func Build(ctx context.Context, root string, prev *Index, lim Limits) (*Index, error) {
	ctx, cancel := context.WithTimeout(ctx, lim.timeout())
	defer cancel()
	inv, err := locate.Build(ctx, root)
	if inv == nil || err != nil {
		if err == nil {
			err = errors.New("no inventory")
		}
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	ix := &Index{root: inv.Root, entries: map[string]*Entry{}, maxFiles: lim.files()}
	var old map[string]*Entry
	if prev != nil {
		prev.mu.RLock()
		if prev.root == inv.Root {
			old = make(map[string]*Entry, len(prev.entries))
			for k, v := range prev.entries {
				old[k] = v
			}
		}
		prev.mu.RUnlock()
	}
	for _, f := range inv.Files {
		if !Indexable(f.Path) {
			continue
		}
		ix.eligible++
		if len(ix.entries) >= ix.maxFiles {
			ix.truncated = true
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		info, err := os.Lstat(filepath.Join(inv.Root, filepath.FromSlash(f.Path)))
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		e := &Entry{Path: f.Path, Size: info.Size(), MTime: info.ModTime().UnixNano()}
		if o := old[f.Path]; o != nil && o.Size == e.Size && o.MTime == e.MTime {
			e.Symbols = o.Symbols
		} else {
			e.Symbols = parse(filepath.Join(inv.Root, filepath.FromSlash(f.Path)))
		}
		ix.add(e)
	}
	return ix, nil
}

// add stores e, keeping the declaration total inside MaxTotal.
func (ix *Index) add(e *Entry) {
	room := MaxTotal - ix.symbols
	if len(e.Symbols) > room {
		e.Symbols = e.Symbols[:max(room, 0)]
		ix.truncated = true
	}
	ix.symbols += len(e.Symbols)
	ix.entries[e.Path] = e
}

// parse reads a file's declarations into symbols, dropping any whose name or
// kind is outside what the index serves.
func parse(abs string) []Symbol {
	ds, _ := locate.Declarations(abs)
	out := make([]Symbol, 0, min(len(ds), MaxPerFile))
	for _, d := range ds {
		if len(out) >= MaxPerFile {
			break
		}
		if s, ok := clean(Symbol{Name: d.Name, Kind: d.Kind, Container: d.Container, Line: d.Line, EndLine: d.EndLine}); ok {
			out = append(out, s)
		}
	}
	return out
}

// clean validates one symbol. A name is a program identifier (letters, digits
// and underscore, at most MaxNameLen bytes), a kind is one of locate.Kinds or
// empty, and a line is positive and not past its end line. A symbol that fails
// is dropped, never repaired.
func clean(s Symbol) (Symbol, bool) {
	if !identifier(s.Name) || (s.Container != "" && !identifier(s.Container)) {
		return Symbol{}, false
	}
	if s.Kind != "" && !locate.ValidKind(s.Kind) {
		return Symbol{}, false
	}
	if s.Line < 1 || s.Line > 1<<28 || s.EndLine < 0 || s.EndLine > 1<<28 || (s.EndLine != 0 && s.EndLine < s.Line) {
		return Symbol{}, false
	}
	return s, true
}

// identifier reports whether s is 1 to MaxNameLen bytes of ASCII letters,
// digits and underscore, not starting with a digit. Unicode identifiers are
// not served: the sanitiser sink for a name is an ASCII identifier filter.
func identifier(s string) bool {
	if s == "" || len(s) > MaxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// Root is the resolved directory the index was built over.
func (ix *Index) Root() string { return ix.root }

// Stats are the index's size, for a status line.
type Stats struct {
	// Files is how many files the index holds; Eligible is how many indexable
	// files the walk found.
	Files, Eligible, Symbols int
	// Truncated says the file or declaration bound stopped the index short.
	Truncated bool
}

// Stats reports the index's size.
func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return Stats{Files: len(ix.entries), Eligible: ix.eligible, Symbols: ix.symbols, Truncated: ix.truncated}
}

// Has reports whether rel is an indexed file.
func (ix *Index) Has(rel string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	_, ok := ix.entries[rel]
	return ok
}

// Outline returns the declarations of one indexed file in source order, and
// false when the file is not in the index.
func (ix *Index) Outline(rel string) ([]Symbol, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	e, ok := ix.entries[rel]
	if !ok {
		return nil, false
	}
	out := make([]Symbol, len(e.Symbols))
	copy(out, e.Symbols)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out, true
}

// Touch re-reads one indexed file when its size or modification time changed,
// and forgets it when it is gone. It reports whether the entry changed. A file
// that is not in the index is left alone: whether a new file is eligible is the
// walk's decision, made by Build.
func (ix *Index) Touch(rel string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	e, ok := ix.entries[rel]
	if !ok {
		return false
	}
	abs := filepath.Join(ix.root, filepath.FromSlash(rel))
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() {
		ix.symbols -= len(e.Symbols)
		delete(ix.entries, rel)
		return true
	}
	if info.Size() == e.Size && info.ModTime().UnixNano() == e.MTime {
		return false
	}
	ix.symbols -= len(e.Symbols)
	ne := &Entry{Path: rel, Size: info.Size(), MTime: info.ModTime().UnixNano(), Symbols: parse(abs)}
	ix.symbols += len(ne.Symbols)
	ix.entries[rel] = ne
	return true
}

// Match tiers, best first.
const (
	tierExact = iota
	tierFold
	tierPrefix
	tierContains
)

// Lookup returns up to limit declarations of name, best match first: an exact
// name, then the same name ignoring case, then a name that starts with it, then
// one that contains it. A query of the form Type.Method matches the method of
// that receiver. kind, when not empty, keeps only that declaration kind. Rows
// of one tier are ordered by path and line, so the answer is deterministic.
func (ix *Index) Lookup(name, kind string, limit int) []Hit {
	container := ""
	if i := strings.LastIndex(name, "."); i >= 0 {
		container, name = name[:i], name[i+1:]
	}
	if name == "" || limit <= 0 {
		return nil
	}
	low := strings.ToLower(name)
	type ranked struct {
		Hit
		tier int
	}
	var all []ranked
	ix.mu.RLock()
	for _, e := range ix.entries {
		for _, s := range e.Symbols {
			if kind != "" && s.Kind != kind {
				continue
			}
			if container != "" && !strings.EqualFold(s.Container, container) {
				continue
			}
			tier := -1
			sl := strings.ToLower(s.Name)
			switch {
			case s.Name == name:
				tier = tierExact
			case sl == low:
				tier = tierFold
			case strings.HasPrefix(sl, low):
				tier = tierPrefix
			case strings.Contains(sl, low):
				tier = tierContains
			}
			if tier >= 0 {
				all = append(all, ranked{Hit{Path: e.Path, Symbol: s}, tier})
			}
		}
	}
	ix.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]Hit, len(all))
	for i, r := range all {
		out[i] = r.Hit
	}
	return out
}

// String renders the stats as one status line.
func (s Stats) String() string {
	line := fmt.Sprintf("%d declaration(s) in %d of %d source file(s)", s.Symbols, s.Files, s.Eligible)
	if s.Truncated {
		line += "; a bound stopped the index short, so some files are not covered"
	}
	return line
}
