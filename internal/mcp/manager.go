package mcp

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/tools"
)

const (
	connectTimeout     = 30 * time.Second
	defaultCallTimeout = 60 * time.Second
	maxCallTimeout     = 10 * time.Minute
)

var serverNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Server states.
const (
	StateRunning  = "running"
	StateFailed   = "failed"
	StateDisabled = "disabled"
)

// Status describes one configured server for /mcp.
type Status struct {
	Name      string
	Transport string
	State     string
	Err       string
	Tools     []string
	Diag      string
}

type server struct {
	name   string
	cfg    config.MCPServer
	client *Client
	tools  []tools.Tool
	err    error
}

// Options configures the manager.
type Options struct {
	// Workdir is the working directory for stdio servers.
	Workdir string
	// Sandbox returns the policy for a server with sandbox: true.
	Sandbox func() sandbox.Policy
	// HTTPClient reaches http servers. nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Manager owns the connections to every configured server.
type Manager struct {
	opts    Options
	mu      sync.Mutex
	servers map[string]*server
	ready   sync.WaitGroup
}

var (
	activeMu sync.Mutex
	active   *Manager
)

// SetActive records the process-wide manager that sessions take tools from.
func SetActive(m *Manager) {
	activeMu.Lock()
	active = m
	activeMu.Unlock()
}

// Active returns the process-wide manager, or nil when none was started.
func Active() *Manager {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}

// Start connects every enabled server concurrently and returns once each has
// connected or failed. A failed server is reported by Status and offers no
// tools; it never stops the session.
func Start(ctx context.Context, cfg *config.MCPSettings, opts Options) *Manager {
	m := StartAsync(ctx, cfg, opts)
	m.Wait()
	return m
}

// StartAsync begins connecting every enabled server and returns at once.
// Tools offers whatever has connected so far; Wait blocks until every
// server has connected or failed.
func StartAsync(ctx context.Context, cfg *config.MCPSettings, opts Options) *Manager {
	m := &Manager{opts: opts, servers: map[string]*server{}}
	if cfg == nil {
		return m
	}
	wg := &m.ready
	for name, sc := range cfg.Servers {
		s := &server{name: name, cfg: sc}
		m.servers[name] = s
		if sc.Disabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.connect(ctx, s)
		}()
	}
	return m
}

// Wait blocks until every server has connected or failed.
func (m *Manager) Wait() {
	if m != nil {
		m.ready.Wait()
	}
}

func (m *Manager) connect(ctx context.Context, s *server) {
	c, ts, err := m.dial(ctx, s.name, s.cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	s.client, s.tools, s.err = c, ts, err
}

func (m *Manager) dial(ctx context.Context, name string, sc config.MCPServer) (*Client, []tools.Tool, error) {
	if !serverNameRE.MatchString(name) {
		return nil, nil, fmt.Errorf("server name %q must be letters, digits, _ or - (at most 32)", name)
	}
	var t transport
	switch sc.Transport {
	case "", "stdio":
		if strings.TrimSpace(sc.Command) == "" {
			return nil, nil, fmt.Errorf("stdio server needs a command")
		}
		var pol *sandbox.Policy
		if sc.Sandbox && m.opts.Sandbox != nil {
			p := m.opts.Sandbox()
			pol = &p
		}
		st, err := startStdio(sc.Command, sc.Args, sc.Env, m.opts.Workdir, pol)
		if err != nil {
			return nil, nil, err
		}
		t = st
	case "http":
		if !strings.HasPrefix(sc.URL, "https://") && !strings.HasPrefix(sc.URL, "http://") {
			return nil, nil, fmt.Errorf("http server needs an http(s) url")
		}
		hc := m.opts.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		t = newHTTP(sc.URL, sc.Headers, hc)
	default:
		return nil, nil, fmt.Errorf("unknown transport %q (want stdio or http)", sc.Transport)
	}
	c := &Client{name: name, t: t}
	cctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := c.initialize(cctx); err != nil {
		_ = c.Close()
		return nil, nil, withDiag(err, t)
	}
	infos, err := c.listTools(cctx)
	if err != nil {
		_ = c.Close()
		return nil, nil, withDiag(err, t)
	}
	timeout := defaultCallTimeout
	if sc.TimeoutMS > 0 {
		timeout = min(time.Duration(sc.TimeoutMS)*time.Millisecond, maxCallTimeout)
	}
	seen := map[string]bool{}
	var out []tools.Tool
	for _, info := range infos {
		if info.Name == "" || (len(sc.Tools) > 0 && !slices.Contains(sc.Tools, info.Name)) {
			continue
		}
		tl := newTool(name, info, c, timeout)
		if seen[tl.def.Name] {
			continue
		}
		seen[tl.def.Name] = true
		out = append(out, tl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Definition().Name < out[j].Definition().Name })
	return c, out, nil
}

func withDiag(err error, t transport) error {
	if d := strings.TrimSpace(t.diag()); d != "" {
		lines := strings.Split(d, "\n")
		return fmt.Errorf("%w (server said: %s)", err, clipRunes(flatten(lines[len(lines)-1]), 200))
	}
	return err
}

// Tools returns the tools of every running server, in name order.
func (m *Manager) Tools() []tools.Tool {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.servers))
	for n := range m.servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []tools.Tool
	for _, n := range names {
		out = append(out, m.servers[n].tools...)
	}
	return out
}

// Status lists every configured server.
func (m *Manager) Status() []Status {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for _, s := range m.servers {
		st := Status{Name: s.name, Transport: s.cfg.Transport}
		if st.Transport == "" {
			st.Transport = "stdio"
		}
		switch {
		case s.cfg.Disabled:
			st.State = StateDisabled
		case s.err != nil:
			st.State, st.Err = StateFailed, s.err.Error()
		default:
			st.State = StateRunning
			if s.client != nil {
				st.Diag = s.client.t.diag()
			}
		}
		for _, t := range s.tools {
			st.Tools = append(st.Tools, t.Definition().Name)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Restart reconnects one server.
func (m *Manager) Restart(ctx context.Context, name string) error {
	m.mu.Lock()
	s, ok := m.servers[name]
	if ok && s.client != nil {
		_ = s.client.Close()
		s.client, s.tools = nil, nil
	}
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	if s.cfg.Disabled {
		return fmt.Errorf("MCP server %q is disabled in settings", name)
	}
	m.connect(ctx, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.err
}

// Close stops every server.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.servers {
		if s.client != nil {
			_ = s.client.Close()
			s.client = nil
		}
	}
}
