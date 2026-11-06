package symindex

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/locate"
)

// The cache file is a magic line, one JSON document and the SHA-256 of both.
// It is a convenience for the next session, never a source of truth: a file
// that fails any check is used for nothing and the index is rebuilt.
const (
	cacheMagic = "belai-symindex 1\n"
	// maxCacheBytes bounds what Load reads, a little over the largest index
	// MaxTotal rows can make.
	maxCacheBytes = 8 << 20
)

type cacheDoc struct {
	Root      string  `json:"root"`
	Eligible  int     `json:"eligible"`
	Truncated bool    `json:"truncated,omitempty"`
	MaxFiles  int     `json:"max_files"`
	Entries   []Entry `json:"entries"`
}

// ErrCache says a cache file was refused.
var ErrCache = errors.New("symbol index cache refused")

// Encode serialises the index as a cache file.
func (ix *Index) Encode() ([]byte, error) {
	ix.mu.RLock()
	doc := cacheDoc{Root: ix.root, Eligible: ix.eligible, Truncated: ix.truncated, MaxFiles: ix.maxFiles}
	doc.Entries = make([]Entry, 0, len(ix.entries))
	for _, e := range ix.entries {
		doc.Entries = append(doc.Entries, *e)
	}
	ix.mu.RUnlock()
	sort.Slice(doc.Entries, func(i, j int) bool { return doc.Entries[i].Path < doc.Entries[j].Path })
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(cacheMagic)
	b.Write(body)
	sum := sha256.Sum256(b.Bytes())
	b.Write(sum[:])
	return b.Bytes(), nil
}

// Decode parses and validates a cache file for root. Every row is checked
// against the same rules a fresh parse obeys; one failure refuses the whole
// file. A cache for another root, or one that holds more than maxFiles files, is
// refused too, so a changed setting rebuilds rather than serving a larger index.
func Decode(data []byte, root string, maxFiles int) (*Index, error) {
	if len(data) > maxCacheBytes || len(data) < len(cacheMagic)+sha256.Size || !bytes.HasPrefix(data, []byte(cacheMagic)) {
		return nil, fmt.Errorf("%w: header", ErrCache)
	}
	body, trailer := data[:len(data)-sha256.Size], data[len(data)-sha256.Size:]
	if sum := sha256.Sum256(body); !bytes.Equal(sum[:], trailer) {
		return nil, fmt.Errorf("%w: checksum", ErrCache)
	}
	var doc cacheDoc
	dec := json.NewDecoder(bytes.NewReader(body[len(cacheMagic):]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCache, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrCache)
	}
	if doc.Root != root || doc.MaxFiles != maxFiles || len(doc.Entries) > maxFiles || doc.Eligible < len(doc.Entries) {
		return nil, fmt.Errorf("%w: scope", ErrCache)
	}
	ix := &Index{root: root, entries: make(map[string]*Entry, len(doc.Entries)), eligible: doc.Eligible, truncated: doc.Truncated, maxFiles: maxFiles}
	for i := range doc.Entries {
		e := doc.Entries[i]
		if !cleanRel(e.Path) || !Indexable(e.Path) || e.Size < 0 || len(e.Symbols) > MaxPerFile {
			return nil, fmt.Errorf("%w: entry", ErrCache)
		}
		if _, dup := ix.entries[e.Path]; dup {
			return nil, fmt.Errorf("%w: duplicate", ErrCache)
		}
		for _, s := range e.Symbols {
			if _, ok := clean(s); !ok {
				return nil, fmt.Errorf("%w: symbol", ErrCache)
			}
		}
		if ix.symbols+len(e.Symbols) > MaxTotal {
			return nil, fmt.Errorf("%w: size", ErrCache)
		}
		ix.symbols += len(e.Symbols)
		ix.entries[e.Path] = &e
	}
	return ix, nil
}

// cleanRel reports whether p is a slash-separated path under a root: bounded,
// no dot or empty segment, no backslash, no control character, and not a
// path the inventory's by-name rules refuse.
func cleanRel(p string) bool {
	if p == "" || len(p) > maxPathLen || path.IsAbs(p) || strings.ContainsRune(p, '\\') || path.Clean(p) != p {
		return false
	}
	for _, r := range p {
		if r < ' ' || r == 0x7f {
			return false
		}
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	_, skip := locate.SkipPath(p)
	return !skip
}

// Load reads the cache file at p for root. Any failure, including a missing
// file, returns an error and the caller builds from scratch.
func Load(p, root string, maxFiles int) (*Index, error) {
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCacheBytes {
		return nil, fmt.Errorf("%w: not a regular file of bounded size", ErrCache)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCacheBytes+1))
	if err != nil {
		return nil, err
	}
	return Decode(data, root, maxFiles)
}

// Save writes the cache file at p under the state directory's lock, through a
// temporary name and a rename, mode 0600. It refuses a directory or file that
// is a symlink, and returns an error rather than overwrite anything but a
// regular file.
func (ix *Index) Save(p string) error {
	data, err := ix.Encode()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a plain directory", dir)
	}
	unlock, err := config.AcquireFileLock(p + ".lock")
	if err != nil {
		return err
	}
	defer unlock()
	if err := knowledge.WriteFileAtomic(p, data); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}
