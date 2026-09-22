package rolemanager

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/vulnetix/signet/internal/config"
)

// defaultSafeCacheSize bounds the session LRU of SAFE verdicts.
const defaultSafeCacheSize = 512

// Cache memoises classifier verdicts keyed by the SHA-256 of the sanitized
// content. SAFE verdicts live in a bounded session LRU; non-SAFE hashes are
// persisted to a small bad-hash set under the global directory so a previously
// refused payload is refused again without a classifier round trip. A lost or
// corrupt cache only costs a reclassification — reads fail open.
type Cache struct {
	mu      sync.Mutex
	safe    map[string]struct{}
	order   []string // insertion order, oldest first
	maxSafe int
	bad     map[string]Sentinel
	badPath string
}

// NewCache returns an empty cache with the given SAFE-verdict bound. badPath is
// where non-SAFE hashes persist; empty disables persistence.
func NewCache(maxSafe int, badPath string) *Cache {
	if maxSafe <= 0 {
		maxSafe = defaultSafeCacheSize
	}
	return &Cache{
		safe:    map[string]struct{}{},
		maxSafe: maxSafe,
		bad:     map[string]Sentinel{},
		badPath: badPath,
	}
}

// LoadCache returns a cache with the persisted bad-hash set loaded. A missing
// file yields an empty set; a corrupt file yields an empty set (fail open: the
// only cost of a lost cache is a reclassification).
func LoadCache(badPath string) (*Cache, error) {
	c := NewCache(defaultSafeCacheSize, badPath)
	data, err := os.ReadFile(badPath)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, err
	}
	var m map[string]Sentinel
	if err := json.Unmarshal(data, &m); err != nil {
		return c, nil
	}
	c.bad = m
	return c, nil
}

// DefaultCachePath returns <GlobalDir>/bad-hashes.json.
func DefaultCachePath() string {
	dir, err := config.GlobalDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "bad-hashes.json")
}

// Key hashes the sanitized content into the cache key for the LLM sentinel
// path (no classifier identity).
func Key(clean string) string {
	return KeyFor("", clean)
}

// KeyFor hashes a classifier identity plus the sanitized content into the
// cache key, so verdicts from different classifier stacks never share a
// bucket. An empty identity yields the content-only key.
func KeyFor(identity, clean string) string {
	h := sha256.New()
	h.Write([]byte(identity))
	h.Write([]byte{0})
	h.Write([]byte(clean))
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached verdict for key, if any.
func (c *Cache) Get(key string) (Sentinel, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.bad[key]; ok {
		record(EventVerdictCacheBad, string(s), "", key[:min(12, len(key))], 0)
		return s, true
	}
	if _, ok := c.safe[key]; ok {
		record(EventVerdictCacheHit, string(SentinelSafe), "", key[:min(12, len(key))], 0)
		return SentinelSafe, true
	}
	return "", false
}

// Put records a verdict. SAFE verdicts enter the bounded LRU; non-SAFE
// verdicts are persisted to the bad-hash set and written through atomically.
func (c *Cache) Put(key string, s Sentinel) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.IsSafe() {
		if _, ok := c.safe[key]; !ok {
			c.safe[key] = struct{}{}
			c.order = append(c.order, key)
			for len(c.safe) > c.maxSafe {
				oldest := c.order[0]
				c.order = c.order[1:]
				delete(c.safe, oldest)
			}
		}
		return nil
	}
	if c.bad[key] == s {
		return nil
	}
	c.bad[key] = s
	return c.persistLocked()
}

func (c *Cache) persistLocked() error {
	if c.badPath == "" {
		return nil
	}
	data, err := json.Marshal(c.bad)
	if err != nil {
		return err
	}
	return config.WriteGlobalFileAtomic(c.badPath, data)
}
