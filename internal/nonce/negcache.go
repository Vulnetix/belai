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
// AI Gateway answers 400). With a cache file set, the verdict is kept on disk
// for negativeCacheTTL, keyed by a hash of the base URL. Only "unsupported"
// verdicts are stored; a provider that issues nonces is always asked, and a
// transport error is never remembered.

const negativeCacheTTL = 24 * time.Hour

var (
	negMu   sync.Mutex
	negPath string
)

// SetNegativeCacheFile persists unsupported-endpoint verdicts to path ("" for
// the process-only cache).
func SetNegativeCacheFile(path string) {
	negMu.Lock()
	negPath = path
	negMu.Unlock()
}

func negKey(baseURL string) string {
	sum := sha256.Sum256([]byte(baseURL))
	return hex.EncodeToString(sum[:8])
}

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

// knownUnsupported reports a fresh on-disk verdict for baseURL.
func knownUnsupported(baseURL string) bool {
	m, _ := loadNegative()
	seen, ok := m[negKey(baseURL)]
	return ok && time.Since(time.Unix(seen, 0)) < negativeCacheTTL
}

// rememberUnsupported records baseURL's verdict on disk.
func rememberUnsupported(baseURL string) {
	m, path := loadNegative()
	if path == "" {
		return
	}
	m[negKey(baseURL)] = time.Now().Unix()
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
