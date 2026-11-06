package egress

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/netguard"
	"github.com/vulnetix/belai/internal/sanitize"
)

// MaxSessionEntries bounds the hosts a user can allow for the session.
const MaxSessionEntries = 64

// maxSocketPath keeps a socket path inside what sockaddr_un holds on Linux
// (108) and macOS (104).
const maxSocketPath = 100

// ErrSocketPath is returned when the state directory is so deep that the
// proxy's socket path would not fit a unix socket address.
var ErrSocketPath = errors.New("the egress socket path is too long for a unix socket; set BELAI_HOME to a shorter directory")

// Denial is one refused connection, as /egress shows it. The host is cleaned
// for one line of display and is never an instruction.
type Denial struct {
	Host   string
	Port   int
	Reason string
	Count  int
	Last   time.Time
}

// Registry is the process's egress state: the proxies it started, the hosts the
// user allowed for the session, and the denials. The zero value is not usable;
// use NewRegistry or Default.
type Registry struct {
	mu      sync.Mutex
	session []string
	ring    []Denial
	counts  map[string]int
	servers map[string]*Server
	seq     int
	swept   bool

	// dir returns the socket directory; tests substitute it.
	dir func() (string, error)
	now func() time.Time

	resolver Resolver
	dial     Dialer
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{counts: map[string]int{}, servers: map[string]*Server{}, dir: config.EgressDir, now: time.Now}
}

// Default is the process's registry.
var Default = NewRegistry()

// SetNetwork replaces the resolver and the dialer proxies started from now on
// use. It exists so a test can point a proxy at a local server; production
// leaves both at the system's.
func (r *Registry) SetNetwork(res Resolver, dial Dialer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolver, r.dial = res, dial
}

// SetDir replaces the socket directory (tests).
func (r *Registry) SetDir(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dir = func() (string, error) { return dir, nil }
}

// Ensure returns the socket of the proxy for this allowlist, starting it on the
// first call. Entries must already be canonical (netguard.CheckAllowList).
// Commands with the same allowlist share one proxy.
func Ensure(allow []string) (string, error) { return Default.Ensure(allow) }

// Shutdown closes the default registry's proxies and removes their sockets.
func Shutdown() { Default.Shutdown() }

func listKey(allow []string) string {
	c := append([]string(nil), allow...)
	sort.Strings(c)
	return strings.Join(c, "\n")
}

// Ensure is the registry's [Ensure].
func (r *Registry) Ensure(allow []string) (string, error) {
	key := listKey(allow)
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.servers[key]; ok && !s.closed.Load() {
		return s.ln.Addr().String(), nil
	}
	dir, err := r.dir()
	if err != nil {
		return "", err
	}
	if err := prepareDir(dir); err != nil {
		return "", err
	}
	if !r.swept {
		r.swept = true
		sweepStale(dir)
	}
	r.seq++
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.sock", os.Getpid(), r.seq))
	if len(path) > maxSocketPath {
		return "", ErrSocketPath
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return "", err
	}
	r.servers[key] = NewServer(ln, Options{Allow: allow, Registry: r, Resolver: r.resolver, Dial: r.dial})
	return path, nil
}

// Shutdown closes every proxy this registry started. The unix listener removes
// its own socket file when closed.
func (r *Registry) Shutdown() {
	r.mu.Lock()
	servers := r.servers
	r.servers = map[string]*Server{}
	r.mu.Unlock()
	for _, s := range servers {
		_ = s.Close()
	}
}

// prepareDir makes the socket directory, owner-only, and refuses a link: the
// sandbox exposes one socket from it, and a link would move that somewhere else.
func prepareDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("egress directory %s is not a plain directory", dir)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

var sockName = regexp.MustCompile(`^(\d+)-\d+\.sock$`)

// sweepStale removes sockets a crashed process left behind.
func sweepStale(dir string) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range des {
		m := sockName.FindStringSubmatch(d.Name())
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid == os.Getpid() || pidAlive(pid) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, d.Name()))
	}
}

// AllowSession lets the user allow one more host (a name or *.domain) for the
// rest of this process. It is a user action: no tool and no settings layer
// reaches it. It returns the canonical entry.
func (r *Registry) AllowSession(entry string) (string, error) {
	e, err := netguard.CheckAllowEntry(entry)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, have := range r.session {
		if have == e {
			return e, nil
		}
	}
	if len(r.session) >= MaxSessionEntries {
		return "", fmt.Errorf("at most %d session entries", MaxSessionEntries)
	}
	r.session = append(r.session, e)
	return e, nil
}

// RevokeSession removes a session entry and reports whether it was there.
func (r *Registry) RevokeSession(entry string) bool {
	e, err := netguard.CheckAllowEntry(entry)
	if err != nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, have := range r.session {
		if have == e {
			r.session = append(r.session[:i], r.session[i+1:]...)
			return true
		}
	}
	return false
}

// SessionAllowed lists the session entries.
func (r *Registry) SessionAllowed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.session...)
}

func (r *Registry) sessionMatches(host string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.session {
		if netguard.MatchHost(e, host) {
			return true
		}
	}
	return false
}

// Deny records a refused connection. host may be empty (a request that never
// named one); it is cleaned for display and kept in the ring only.
func (r *Registry) Deny(host string, port int, reason string) {
	host = sanitize.Line(host, 120)
	r.mu.Lock()
	r.counts[reason]++
	n := r.counts[reason]
	found := false
	for i := range r.ring {
		d := &r.ring[i]
		if d.Host == host && d.Port == port && d.Reason == reason {
			d.Count++
			d.Last = r.now()
			// Newest last.
			moved := *d
			r.ring = append(append(r.ring[:i:i], r.ring[i+1:]...), moved)
			found = true
			break
		}
	}
	if !found {
		if len(r.ring) >= RingSize {
			r.ring = r.ring[1:]
		}
		r.ring = append(r.ring, Denial{Host: host, Port: port, Reason: reason, Count: 1, Last: r.now()})
	}
	r.mu.Unlock()
	// The first of a reason, then every tenth power and every thousand: a
	// command in a retry loop does not flood the log. The event holds the
	// reason and a count, never a host.
	if n == 1 || n == 10 || n == 100 || n%1000 == 0 {
		audit.Emit(audit.Fact{
			Kind: audit.EgressDenied, ActorKind: audit.ActorHarness, Outcome: "denied",
			Data: map[string]string{"reason": reason, "count": strconv.Itoa(n)},
		})
	}
}

// Denials returns the recent denials, newest first.
func (r *Registry) Denials() []Denial {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Denial, len(r.ring))
	for i, d := range r.ring {
		out[len(r.ring)-1-i] = d
	}
	return out
}

// Since returns the denials last seen at or after t, newest first.
func (r *Registry) Since(t time.Time) []Denial {
	var out []Denial
	for _, d := range r.Denials() {
		if !d.Last.Before(t) {
			out = append(out, d)
		}
	}
	return out
}

// Total is how many connections the proxy has refused in this process.
func (r *Registry) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.counts {
		n += c
	}
	return n
}
