package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/jsonrpc"
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
	// VulnetixAuth returns the Vulnetix CLI's Authorization header value. It
	// resolves a "vulnetix:cli" header reference on every dial, so a later
	// login takes effect on restart. nil leaves the reference unresolved.
	VulnetixAuth func() (string, error)
	// Builtins are servers compiled into this binary, by name. Each runs
	// in-process and is offered whatever the user's settings say: a settings
	// entry of the same name is ignored, and Upsert and Remove refuse the
	// name. It is nil in every build that has none (docs/mcp.md).
	Builtins map[string]jsonrpc.Handler
}

// VulnetixCLIRef is the header value that stands for the Vulnetix CLI's
// credential. It is never written out resolved: settings hold the reference.
const VulnetixCLIRef = config.VulnetixCLIRef

// resolveHeaders expands every VulnetixCLIRef in an http server's headers.
// The credential is sent only as an Authorization header, only over https,
// and only to vulnetix.com or one of its subdomains, so a hand-edited entry
// cannot hand it to another host.
func (m *Manager) resolveHeaders(rawURL string, headers map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		if v != VulnetixCLIRef {
			out[k] = v
			continue
		}
		if !strings.EqualFold(k, "Authorization") {
			return nil, fmt.Errorf("%s is only accepted in the Authorization header", VulnetixCLIRef)
		}
		if !vulnetixHost(rawURL) {
			return nil, fmt.Errorf("%s is only sent to https://*.vulnetix.com", VulnetixCLIRef)
		}
		if m.opts.VulnetixAuth == nil {
			return nil, fmt.Errorf("%s is not available in this session", VulnetixCLIRef)
		}
		h, err := m.opts.VulnetixAuth()
		if err != nil {
			return nil, fmt.Errorf("vulnetix credential: %w (run /vulnetix setup or `vulnetix auth login`)", err)
		}
		out[k] = h
	}
	return out, nil
}

// vulnetixHost reports whether rawURL is https on vulnetix.com or a subdomain.
func vulnetixHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "vulnetix.com" || strings.HasSuffix(h, ".vulnetix.com")
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
	wg := &m.ready
	for name, h := range opts.Builtins {
		s := &server{name: name, cfg: config.MCPServer{Transport: BuiltinTransport}}
		m.servers[name] = s
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.connect(ctx, s, h)
		}()
	}
	if cfg == nil {
		return m
	}
	for name, sc := range cfg.Servers {
		if _, builtin := opts.Builtins[name]; builtin {
			continue
		}
		s := &server{name: name, cfg: sc}
		m.servers[name] = s
		if sc.Disabled {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.connect(ctx, s, nil)
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

// connect dials s; a non-nil h runs the server in-process instead.
func (m *Manager) connect(ctx context.Context, s *server, h jsonrpc.Handler) {
	c, ts, err := m.dial(ctx, s.name, s.cfg, h)
	m.mu.Lock()
	defer m.mu.Unlock()
	s.client, s.tools, s.err = c, ts, err
}

func (m *Manager) dial(ctx context.Context, name string, sc config.MCPServer, h jsonrpc.Handler) (*Client, []tools.Tool, error) {
	if !serverNameRE.MatchString(name) {
		return nil, nil, fmt.Errorf("server name %q must be letters, digits, _ or - (at most 32)", name)
	}
	var t transport
	switch {
	case h != nil:
		t = newInproc(h)
	default:
		var err error
		if t, err = m.dialRemote(sc); err != nil {
			return nil, nil, err
		}
	}
	return m.finishDial(ctx, name, sc, t)
}

// dialRemote opens the stdio or http transport a settings entry names.
func (m *Manager) dialRemote(sc config.MCPServer) (transport, error) {
	var t transport
	switch sc.Transport {
	case "", "stdio":
		if strings.TrimSpace(sc.Command) == "" {
			return nil, fmt.Errorf("stdio server needs a command")
		}
		var pol *sandbox.Policy
		if sc.Sandbox && m.opts.Sandbox != nil {
			p := m.opts.Sandbox()
			pol = &p
		}
		st, err := startStdio(sc.Command, sc.Args, sc.Env, m.opts.Workdir, pol)
		if err != nil {
			return nil, err
		}
		t = st
	case "http":
		if !strings.HasPrefix(sc.URL, "https://") && !strings.HasPrefix(sc.URL, "http://") {
			return nil, fmt.Errorf("http server needs an http(s) url")
		}
		hc := m.opts.HTTPClient
		if hc == nil {
			hc = http.DefaultClient
		}
		headers, err := m.resolveHeaders(sc.URL, sc.Headers)
		if err != nil {
			return nil, err
		}
		t = newHTTP(sc.URL, headers, hc)
	default:
		return nil, fmt.Errorf("unknown transport %q (want stdio or http)", sc.Transport)
	}
	return t, nil
}

// finishDial handshakes over t and builds the server's tools.
func (m *Manager) finishDial(ctx context.Context, name string, sc config.MCPServer, t transport) (*Client, []tools.Tool, error) {
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
	m.connect(ctx, s, m.opts.Builtins[name])
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.err
}

// Upsert replaces (or adds) one server's configuration and connects it. It is
// how a settings change made in this session, such as /vulnetix mcp, takes
// effect without a restart.
func (m *Manager) Upsert(ctx context.Context, name string, sc config.MCPServer) error {
	if m == nil {
		return fmt.Errorf("MCP is not running in this session")
	}
	if _, builtin := m.opts.Builtins[name]; builtin {
		return fmt.Errorf("MCP server %q is built in and cannot be replaced", name)
	}
	m.mu.Lock()
	if old, ok := m.servers[name]; ok && old.client != nil {
		_ = old.client.Close()
	}
	s := &server{name: name, cfg: sc}
	m.servers[name] = s
	m.mu.Unlock()
	if sc.Disabled {
		return nil
	}
	m.connect(ctx, s, nil)
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.err
}

// Remove stops one server and forgets it.
func (m *Manager) Remove(name string) {
	if m == nil {
		return
	}
	if _, builtin := m.opts.Builtins[name]; builtin {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[name]; ok {
		if s.client != nil {
			_ = s.client.Close()
		}
		delete(m.servers, name)
	}
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
