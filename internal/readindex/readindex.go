// Package readindex is the harness's in-memory record of the file reads a
// session has already delivered to the model, so a repeated read of an
// unchanged file is answered deterministically instead of re-sending the same
// bytes.
//
// Models re-read. A planning pass read the same files five to ten times each
// (session b3a026a4), and every copy cost a full-context round, a classifier
// call and context that the clearing pass then had to drop. Belai performs
// every file change itself, so it knows exactly when a file it has shown the
// model stops being current.
//
// The index never holds file contents: a tool result can be far too large to
// keep a second copy of. An entry is the resolved path and range, the file's
// size and modification time, its git blob id, and the id of the tool result
// that carried the bytes. Whether that result is still in the conversation is
// asked of the caller at lookup time; when it is not (cleared or compacted),
// the read goes to disk as normal.
package readindex

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxBlobBytes bounds the file size the git blob id is computed for. The id is
// a harness fact for the map; hashing a multi-gigabyte log to print it is not
// worth the disk read, and the stat stamp alone still guards validity.
const maxBlobBytes = 8 << 20

// Key identifies one read: the absolute path and the requested window.
// Offset and Limit are the call's arguments, zero when absent.
type Key struct {
	Path   string
	Offset int64
	Limit  int64
}

// Entry is what the index knows about one delivered read.
type Entry struct {
	Key
	// Size and ModTime are the file's stat when it was read; a lookup
	// against a file whose stat differs misses.
	Size    int64
	ModTime time.Time
	// Blob is the git blob id of the content read ("" above maxBlobBytes).
	Blob string
	// CallID is the tool call whose result carried the bytes.
	CallID string
	// ResultHash is the SHA-256 of that result as delivered. Liveness is
	// checked against it, not the call id: some OpenAI-compatible servers
	// reuse ids, and a reused id must never vouch for another result.
	ResultHash string
	// Whole is true when the read showed the entire file, so it answers a
	// later windowed read of the same file too.
	Whole bool
	// Span is the harness-rendered extent, e.g. "lines 1–200 of 450".
	Span string
}

// Index is safe for concurrent use: the tool fan-out records from several
// goroutines.
type Index struct {
	mu      sync.Mutex
	entries map[Key]Entry
}

// New returns an empty index.
func New() *Index { return &Index{entries: map[Key]Entry{}} }

// Record notes that result, delivered by callID, carried key to the model. It
// stats the file now; a file that cannot be statted is not recorded. whole and
// span describe the extent the result showed.
func (x *Index) Record(key Key, callID, result string, whole bool, span string) {
	if x == nil || result == "" {
		return
	}
	fi, err := os.Stat(key.Path)
	if err != nil || fi.IsDir() {
		return
	}
	e := Entry{Key: key, Size: fi.Size(), ModTime: fi.ModTime(), CallID: callID, ResultHash: HashResult(result), Whole: whole, Span: span}
	if fi.Size() <= maxBlobBytes {
		e.Blob = blobID(key.Path, fi.Size())
	}
	x.mu.Lock()
	x.entries[key] = e
	x.mu.Unlock()
}

// HashResult is the fingerprint Record stores and callers compare conversation
// turns against.
func HashResult(result string) string {
	sum := sha256.Sum256([]byte(result))
	return hex.EncodeToString(sum[:])
}

// Lookup returns the entry that already answers key: the same window, or a
// whole-file read of the same path. It misses when the file's stat changed
// since, or when live reports the carrying result is no longer in the
// conversation; a stale entry is dropped on the way.
func (x *Index) Lookup(key Key, live func(Entry) bool) (Entry, bool) {
	if x == nil {
		return Entry{}, false
	}
	fi, err := os.Stat(key.Path)
	if err != nil || fi.IsDir() {
		return Entry{}, false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	candidates := []Key{key}
	if key.Offset != 0 || key.Limit != 0 {
		candidates = append(candidates, Key{Path: key.Path})
	}
	for i, k := range candidates {
		e, ok := x.entries[k]
		if !ok {
			continue
		}
		if e.Size != fi.Size() || !e.ModTime.Equal(fi.ModTime()) {
			delete(x.entries, k)
			continue
		}
		if i > 0 && !e.Whole {
			continue
		}
		if live != nil && !live(e) {
			delete(x.entries, k)
			continue
		}
		return e, true
	}
	return Entry{}, false
}

// Invalidate drops every entry for the given paths. A path may be absolute or
// relative to root; the harness calls it with the paths its file-diff recorder
// saw change, so an edit the harness made is never answered from the index.
func (x *Index) Invalidate(root string, paths ...string) {
	if x == nil || len(paths) == 0 {
		return
	}
	drop := map[string]bool{}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		drop[filepath.Clean(p)] = true
	}
	x.mu.Lock()
	for k := range x.entries {
		if drop[filepath.Clean(k.Path)] {
			delete(x.entries, k)
		}
	}
	x.mu.Unlock()
}

// Len reports the number of live entries.
func (x *Index) Len() int {
	if x == nil {
		return 0
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	return len(x.entries)
}

// Summary renders up to max entries as harness facts — paths relative to root,
// extent, size and short blob id, never contents — sorted by path, one per
// line. keep, when non-nil, selects the entries to show (the caller keeps
// only reads whose result is still in the conversation). It feeds the
// per-turn repository status.
func (x *Index) Summary(root string, max int, keep func(Entry) bool) []string {
	if x == nil {
		return nil
	}
	x.mu.Lock()
	entries := make([]Entry, 0, len(x.entries))
	for _, e := range x.entries {
		if keep == nil || keep(e) {
			entries = append(entries, e)
		}
	}
	x.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Path != entries[j].Path {
			return entries[i].Path < entries[j].Path
		}
		return entries[i].Offset < entries[j].Offset
	})
	var out []string
	for _, e := range entries {
		if max > 0 && len(out) >= max {
			out = append(out, fmt.Sprintf("… and %d more", len(entries)-max))
			break
		}
		out = append(out, e.Describe(root))
	}
	return out
}

// Current reports whether the file still has the stat it had when read.
func (e Entry) Current() bool {
	fi, err := os.Stat(e.Path)
	return err == nil && fi.Size() == e.Size && fi.ModTime().Equal(e.ModTime)
}

// Describe renders one entry as a single line of harness facts.
func (e Entry) Describe(root string) string {
	p := e.Path
	if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
		p = rel
	}
	var b strings.Builder
	b.WriteString(p)
	if e.Span != "" {
		b.WriteString(" (" + e.Span + ")")
	}
	fmt.Fprintf(&b, " %dB", e.Size)
	if len(e.Blob) >= 7 {
		b.WriteString(" blob " + e.Blob[:7])
	}
	return b.String()
}

// blobID is the git blob id of the file's current content ("blob <n>\0" then
// the bytes, SHA-1), streamed so the content is never held in memory. It is
// the id `git hash-object` prints for the working-tree file, so a tracked,
// unmodified file shows the same id git records. "" on any read error.
func blobID(path string, size int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", size)
	if n, err := io.Copy(h, f); err != nil || n != size {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
