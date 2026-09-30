package tts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultCacheMB is the cache size cap when none is set.
const DefaultCacheMB = 256

var keyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Key names one synthesis. It is a hash, so a file name never carries the text
// that was read, and it covers everything that changes the audio.
func Key(engine, voice, text string) string {
	h := sha256.New()
	for _, p := range []string{engine, voice, text} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DefaultCacheDir is where synthesised audio is kept: under the user's cache
// directory, beside belai's models.
func DefaultCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "belai", "tts"), nil
}

// Cache keeps synthesised PCM on disk, least recently used out first once it
// grows past MaxBytes. A MaxBytes of zero or less turns it off: Get misses and
// Put stores nothing. The directory is private (0700), files are written whole
// through a .part name, and a key that is not a SHA-256 hex string is refused,
// so a name can never point outside the directory.
type Cache struct {
	Dir      string
	MaxBytes int64

	mu sync.Mutex
}

// NewCache returns a cache in dir capped at mb megabytes.
func NewCache(dir string, mb int) *Cache {
	return &Cache{Dir: dir, MaxBytes: int64(mb) << 20}
}

func (c *Cache) path(key string) (string, bool) {
	if c == nil || c.Dir == "" || c.MaxBytes <= 0 || !keyRE.MatchString(key) {
		return "", false
	}
	return filepath.Join(c.Dir, key+".pcm"), true
}

// Get returns the audio stored under key and marks it recently used.
func (c *Cache) Get(key string) ([]byte, bool) {
	p, ok := c.path(key)
	if !ok {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, err := os.ReadFile(p)
	if err != nil || len(b) == 0 || len(b)%BytesPerSample != 0 {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return b, true
}

// Put stores audio under key, then evicts down to the cap. Audio larger than
// the whole cap is not stored.
func (c *Cache) Put(key string, pcm []byte) error {
	p, ok := c.path(key)
	if !ok || len(pcm) == 0 || int64(len(pcm)) > c.MaxBytes {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, pcm, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return c.evictLocked()
}

type cached struct {
	path string
	size int64
	used time.Time
}

func (c *Cache) listLocked() ([]cached, int64) {
	entries, _ := os.ReadDir(c.Dir)
	var out []cached
	var total int64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".pcm") || !keyRE.MatchString(strings.TrimSuffix(name, ".pcm")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, cached{filepath.Join(c.Dir, name), info.Size(), info.ModTime()})
		total += info.Size()
	}
	return out, total
}

func (c *Cache) evictLocked() error {
	files, total := c.listLocked()
	if total <= c.MaxBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].used.Before(files[j].used) })
	var errs []error
	for _, f := range files {
		if total <= c.MaxBytes {
			break
		}
		if err := os.Remove(f.path); err != nil {
			errs = append(errs, err)
			continue
		}
		total -= f.size
	}
	return errors.Join(errs...)
}

// Size is the bytes of audio held.
func (c *Cache) Size() int64 {
	if c == nil || c.Dir == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, total := c.listLocked()
	return total
}

// Clear removes every cached clip, and only those: other files in the
// directory are left alone.
func (c *Cache) Clear() error {
	if c == nil || c.Dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	files, _ := c.listLocked()
	var errs []error
	for _, f := range files {
		if err := os.Remove(f.path); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
