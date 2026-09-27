package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// Cloud-CLI auth probes (`gh auth status`, `aws sts get-caller-identity`,
// `gcloud config get-value account`, …) cost well over a second per process
// on a machine with several CLIs installed, and every headless run, TUI
// session, background agent and recovery subagent used to pay it again. The
// answer changes only when the user logs in or out, so each probe's verdict
// is cached on disk for probeCacheTTL, keyed by the resolved binary path and
// its modification time. Presence in $PATH is still checked on every run —
// it is cheap — so an uninstalled CLI disappears at once.
//
// An expired verdict (up to probeStaleTTL old) is served at once and refreshed
// in the background, so an expired cache never puts the probes back in front
// of a run. The cache only ever narrows what a stale entry can do: a cached "yes" for a
// CLI whose login has since expired offers a read-only tool whose call fails
// at runtime, which is already how presence-only CLIs behave. A cached "no"
// hides a tool for at most probeCacheTTL after a login.

// probeCacheTTL is how long an auth-probe verdict is reused.
const probeCacheTTL = 30 * time.Minute

type probeEntry struct {
	Mod  int64 `json:"mod"`
	OK   bool  `json:"ok"`
	Seen int64 `json:"seen"`
}

type probeCache struct {
	path    string
	mu      sync.Mutex
	entries map[string]probeEntry
	dirty   bool
	now     func() time.Time
}

func openProbeCache() *probeCache {
	c := &probeCache{entries: map[string]probeEntry{}, now: time.Now}
	dir, err := config.GlobalDir()
	if err != nil {
		return c
	}
	c.path = filepath.Join(dir, "cache", "capability-probes.json")
	if data, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(data, &c.entries)
	}
	return c
}

// key identifies one probe of one binary build.
func probeKey(bin string, args []string) (string, int64, bool) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", 0, false
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", 0, false
	}
	return p + "\x00" + strings.Join(args, " "), fi.ModTime().UnixNano(), true
}

func (c *probeCache) get(key string, mod int64) (bool, bool) {
	v, fresh, _ := c.lookup(key, mod)
	return v, fresh
}

// probeStaleTTL is how long an expired verdict may still be served while a
// background probe refreshes it, so an expired cache never puts the probes
// back in front of a run.
const probeStaleTTL = 24 * time.Hour

// lookup returns a verdict and whether it is fresh or merely usable (stale
// but within probeStaleTTL).
func (c *probeCache) lookup(key string, mod int64) (v, fresh, usable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.Mod != mod {
		return false, false, false
	}
	age := c.now().Sub(time.Unix(0, e.Seen))
	return e.OK, age <= probeCacheTTL, age <= probeStaleTTL
}

func (c *probeCache) put(key string, mod int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = probeEntry{Mod: mod, OK: ok, Seen: c.now().UnixNano()}
	c.dirty = true
}

// save writes the cache atomically. Failure is silent: the cache is an
// accelerant, never a requirement.
func (c *probeCache) save() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty || c.path == "" {
		return
	}
	data, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
	c.dirty = false
}

// cachedRun wraps a probe runner with the cache. A probe cut short by the
// detection deadline is not recorded, so a slow CLI is retried next time
// rather than remembered as logged out.
func (c *probeCache) cachedRun(run func(ctx context.Context, name string, args ...string) bool) func(ctx context.Context, name string, args ...string) bool {
	return func(ctx context.Context, name string, args ...string) bool {
		key, mod, ok := probeKey(name, args)
		if ok {
			v, fresh, usable := c.lookup(key, mod)
			if fresh {
				return v
			}
			if usable {
				// Serve the stale verdict now; refresh it for the next run.
				go func() {
					pctx, cancel := context.WithTimeout(context.Background(), detectTimeout)
					defer cancel()
					nv := run(pctx, name, args...)
					if pctx.Err() == nil {
						c.put(key, mod, nv)
						c.save()
					}
				}()
				return v
			}
		}
		v := run(ctx, name, args...)
		if ok && ctx.Err() == nil {
			c.put(key, mod, v)
		}
		return v
	}
}

// detectMemo holds detection for the life of the process: the TUI, its
// background agents and recovery subagents all ask, and the answer does not
// change between them.
var (
	detectMu   sync.Mutex
	detectMemo *Capabilities
	detectAt   time.Time
)
