package lsp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// manager lives in manager.go to keep the exported API in lsp.go small.
type Manager struct {
	opts    Options
	entries map[entryKey]*entry
	mu      sync.Mutex
	cache   *reportCache
	detect  map[string]string // langID -> resolved binary
	stop    chan struct{}
	wg      sync.WaitGroup
}

type entryKey struct {
	langID string
	binary string
	root   string
}

type entry struct {
	lang      *Language
	binary    string
	root      string
	conn      Conn
	client    *client
	cmd       *exec.Cmd
	ready     bool
	warming   bool
	closed    bool
	strikes   int
	restarts  int
	lastCrash time.Time
	lastUsed  time.Time
	budget    time.Duration
	mu        sync.Mutex
}

type reportCache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   func() time.Time
	items map[string]cacheItem
}

type cacheItem struct {
	report Report
	hash   string
	at     time.Time
}

func newManager(o Options) *Manager {
	budget := o.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	m := &Manager{
		opts: Options{
			Roots:    o.Roots,
			Enabled:  o.Enabled,
			Fallback: o.Fallback,
			Budget:   budget,
			MaxLive:  o.MaxLive,
			Servers:  o.Servers,
			Start:    o.Start,
			Now:      o.Now,
		},
		entries: map[entryKey]*entry{},
		cache: &reportCache{
			ttl:   defaultCacheTTL,
			now:   defaultNow(o.Now),
			items: map[string]cacheItem{},
		},
		detect: map[string]string{},
		stop:   make(chan struct{}),
	}
	if m.opts.MaxLive <= 0 {
		m.opts.MaxLive = defaultMaxLive
	}
	if m.opts.Start == nil {
		m.opts.Start = defaultStart
	}
	m.wg.Add(1)
	go m.janitor()
	return m
}

func (m *Manager) diagnose(ctx context.Context, abs string, content []byte) Report {
	lang := LanguageFor(abs)
	if lang == nil {
		return Report{Status: StatusUnsupported}
	}

	// Windows is unsupported for v1.
	if runtime.GOOS == "windows" {
		if m.opts.Fallback && canFallback(lang, abs, "") {
			r := runFallback(ctx, lang, abs)
			r.Language = lang.Display
			return r
		}
		return Report{Language: lang.Display, Status: StatusUnavailable}
	}

	if m.opts.Enabled != nil && !m.opts.Enabled(lang.ID) {
		return Report{Language: lang.Display, Status: StatusUnavailable}
	}

	// Content-hash cache.
	if cached := m.cache.get(abs, content); cached != nil {
		return *cached
	}

	root := rootFor(abs, m.opts.Roots)
	binary := m.resolveBinary(lang)

	// No server available -> try fallback.
	if binary == "" {
		if m.opts.Fallback && canFallback(lang, abs, root) {
			r := runFallback(ctx, lang, abs)
			r.Language = lang.Display
			m.cache.set(abs, content, r)
			return r
		}
		return Report{Language: lang.Display, Status: StatusUnavailable}
	}

	key := entryKey{langID: lang.ID, binary: binary, root: root}
	m.mu.Lock()
	e, ok := m.entries[key]
	if !ok {
		m.evictIfNeededLocked()
		e = &entry{lang: lang, binary: binary, root: root, budget: m.opts.Budget}
		m.entries[key] = e
	}
	m.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	now := m.now()
	e.lastUsed = now

	// If still warming, skip the wait but fall back if possible.
	if e.warming {
		if m.opts.Fallback && canFallback(lang, abs, root) {
			r := runFallback(ctx, lang, abs)
			r.Language = lang.Display
			m.cache.set(abs, content, r)
			return r
		}
		return Report{Language: lang.Display, Status: StatusWarming}
	}

	// Cold entry: initialize and mark warming.
	if e.client == nil {
		e.warming = true
		go m.initEntry(key, e)
		if m.opts.Fallback && canFallback(lang, abs, root) {
			r := runFallback(ctx, lang, abs)
			r.Language = lang.Display
			m.cache.set(abs, content, r)
			return r
		}
		return Report{Language: lang.Display, Status: StatusWarming}
	}

	// Manifest edits invalidate the server's index; notify and return warming.
	if isManifest(abs) {
		if e.client != nil {
			_ = e.client.t.notifyJSON("workspace/didChangeWatchedFiles", DidChangeWatchedFilesParams{
				Changes: []FileEvent{{URI: pathToURI(abs), Type: fileChangeChanged}},
			})
		}
		if m.opts.Fallback && canFallback(lang, abs, root) {
			r := runFallback(ctx, lang, abs)
			r.Language = lang.Display
			m.cache.set(abs, content, r)
			return r
		}
		return Report{Language: lang.Display, Status: StatusWarming}
	}

	// Run live diagnosis within the per-key adaptive budget.
	budget := e.budget
	if budget <= 0 {
		budget = m.opts.Budget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	report, err := e.client.diagnose(ctx, abs, content)
	report.Language = lang.Display
	if err != nil {
		// Budget exceeded.
		if ctx.Err() == context.DeadlineExceeded {
			e.strikes++
			m.adjustStrike(e)
			if m.opts.Fallback && canFallback(lang, abs, root) {
				r := runFallback(ctx, lang, abs)
				r.Language = lang.Display
				m.cache.set(abs, content, r)
				return r
			}
			return Report{Language: lang.Display, Status: StatusTimeout}
		}
		// Server error counts as a crash.
		m.handleCrash(key, e)
		if m.opts.Fallback && canFallback(lang, abs, root) {
			r := runFallback(context.Background(), lang, abs) // ctx may be done.
			r.Language = lang.Display
			m.cache.set(abs, content, r)
			return r
		}
		return Report{Language: lang.Display, Status: StatusUnavailable}
	}

	// Success resets strikes and marks ready.
	e.strikes = 0
	e.budget = m.opts.Budget
	e.ready = true
	m.cache.set(abs, content, report)
	return report
}

func (m *Manager) resolveBinary(lang *Language) string {
	if override := m.opts.Servers[lang.ID]; override != "" {
		return override
	}
	if cached, ok := m.detect[lang.ID]; ok {
		return cached
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if p, ok := Detect(ctx, realProbe{}, lang.Server, lang.Alts); ok {
		m.detect[lang.ID] = p
		return p
	}
	return ""
}

func (m *Manager) initEntry(key entryKey, e *entry) {
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	argv := []string{e.binary}
	for _, a := range e.lang.Args {
		if a == "<cachedir>" {
			a = filepath.Join(cacheDir(), "lsp", e.lang.ID)
		}
		argv = append(argv, a)
	}
	conn, err := m.opts.Start(ctx, argv, e.root)
	if err != nil {
		m.handleCrash(key, e)
		return
	}

	cl := newClient(e.lang, conn, []string{e.root})
	if err := cl.initialize(ctx, e.lang); err != nil {
		_ = conn.Close()
		m.handleCrash(key, e)
		return
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		_ = cl.close()
		return
	}
	e.conn = conn
	e.client = cl
	e.ready = true
	e.warming = false
	e.mu.Unlock()
}

func (m *Manager) handleCrash(key entryKey, e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	e.mu.Lock()
	if e.client != nil {
		_ = e.client.close()
	}
	if e.conn != nil {
		_ = e.conn.Close()
	}
	e.client = nil
	e.conn = nil
	e.ready = false
	e.warming = false
	e.lastCrash = m.now()

	// Restart policy: at most one restart per key per session, with a cooldown.
	e.restarts++
	if e.restarts <= 1 {
		e.mu.Unlock()
		go func() {
			time.Sleep(restartCooldown)
			m.mu.Lock()
			delete(m.entries, key)
			m.mu.Unlock()
		}()
		return
	}
	e.mu.Unlock()
	// Second crash: permanently unavailable for this session.
	delete(m.entries, key)
}

func (m *Manager) adjustStrike(e *entry) {
	if e.strikes >= strikeDisable {
		e.budget = 0
	} else if e.strikes >= strikeBudgetHalve {
		half := e.budget / 2
		if half < 100*time.Millisecond {
			half = 100 * time.Millisecond
		}
		e.budget = half
	}
}

func (m *Manager) warm(langIDs []string) {
	for _, id := range langIDs {
		if !KnownLanguageID(id) {
			continue
		}
		lang := langByID[id]
		binary := m.resolveBinary(lang)
		if binary == "" {
			continue
		}
		// Warm one root at a time; without a specific file we use the first root.
		root := ""
		if len(m.opts.Roots) > 0 {
			root = m.opts.Roots[0]
		}
		key := entryKey{langID: id, binary: binary, root: root}
		m.mu.Lock()
		if _, ok := m.entries[key]; ok {
			m.mu.Unlock()
			continue
		}
		m.evictIfNeededLocked()
		e := &entry{lang: lang, binary: binary, root: root, budget: m.opts.Budget, warming: true}
		m.entries[key] = e
		m.mu.Unlock()
		go m.initEntry(key, e)
	}
}

func (m *Manager) close() error {
	close(m.stop)
	m.wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.entries {
		if cl := e.client; cl != nil {
			_ = cl.close()
		} else if e.conn != nil {
			_ = e.conn.Close()
		}
	}
	m.entries = map[entryKey]*entry{}
	return nil
}

func (m *Manager) janitor() {
	defer m.wg.Done()
	ticker := time.NewTicker(janitorTick)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-ticker.C:
			m.evictIdle()
		}
	}
}

func (m *Manager) evictIdle() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for k, e := range m.entries {
		if now.Sub(e.lastUsed) > defaultIdleEviction {
			if cl := e.client; cl != nil {
				_ = cl.close()
			} else if e.conn != nil {
				_ = e.conn.Close()
			}
			delete(m.entries, k)
		}
	}
}

func (m *Manager) evictIfNeededLocked() {
	if len(m.entries) < m.opts.MaxLive {
		return
	}
	var oldest *entryKey
	var oldestTime time.Time
	for k, e := range m.entries {
		if oldest == nil || e.lastUsed.Before(oldestTime) {
			oldest = &k
			oldestTime = e.lastUsed
		}
	}
	if oldest != nil {
		e := m.entries[*oldest]
		if cl := e.client; cl != nil {
			_ = cl.close()
		} else if e.conn != nil {
			_ = e.conn.Close()
		}
		delete(m.entries, *oldest)
	}
}

func (m *Manager) now() time.Time {
	return defaultNow(m.opts.Now)()
}

func defaultNow(fn func() time.Time) func() time.Time {
	if fn != nil {
		return fn
	}
	return time.Now
}

func cacheDir() string {
	d, _ := os.UserCacheDir()
	if d == "" {
		d = "/tmp"
	}
	return d
}

func rootFor(abs string, roots []string) string {
	for _, r := range roots {
		clean := filepath.Clean(r)
		prefix := clean + string(filepath.Separator)
		if strings.HasPrefix(abs, prefix) {
			return clean
		}
	}
	if len(roots) > 0 {
		return filepath.Clean(roots[0])
	}
	return filepath.Dir(abs)
}

func isManifest(abs string) bool {
	base := filepath.Base(abs)
	switch base {
	case "go.mod", "go.sum", "package.json", "tsconfig.json", "Cargo.toml", "pyproject.toml":
		return true
	}
	return false
}

// defaultStart is the real process spawner.
func defaultStart(ctx context.Context, argv []string, dir string) (Conn, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	return &cmdConn{cmd: cmd, stdin: stdin, stdout: stdout}, nil
}

type cmdConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
}

func (c *cmdConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *cmdConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *cmdConn) Close() error {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	if c.cmd.Process != nil {
		return c.cmd.Cancel()
	}
	return nil
}
func (c *cmdConn) Wait() error { return c.cmd.Wait() }

// report cache methods.
func (c *reportCache) get(abs string, content []byte) *Report {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := fileHash(abs, content)
	item, ok := c.items[h]
	if !ok {
		return nil
	}
	if c.now().Sub(item.at) > c.ttl {
		delete(c.items, h)
		return nil
	}
	return &item.report
}

func (c *reportCache) set(abs string, content []byte, r Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := fileHash(abs, content)
	c.items[h] = cacheItem{report: r, hash: h, at: c.now()}
}

func fileHash(abs string, content []byte) string {
	h := sha256.New()
	h.Write([]byte(abs))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
