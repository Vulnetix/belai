package egress

import (
	"bufio"
	"context"
	"fmt"
	"github.com/vulnetix/belai/internal/audit"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

var public = netip.MustParseAddr("93.184.216.34")

// harness is a proxy in front of a recording origin.
type harness struct {
	t      *testing.T
	reg    *Registry
	srv    *Server
	sock   string
	origin net.Listener

	mu     sync.Mutex
	dialed []string
	got    chan string
}

func newHarness(t *testing.T, allow []string, opt func(*Options)) *harness {
	t.Helper()
	h := &harness{t: t, reg: NewRegistry(), got: make(chan string, 4)}
	origin, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h.origin = origin
	t.Cleanup(func() { origin.Close() })
	go func() {
		for {
			c, err := origin.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				var b strings.Builder
				for {
					line, err := br.ReadString('\n')
					b.WriteString(line)
					if err != nil || line == "\r\n" {
						break
					}
				}
				h.got <- b.String()
				io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nhi")
			}()
		}
	}()
	dir, err := os.MkdirTemp("", "egp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h.sock = filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	o := Options{
		Allow: allow, Registry: h.reg,
		Resolver: fakeResolver{
			"allowed.test": {public}, "rebind.test": {public, netip.MustParseAddr("10.0.0.5")},
			"loop.test": {netip.MustParseAddr("127.0.0.1")}, "meta.test": {netip.MustParseAddr("169.254.169.254")},
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			h.mu.Lock()
			h.dialed = append(h.dialed, addr)
			h.mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, origin.Addr().String())
		},
	}
	if opt != nil {
		opt(&o)
	}
	h.srv = NewServer(ln, o)
	t.Cleanup(func() { h.srv.Close() })
	return h
}

func (h *harness) conn() net.Conn {
	c, err := net.Dial("unix", h.sock)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	return c
}

// exchange sends raw and returns everything the proxy answers until it closes.
func (h *harness) exchange(raw string) string {
	c := h.conn()
	io.WriteString(c, raw)
	if u, ok := c.(*net.UnixConn); ok {
		u.CloseWrite()
	}
	b, _ := io.ReadAll(c)
	return string(b)
}

func status(resp string) string {
	line, _, _ := strings.Cut(resp, "\r\n")
	return line
}

func TestConnectThroughAnAllowedHost(t *testing.T) {
	h := newHarness(t, []string{"allowed.test"}, nil)
	c := h.conn()
	io.WriteString(c, "CONNECT allowed.test:443 HTTP/1.1\r\nHost: allowed.test:443\r\n\r\n")
	br := bufio.NewReader(c)
	if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("status %q", line)
	}
	br.ReadString('\n') // blank line
	io.WriteString(c, "GET / HTTP/1.1\r\nHost: allowed.test\r\n\r\n")
	if got := <-h.got; !strings.HasPrefix(got, "GET / HTTP/1.1") {
		t.Fatalf("the origin got %q", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.dialed) != 1 || h.dialed[0] != "93.184.216.34:443" {
		t.Fatalf("the proxy must dial the address it checked: %v", h.dialed)
	}
}

func TestPlainHTTPIsRewrittenAndPinnedToTheCheckedHost(t *testing.T) {
	h := newHarness(t, []string{"allowed.test"}, nil)
	resp := h.exchange("GET http://allowed.test/a/b?q=1 HTTP/1.1\r\nHost: evil.test\r\nProxy-Connection: keep-alive\r\nProxy-Authorization: Basic eA==\r\nConnection: keep-alive\r\nX-Keep: yes\r\n\r\n")
	if !strings.HasPrefix(resp, "HTTP/1.1 200") || !strings.HasSuffix(resp, "hi") {
		t.Fatalf("response %q", resp)
	}
	got := <-h.got
	for _, want := range []string{"GET /a/b?q=1 HTTP/1.1\r\n", "Host: allowed.test\r\n", "X-Keep: yes\r\n", "Connection: close\r\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("origin request lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"evil.test", "Proxy-", "keep-alive"} {
		if strings.Contains(got, bad) {
			t.Errorf("origin request still has %q:\n%s", bad, got)
		}
	}
}

func TestRefusals(t *testing.T) {
	h := newHarness(t, []string{"allowed.test", "rebind.test", "loop.test", "meta.test", "*.wild.test", "nx.test"}, nil)
	cases := []struct {
		name, raw, want, reason string
	}{
		{"not on the list", "CONNECT other.test:443 HTTP/1.1\r\n\r\n", "403", ReasonNotAllowed},
		{"wildcard apex", "CONNECT wild.test:443 HTTP/1.1\r\n\r\n", "403", ReasonNotAllowed},
		{"port", "CONNECT allowed.test:22 HTTP/1.1\r\n\r\n", "403", ReasonPort},
		{"mixed answer with a private address", "CONNECT rebind.test:443 HTTP/1.1\r\n\r\n", "403", ReasonAddress},
		{"loopback answer", "CONNECT loop.test:443 HTTP/1.1\r\n\r\n", "403", ReasonAddress},
		{"metadata answer", "CONNECT meta.test:80 HTTP/1.1\r\n\r\n", "403", ReasonAddress},
		{"ip literal", "CONNECT 93.184.216.34:443 HTTP/1.1\r\n\r\n", "403", ReasonNotAllowed},
		{"loopback literal", "CONNECT 127.0.0.1:443 HTTP/1.1\r\n\r\n", "403", ReasonInvalid},
		{"internal name", "CONNECT db.internal:443 HTTP/1.1\r\n\r\n", "403", ReasonInvalid},
		{"plain http, other host", "GET http://other.test/ HTTP/1.1\r\n\r\n", "403", ReasonNotAllowed},
		{"plain http, bad port", "GET http://allowed.test:8080/ HTTP/1.1\r\n\r\n", "403", ReasonPort},
		{"origin form", "GET / HTTP/1.1\r\nHost: allowed.test\r\n\r\n", "400", ""},
		{"https absolute form", "GET https://allowed.test/ HTTP/1.1\r\n\r\n", "400", ""},
		{"allowed but unresolvable", "CONNECT nx.test:443 HTTP/1.1\r\n\r\n", "502", ReasonResolve},
		{"garbage", "NOPE\r\n\r\n", "400", ""},
		{"folded header", "GET http://allowed.test/ HTTP/1.1\r\nA: b\r\n c\r\n\r\n", "400", ""},
		{"nul", "GET http://allowed.test/\x00 HTTP/1.1\r\n\r\n", "400", ""},
		{"userinfo", "GET http://u:p@allowed.test/ HTTP/1.1\r\n\r\n", "400", ""},
	}
	for _, c := range cases {
		before := h.reg.Total()
		resp := h.exchange(c.raw)
		if !strings.Contains(status(resp), " "+c.want+" ") {
			t.Errorf("%s: status %q", c.name, status(resp))
		}
		if strings.Contains(resp, "allowed.test") || strings.Contains(resp, "other.test") {
			t.Errorf("%s: the response must not echo a host: %q", c.name, resp)
		}
		if h.reg.Total() != before+1 {
			t.Errorf("%s: the refusal was not counted", c.name)
		}
		if c.reason != "" {
			if d := h.reg.Denials(); len(d) == 0 || d[0].Reason != c.reason {
				t.Errorf("%s: newest denial %+v, want reason %s", c.name, d, c.reason)
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.dialed) != 0 {
		t.Fatalf("a refused request dialled %v", h.dialed)
	}
}

func TestRefusedHostsAreCleanedInTheRing(t *testing.T) {
	h := newHarness(t, nil, nil)
	h.exchange("CONNECT evil\x1b[31m.test:443 HTTP/1.1\r\n\r\n")
	for _, d := range h.reg.Denials() {
		if strings.ContainsAny(d.Host, "\x1b\n\r") {
			t.Fatalf("host not cleaned: %q", d.Host)
		}
	}
}

func TestOversizeHeadersAreRefused(t *testing.T) {
	h := newHarness(t, []string{"allowed.test"}, nil)
	resp := h.exchange("GET http://allowed.test/ HTTP/1.1\r\nX-Big: " + strings.Repeat("a", MaxHeaderBytes) + "\r\n\r\n")
	if !strings.Contains(status(resp), " 431 ") {
		t.Fatalf("status %q", status(resp))
	}
	// Many small headers add up to the same bound.
	var b strings.Builder
	b.WriteString("GET http://allowed.test/ HTTP/1.1\r\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&b, "X-%d: aaaaaaaaaaaaaaaaaaaa\r\n", i)
	}
	b.WriteString("\r\n")
	if resp := h.exchange(b.String()); !strings.Contains(status(resp), " 431 ") {
		t.Fatalf("status %q", status(resp))
	}
}

func TestConnectionCapAnswers503(t *testing.T) {
	h := newHarness(t, nil, func(o *Options) { o.MaxConns = 2 })
	hold := []net.Conn{h.conn(), h.conn()}
	// Wait until the server has taken both.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.srv.sem) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	resp := h.exchange("CONNECT x.test:443 HTTP/1.1\r\n\r\n")
	if !strings.Contains(status(resp), " 503 ") {
		t.Fatalf("status %q", status(resp))
	}
	if d := h.reg.Denials(); len(d) == 0 || d[0].Reason != ReasonLimit {
		t.Fatalf("denials %+v", d)
	}
	for _, c := range hold {
		c.Close()
	}
}

func TestIdleConnectionsEnd(t *testing.T) {
	h := newHarness(t, []string{"allowed.test"}, func(o *Options) { o.Idle = 150 * time.Millisecond })
	c := h.conn()
	start := time.Now()
	// Send nothing: the server must hang up on its own.
	if _, err := io.ReadAll(c); err != nil && !os.IsTimeout(err) {
		t.Logf("read: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("an idle connection stayed open for %v", time.Since(start))
	}
}

func TestSessionAllowAndRevoke(t *testing.T) {
	h := newHarness(t, nil, nil)
	if got := status(h.exchange("CONNECT allowed.test:443 HTTP/1.1\r\n\r\n")); !strings.Contains(got, " 403 ") {
		t.Fatalf("before: %q", got)
	}
	if _, err := h.reg.AllowSession("allowed.test"); err != nil {
		t.Fatal(err)
	}
	c := h.conn()
	io.WriteString(c, "CONNECT allowed.test:443 HTTP/1.1\r\n\r\n")
	if line, _ := bufio.NewReader(c).ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("after allow: %q", line)
	}
	if !h.reg.RevokeSession("ALLOWED.test") {
		t.Fatal("revoke found nothing")
	}
	if got := status(h.exchange("CONNECT allowed.test:443 HTTP/1.1\r\n\r\n")); !strings.Contains(got, " 403 ") {
		t.Fatalf("after revoke: %q", got)
	}
}

func TestSessionAllowValidates(t *testing.T) {
	r := NewRegistry()
	for _, bad := range []string{"", "*", "*.com", "localhost", "10.0.0.1", "a b", "host:443", "https://x.test", "db.internal", "x.test/path"} {
		if _, err := r.AllowSession(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for i := 0; i < MaxSessionEntries; i++ {
		if _, err := r.AllowSession(fmt.Sprintf("h%d.test", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.AllowSession("one-more.test"); err == nil {
		t.Fatal("the session list is unbounded")
	}
}

func TestRingIsBoundedAndDeduplicated(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < RingSize+10; i++ {
		r.Deny(fmt.Sprintf("h%d.test", i), 443, ReasonNotAllowed)
	}
	d := r.Denials()
	if len(d) != RingSize || d[0].Host != fmt.Sprintf("h%d.test", RingSize+9) {
		t.Fatalf("ring %d newest %s", len(d), d[0].Host)
	}
	r.Deny("h70.test", 443, ReasonNotAllowed)
	r.Deny("h70.test", 443, ReasonNotAllowed)
	d = r.Denials()
	if len(d) != RingSize || d[0].Host != "h70.test" || d[0].Count != 3 {
		t.Fatalf("repeat denial: %+v", d[0])
	}
}

func TestEnsureSharesAProxyPerAllowlistAndKeepsTheSocketPrivate(t *testing.T) {
	r := NewRegistry()
	dir, err := os.MkdirTemp("", "ege")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	r.SetDir(filepath.Join(dir, "egress"))
	defer r.Shutdown()
	a, err := r.Ensure([]string{"a.test", "b.test"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := r.Ensure([]string{"b.test", "a.test"})
	c, _ := r.Ensure([]string{"a.test"})
	if a != b || a == c {
		t.Fatalf("sockets %s %s %s", a, b, c)
	}
	st, err := os.Stat(a)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode %v err %v", st.Mode(), err)
	}
	if st, _ := os.Stat(filepath.Dir(a)); st.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", st.Mode())
	}
	r.Shutdown()
	if _, err := os.Stat(a); !os.IsNotExist(err) {
		t.Fatalf("the socket outlived the proxy: %v", err)
	}
}

func TestEnsureRefusesALinkedDirectoryAndSweepsDeadSockets(t *testing.T) {
	root, err := os.MkdirTemp("", "egs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	real := filepath.Join(root, "real")
	os.Mkdir(real, 0o700)
	link := filepath.Join(root, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip(err)
	}
	r := NewRegistry()
	r.SetDir(link)
	if _, err := r.Ensure(nil); err == nil {
		t.Fatal("a symlinked egress directory was used")
	}

	dir := filepath.Join(root, "e")
	os.Mkdir(dir, 0o700)
	dead := filepath.Join(dir, "999999999-1.sock")
	live := filepath.Join(dir, fmt.Sprintf("%d-9.sock", os.Getppid()))
	other := filepath.Join(dir, "notes.txt")
	for _, p := range []string{dead, live, other} {
		os.WriteFile(p, nil, 0o600)
	}
	r2 := NewRegistry()
	r2.SetDir(dir)
	defer r2.Shutdown()
	if _, err := r2.Ensure(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("a dead process's socket was not swept")
	}
	for _, p := range []string{live, other} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed", p)
		}
	}
}

func TestEnsureRefusesAPathTooLongForASocket(t *testing.T) {
	r := NewRegistry()
	r.SetDir(filepath.Join(os.TempDir(), strings.Repeat("d", 120)))
	if _, err := r.Ensure(nil); err != ErrSocketPath {
		t.Fatalf("err %v", err)
	}
}

func TestRelayCopiesBothWays(t *testing.T) {
	dir, err := os.MkdirTemp("", "egr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "r.sock")
	up, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 4)
		io.ReadFull(c, b)
		c.Write([]byte(strings.ToUpper(string(b))))
	}()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { Relay(ln, sock); close(done) }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "PING" {
		t.Fatalf("%q %v", buf, err)
	}
	c.Close()
	ln.Close()
	<-done
}

func TestExecMainRunsTheCommandAndPropagatesItsStatus(t *testing.T) {
	var out, errb strings.Builder
	sock := filepath.Join(t.TempDir(), "x.sock")
	port := func() int {
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		return ln.Addr().(*net.TCPAddr).Port
	}
	if code := ExecMain([]string{"-sock", sock, "-port", fmt.Sprint(port()), "--", "sh", "-c", "exit 7"}, &out, &errb); code != 7 {
		t.Fatalf("code %d (%s)", code, errb.String())
	}
	if code := ExecMain([]string{"-sock", sock, "-port", fmt.Sprint(port()), "--", "sh", "-c", "kill -TERM $$"}, &out, &errb); code != 128+15 {
		t.Fatalf("signalled child: code %d", code)
	}
	if code := ExecMain([]string{"-sock", sock, "-port", fmt.Sprint(port()), "--", "/no/such/binary"}, &out, &errb); code != 127 {
		t.Fatalf("missing binary: code %d", code)
	}
	for _, bad := range [][]string{
		{"-sock", "relative.sock", "-port", "5000", "--", "true"},
		{"-sock", sock, "-port", "0", "--", "true"},
		{"-sock", sock, "-port", "5000"},
		{"-port", "5000", "--", "true"},
	} {
		if code := ExecMain(bad, &out, &errb); code != 125 {
			t.Errorf("%v: code %d", bad, code)
		}
	}
}

// The audit log gets the reason and a running count, on a schedule that does not
// flood it, and never a host.
func TestAuditCarriesAReasonAndACountAndNeverAHost(t *testing.T) {
	rec, err := audit.Open(t.TempDir(), "test-host")
	if err != nil {
		t.Fatal(err)
	}
	audit.SetDefault(rec)
	t.Cleanup(func() { audit.SetDefault(nil) })
	r := NewRegistry()
	for i := 0; i < 12; i++ {
		r.Deny("secret-host.example", 443, ReasonNotAllowed)
	}
	r.Deny("other-host.example", 443, ReasonPort)
	b, err := os.ReadFile(rec.Path())
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if n := strings.Count(text, `"kind":"egress.denied"`); n != 3 {
		t.Fatalf("%d events, want the 1st and 10th not_allowed and the 1st port:\n%s", n, text)
	}
	for _, want := range []string{`"reason":"not_allowed"`, `"count":"10"`, `"reason":"port"`, `"count":"1"`} {
		if !strings.Contains(text, want) {
			t.Errorf("audit lacks %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret-host") || strings.Contains(text, "other-host") || strings.Contains(text, "example") {
		t.Fatalf("a host reached the audit log:\n%s", text)
	}
}
