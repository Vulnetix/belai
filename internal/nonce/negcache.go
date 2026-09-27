package nonce

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The negative cache used to last for the process only, so every headless
// run, and every TUI launch, spent a serial round trip before its first model
// call learning again that its provider has no nonce endpoint (the Cloudflare
// AI Gateway answers 400). Verdicts are now kept heavily: an "unsupported"
// answer for UnsupportedTTL, and a transient failure (5xx, timeout, transport
// error, a body that is not the spec's JSON) for TransientTTL, both in
// process and, with a cache file set, on disk keyed by a hash of the base
// URL. A 200 is never cached: a nonce endpoint that works is always asked.

const (
	// UnsupportedTTL is how long a 400/401/403/404/405 answer is believed.
	UnsupportedTTL = 7 * 24 * time.Hour
	// TransientTTL is how long a failing endpoint is left alone.
	TransientTTL = 30 * time.Minute
)

var (
	negMu   sync.Mutex
	negPath string

	// unsupportedURLs and transientURLs are the in-process verdicts, base
	// URL -> expiry.
	unsupportedURLs sync.Map
	transientURLs   sync.Map
)

// SetNegativeCacheFile persists endpoint verdicts to path ("" for the
// process-only cache).
func SetNegativeCacheFile(path string) {
	negMu.Lock()
	negPath = path
	negMu.Unlock()
}

func negKey(baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return hex.EncodeToString(sum[:8])
}

// transientKey keeps transient verdicts apart from unsupported ones in the
// same file; unsupported keys stay the bare hash so older files still read.
func transientKey(baseURL string) string { return "t:" + negKey(baseURL) }

func loadNegative() (map[string]int64, string) {
	negMu.Lock()
	path := negPath
	negMu.Unlock()
	m := map[string]int64{}
	if path == "" {
		return m, ""
	}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m, path
}

func fresh(m *sync.Map, baseURL string) bool {
	v, ok := m.Load(baseURL)
	if !ok {
		return false
	}
	if until, _ := v.(time.Time); time.Now().Before(until) {
		return true
	}
	m.Delete(baseURL)
	return false
}

// knownUnsupported reports a fresh unsupported verdict for baseURL.
func knownUnsupported(baseURL string) bool {
	if fresh(&unsupportedURLs, baseURL) {
		return true
	}
	m, _ := loadNegative()
	seen, ok := m[negKey(baseURL)]
	return ok && time.Since(time.Unix(seen, 0)) < UnsupportedTTL
}

// knownTransient reports a fresh transient-failure verdict for baseURL.
func knownTransient(baseURL string) bool {
	if fresh(&transientURLs, baseURL) {
		return true
	}
	m, _ := loadNegative()
	seen, ok := m[transientKey(baseURL)]
	return ok && time.Since(time.Unix(seen, 0)) < TransientTTL
}

// rememberUnsupported records baseURL's unsupported verdict.
func rememberUnsupported(baseURL string) {
	unsupportedURLs.Store(baseURL, time.Now().Add(UnsupportedTTL))
	writeNegative(negKey(baseURL))
}

// rememberTransient records baseURL's transient failure.
func rememberTransient(baseURL string) {
	transientURLs.Store(baseURL, time.Now().Add(TransientTTL))
	writeNegative(transientKey(baseURL))
}

// ForgetEndpoint clears every verdict for baseURL, so a settings or firewall
// change is probed afresh.
func ForgetEndpoint(baseURL string) {
	unsupportedURLs.Delete(baseURL)
	transientURLs.Delete(baseURL)
	m, path := loadNegative()
	if path == "" {
		return
	}
	_, a := m[negKey(baseURL)]
	_, b := m[transientKey(baseURL)]
	if !a && !b {
		return
	}
	delete(m, negKey(baseURL))
	delete(m, transientKey(baseURL))
	saveNegative(m, path)
}

func writeNegative(key string) {
	m, path := loadNegative()
	if path == "" {
		return
	}
	now := time.Now()
	m[key] = now.Unix()
	// Drop expired entries so the file does not grow without bound.
	for k, seen := range m {
		ttl := UnsupportedTTL
		if len(k) > 2 && k[:2] == "t:" {
			ttl = TransientTTL
		}
		if now.Sub(time.Unix(seen, 0)) >= ttl {
			delete(m, k)
		}
	}
	saveNegative(m, path)
}

func saveNegative(m map[string]int64, path string) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	if os.WriteFile(path+".tmp", data, 0o600) == nil {
		_ = os.Rename(path+".tmp", path)
	}
}
