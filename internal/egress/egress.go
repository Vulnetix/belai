// Package egress is the filtering forward proxy behind `sandbox.network:
// proxy`. A sandboxed command is cut off from the network and given one way
// out: a loopback relay inside the sandbox that copies bytes to a unix socket
// the harness listens on, where the proxy decides, deterministically, whether
// the command may reach the host it asked for. No model is asked whether a host
// is safe.
//
// The proxy speaks two things: CONNECT host:port (HTTPS and anything else
// tunnelled) and plain HTTP requests in absolute form. It never interposes on
// TLS, so it sees the host a client named and nothing of the traffic. A host
// is allowed when it matches an entry of the user's allowlist (a name or
// `*.domain`, see netguard.MatchHost) and the port is 80 or 443. The proxy
// resolves the name itself, refuses the connection when any address is not
// public (netguard.Forbidden), and dials one of those exact addresses, so a
// DNS answer cannot change between the check and the connection.
//
// Denied hosts are kept in a bounded in-memory ring for /egress. Nothing about
// a host is written to disk or to the audit log; the audit event carries a
// count and an enum.
package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/netguard"
)

// Limits. They are fixed: the proxy is a guard, not a service.
const (
	// IdleTimeout ends a connection that moved no bytes in either direction.
	IdleTimeout = 60 * time.Second
	// MaxConns is how many connections one proxy serves at once.
	MaxConns = 64
	// MaxHeaderBytes bounds the request line and headers.
	MaxHeaderBytes = 8 << 10
	// DialTimeout bounds name resolution and each connect.
	DialTimeout = 10 * time.Second
	// RingSize is how many distinct denials /egress remembers.
	RingSize = 64
)

// Denial reasons. They are an enum: the audit event carries one, never a host.
const (
	ReasonNotAllowed = "not_allowed"
	ReasonPort       = "port"
	ReasonInvalid    = "invalid"
	ReasonAddress    = "address"
	ReasonResolve    = "resolve"
	ReasonLimit      = "limit"
)

// denyBody is the text of a 403. It is fixed and names no host: it reaches
// the model through the command's output.
const denyBody = "Blocked by the Belai egress proxy: this host is not in the sandbox egress allowlist. Ask the user to allow it with /egress allow.\n"

// Resolver looks a host name up. The default is the system resolver.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Dialer opens the outgoing connection to a checked address.
type Dialer func(ctx context.Context, network, addr string) (net.Conn, error)

// Options configure a Server. The zero value of each optional field is the
// production default; tests substitute them.
type Options struct {
	// Allow is the standing allowlist, canonical entries (netguard.CheckAllowEntry).
	Allow []string
	// Registry holds the session allows and the denial ring. Default: Default.
	Registry *Registry
	Resolver Resolver
	Dial     Dialer
	// Idle overrides IdleTimeout, MaxConns overrides MaxConns (tests).
	Idle     time.Duration
	MaxConns int
}

// Server is one proxy. It is safe for concurrent use.
type Server struct {
	ln       net.Listener
	reg      *Registry
	allow    []string
	resolver Resolver
	dial     Dialer
	idle     time.Duration
	sem      chan struct{}

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed atomic.Bool
	wg     sync.WaitGroup
}

// NewServer serves on ln until Close. The caller owns the listener's address
// (a unix socket in production).
func NewServer(ln net.Listener, o Options) *Server {
	s := &Server{
		ln: ln, reg: o.Registry, allow: append([]string(nil), o.Allow...),
		resolver: o.Resolver, dial: o.Dial, idle: o.Idle, conns: map[net.Conn]struct{}{},
	}
	if s.reg == nil {
		s.reg = Default
	}
	if s.resolver == nil {
		s.resolver = net.DefaultResolver
	}
	if s.dial == nil {
		d := &net.Dialer{Timeout: DialTimeout}
		s.dial = d.DialContext
	}
	if s.idle <= 0 {
		s.idle = IdleTimeout
	}
	n := o.MaxConns
	if n <= 0 {
		n = MaxConns
	}
	s.sem = make(chan struct{}, n)
	s.wg.Add(1)
	go s.acceptLoop()
	return s
}

// Close stops accepting, closes every open connection and waits for them.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return err
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closed.Load() || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		select {
		case s.sem <- struct{}{}:
		default:
			s.reg.Deny("", 0, ReasonLimit)
			_ = c.SetWriteDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.WriteString(c, "HTTP/1.1 503 Service Unavailable\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
			_ = c.Close()
			continue
		}
		s.track(c, true)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			defer s.track(c, false)
			defer c.Close()
			s.serve(c)
		}()
	}
}

func (s *Server) track(c net.Conn, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if add {
		s.conns[c] = struct{}{}
	} else {
		delete(s.conns, c)
	}
}

// allowed reports whether the canonical host matches the standing allowlist or
// a session allow.
func (s *Server) allowed(host string) bool {
	for _, e := range s.allow {
		if netguard.MatchHost(e, host) {
			return true
		}
	}
	return s.reg.sessionMatches(host)
}

// head is a parsed request line and headers.
type head struct {
	method, target, version string
	headers                 []header
}

type header struct{ name, value string }

var errHead = errors.New("bad request head")

// readHead reads the request line and headers, at most MaxHeaderBytes, from
// br. It refuses a folded header, a header without a colon and a NUL.
func readHead(br *bufio.Reader) (*head, error) {
	var lines []string
	total := 0
	for {
		line, err := br.ReadSlice('\n')
		total += len(line)
		if total > MaxHeaderBytes || errors.Is(err, bufio.ErrBufferFull) {
			return nil, errTooLarge
		}
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(line, 0) >= 0 {
			return nil, errHead
		}
		l := strings.TrimRight(string(line), "\r\n")
		if l == "" {
			if len(lines) == 0 {
				continue
			}
			break
		}
		lines = append(lines, l)
	}
	parts := strings.Split(lines[0], " ")
	if len(parts) != 3 || !strings.HasPrefix(parts[2], "HTTP/1.") || !validToken(parts[0]) || parts[1] == "" {
		return nil, errHead
	}
	h := &head{method: parts[0], target: parts[1], version: parts[2]}
	for _, l := range lines[1:] {
		if l[0] == ' ' || l[0] == '\t' {
			return nil, errHead
		}
		i := strings.IndexByte(l, ':')
		if i <= 0 || !validToken(l[:i]) {
			return nil, errHead
		}
		h.headers = append(h.headers, header{name: l[:i], value: strings.TrimSpace(l[i+1:])})
	}
	return h, nil
}

var errTooLarge = errors.New("request head too large")

func validToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// idleConn refreshes a deadline before every read and write, so a connection
// that moves nothing for Idle ends.
type idleConn struct {
	net.Conn
	d time.Duration
}

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.d))
	return c.Conn.Read(p)
}

func (c idleConn) Write(p []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(c.d))
	return c.Conn.Write(p)
}

func (c idleConn) CloseWrite() {
	if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func (s *Server) serve(raw net.Conn) {
	c := idleConn{Conn: raw, d: s.idle}
	br := bufio.NewReaderSize(c, MaxHeaderBytes)
	h, err := readHead(br)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			s.reg.Deny("", 0, ReasonInvalid)
			respond(c, 431, "Request Header Fields Too Large", "")
		} else if !errors.Is(err, io.EOF) {
			s.reg.Deny("", 0, ReasonInvalid)
			respond(c, 400, "Bad Request", "")
		}
		return
	}
	if strings.EqualFold(h.method, "CONNECT") {
		s.connect(c, br, h)
		return
	}
	s.forward(c, br, h)
}

// open checks a host and port, the allowlist and the addresses, and returns
// the connection to the destination. On refusal it has already answered the
// client and recorded the denial.
func (s *Server) open(c idleConn, rawHost string, port int) (net.Conn, bool) {
	host, err := netguard.CheckHostPort(rawHost, port)
	if err != nil {
		reason := ReasonInvalid
		if port != 80 && port != 443 {
			reason = ReasonPort
		}
		s.reg.Deny(rawHost, port, reason)
		respond(c, 403, "Forbidden", denyBody)
		return nil, false
	}
	if !s.allowed(host) {
		s.reg.Deny(host, port, ReasonNotAllowed)
		respond(c, 403, "Forbidden", denyBody)
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), DialTimeout)
	defer cancel()
	addrs, err := s.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		s.reg.Deny(host, port, ReasonResolve)
		respond(c, 502, "Bad Gateway", "")
		return nil, false
	}
	// One forbidden address in the answer refuses the host: a name that
	// resolves to both a public and an internal address is how rebinding
	// reaches the internal one.
	for _, a := range addrs {
		if netguard.Forbidden(a) {
			s.reg.Deny(host, port, ReasonAddress)
			respond(c, 403, "Forbidden", denyBody)
			return nil, false
		}
	}
	for _, a := range addrs {
		if up, derr := s.dial(ctx, "tcp", net.JoinHostPort(a.Unmap().String(), strconv.Itoa(port))); derr == nil {
			return up, true
		}
	}
	s.reg.Deny(host, port, ReasonResolve)
	respond(c, 502, "Bad Gateway", "")
	return nil, false
}

func (s *Server) connect(c idleConn, br *bufio.Reader, h *head) {
	host, portStr, err := net.SplitHostPort(h.target)
	port, perr := strconv.Atoi(portStr)
	if err != nil || perr != nil || port < 1 || port > 65535 {
		s.reg.Deny("", 0, ReasonInvalid)
		respond(c, 400, "Bad Request", "")
		return
	}
	up, ok := s.open(c, host, port)
	if !ok {
		return
	}
	defer up.Close()
	s.track(up, true)
	defer s.track(up, false)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	pipe(c, br, idleConn{Conn: up, d: s.idle})
}

// hopByHop are headers the proxy does not forward: they describe the
// client-to-proxy hop, and Host is replaced with the checked one.
var hopByHop = map[string]bool{
	"connection": true, "proxy-connection": true, "proxy-authorization": true, "keep-alive": true,
	"upgrade": true, "host": true, "te": true, "trailer": true,
}

func (s *Server) forward(c idleConn, br *bufio.Reader, h *head) {
	u, err := netguard.CheckURL(h.target, netguard.Fetch)
	if err != nil || u.Scheme != "http" {
		s.reg.Deny("", 0, ReasonInvalid)
		respond(c, 400, "Bad Request", "")
		return
	}
	port := 80
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil {
			s.reg.Deny("", 0, ReasonInvalid)
			respond(c, 400, "Bad Request", "")
			return
		}
	}
	up, ok := s.open(c, u.Hostname(), port)
	if !ok {
		return
	}
	defer up.Close()
	s.track(up, true)
	defer s.track(up, false)
	var b strings.Builder
	b.WriteString(h.method + " " + (&url.URL{Path: u.Path, RawPath: u.RawPath, RawQuery: u.RawQuery}).RequestURI() + " HTTP/1.1\r\n")
	b.WriteString("Host: " + u.Host + "\r\n")
	for _, hd := range h.headers {
		if hopByHop[strings.ToLower(hd.name)] {
			continue
		}
		b.WriteString(hd.name + ": " + hd.value + "\r\n")
	}
	// One request per upstream connection: the bytes after this head go to the
	// host that was checked and to no other.
	b.WriteString("Connection: close\r\n\r\n")
	if _, err := io.WriteString(up, b.String()); err != nil {
		return
	}
	pipe(c, br, idleConn{Conn: up, d: s.idle})
}

// pipe copies both ways until both directions end, half-closing each write
// side so a client that shuts down its sending half still gets the response.
func pipe(client idleConn, clientIn io.Reader, up idleConn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, clientIn)
		up.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, up)
		client.CloseWrite()
	}()
	wg.Wait()
}

func respond(c io.Writer, code int, text, body string) {
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s", code, text, len(body), body)
}
