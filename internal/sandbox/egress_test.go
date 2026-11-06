package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/egress"
	"github.com/vulnetix/belai/internal/posture"
)

func TestFromSettingsMapsTheNetworkSetting(t *testing.T) {
	cases := []struct {
		network               string
		deny, proxy           bool
		wantAllow             []string
		allow, wantAllowAfter []string
	}{
		{"", false, false, nil, nil, nil},
		{"allow", false, false, nil, nil, nil},
		{"deny", true, false, nil, nil, nil},
		{"proxy", false, true, []string{"example.com"}, []string{"Example.COM", "bad entry", "*.com"}, []string{"example.com"}},
		{"banana", true, false, nil, nil, nil},
	}
	for _, c := range cases {
		p := FromSettings(&config.SandboxSettings{Network: c.network, EgressAllow: c.allow}, []string{"/w"}, posture.Defaults())
		if p.DenyNetwork != c.deny || p.Egress != c.proxy {
			t.Errorf("network %q: deny %v egress %v", c.network, p.DenyNetwork, p.Egress)
		}
		if strings.Join(p.EgressAllow, ",") != strings.Join(c.wantAllow, ",") {
			t.Errorf("network %q: allow %v, want %v", c.network, p.EgressAllow, c.wantAllow)
		}
	}
}

func TestBwrapArgsKeepUnshareNetAndBindOnlyTheSocket(t *testing.T) {
	state := t.TempDir()
	p := Policy{Mode: ModeAuto, Hidden: []string{state}, egressSock: filepath.Join(state, "1-1.sock"), egressPort: 4000}
	args := strings.Join(BwrapArgs(p, "", []string{"true"}), " ")
	if !strings.Contains(args, "--unshare-net") {
		t.Fatalf("a proxy policy must keep the network namespace: %s", args)
	}
	tmpfs := strings.Index(args, "--tmpfs "+state)
	bind := strings.Index(args, "--bind "+p.egressSock+" "+p.egressSock)
	if tmpfs < 0 || bind < 0 || bind < tmpfs {
		t.Fatalf("the socket must be bound after the state directory is hidden: %s", args)
	}
	// No proxy, no bind, and a plain allow policy shares the network.
	if got := strings.Join(BwrapArgs(Policy{}, "", []string{"true"}), " "); strings.Contains(got, "--unshare-net") || strings.Contains(got, "--bind ") {
		t.Fatalf("allow policy: %s", got)
	}
}

func TestSeatbeltProfileAllowsOnlyTheRelayPort(t *testing.T) {
	prof := SeatbeltProfile(Policy{egressPort: 4123})
	deny := strings.Index(prof, "(deny network-outbound (remote ip))")
	allow := strings.Index(prof, `(allow network-outbound (remote ip "localhost:4123"))`)
	if deny < 0 || allow < deny {
		t.Fatalf("the allow for the relay port must follow the deny:\n%s", prof)
	}
	if strings.Count(prof, "(allow network-outbound") != 1 {
		t.Fatalf("exactly one outbound allow:\n%s", prof)
	}
	if strings.Contains(SeatbeltProfile(Policy{}), "network-outbound") {
		t.Fatal("an allow policy must not mention the network")
	}
}

func TestLandlockPlanAllowsOnlyTheRelayPort(t *testing.T) {
	list := func(string) ([]llEntry, bool) { return nil, false }
	plan, err := landlockPlan(landlockPolicy{EgressPort: 4123}, landlockNetABI, list)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Net || len(plan.ConnectPorts) != 1 || plan.ConnectPorts[0] != 4123 {
		t.Fatalf("net %v ports %v", plan.Net, plan.ConnectPorts)
	}
	// Deny wins: no port is opened.
	plan, _ = landlockPlan(landlockPolicy{DenyNetwork: true, EgressPort: 4123}, landlockNetABI, list)
	if !plan.Net || len(plan.ConnectPorts) != 0 {
		t.Fatalf("deny with a port: net %v ports %v", plan.Net, plan.ConnectPorts)
	}
	// Before ABI 4 TCP cannot be restricted at all.
	plan, _ = landlockPlan(landlockPolicy{EgressPort: 4123}, 3, list)
	if plan.Net || len(plan.ConnectPorts) != 0 {
		t.Fatalf("ABI 3: net %v ports %v", plan.Net, plan.ConnectPorts)
	}
}

func TestProxyEnvPointsEveryClientAtTheRelay(t *testing.T) {
	env := strings.Join(proxyEnv(4123), " ")
	for _, want := range []string{"HTTP_PROXY=http://127.0.0.1:4123", "https_proxy=http://127.0.0.1:4123", "ALL_PROXY=http://127.0.0.1:4123", "NO_PROXY="} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in %s", want, env)
		}
	}
}

// fakeBackend installs a probe result for the test.
func fakeBackend(t *testing.T, s backendState) {
	t.Helper()
	probeMu.Lock()
	old, oldDone := probeState, probeDone
	s.at = time.Now()
	probeState, probeDone = s, true
	probeMu.Unlock()
	t.Cleanup(func() {
		probeMu.Lock()
		probeState, probeDone = old, oldDone
		probeMu.Unlock()
	})
}

func TestProxyFailsClosedWhereTheBackendCannotConfineTCP(t *testing.T) {
	exe, _ := os.Executable()
	fakeBackend(t, backendState{name: "landlock", path: exe, abi: 3})
	p := Policy{Mode: ModeRequired, Egress: true, EgressAllow: []string{"example.com"}}
	cmd := exec.Command("true")
	if ok, err := Wrap(cmd, p); ok || !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("required on ABI 3: ok %v err %v", ok, err)
	}
	// Auto degrades to network deny: the command still runs under the sandbox,
	// and gets no proxy variables, since nothing can reach the proxy.
	p.Mode = ModeAuto
	cmd = exec.Command("true")
	cmd.Env = []string{"A=1"}
	ok, err := Wrap(cmd, p)
	if err != nil || !ok {
		t.Fatalf("auto on ABI 3: ok %v err %v", ok, err)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "HTTP_PROXY=") || strings.HasPrefix(e, "https_proxy=") {
			t.Fatalf("a degraded command must not be pointed at a proxy: %v", cmd.Env)
		}
	}
}

func TestProxyFailsClosedWhenTheSocketCannotBeMade(t *testing.T) {
	fakeBackend(t, backendState{name: "bwrap", path: "/usr/bin/bwrap"})
	reg := egress.NewRegistry()
	reg.SetDir(filepath.Join(t.TempDir(), strings.Repeat("d", 120)))
	old := egress.Default
	egress.Default = reg
	t.Cleanup(func() { egress.Default = old })
	p := Policy{Mode: ModeRequired, Egress: true}
	if ok, err := Wrap(exec.Command("true"), p); ok || !errors.Is(err, ErrEgressUnavailable) {
		t.Fatalf("a socket path too long for a unix socket: ok %v err %v", ok, err)
	}
	p.Mode = ModeAuto
	cmd := exec.Command("true")
	if ok, err := Wrap(cmd, p); !ok || err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--unshare-net") {
		t.Fatalf("auto must fall back to network deny: ok %v err %v args %v", ok, err, cmd.Args)
	}
}

type fakeResolver map[string][]netip.Addr

func (f fakeResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, ok := f[host]; ok {
		return a, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

// TestSandboxedCommandReachesOnlyAllowedHosts runs a real command under the
// backend of this machine with network proxy: an allowed name reaches the
// (local, stand-in) origin through the proxy, a name outside the list gets a
// 403, a name that resolves to a loopback address is refused, and a direct
// connection that bypasses the proxy finds no network.
func TestSandboxedCommandReachesOnlyAllowedHosts(t *testing.T) {
	if Nested() {
		t.Skip("already inside a Belai sandbox")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	for _, backend := range []string{"native", "landlock"} {
		t.Run(backend, func(t *testing.T) {
			if backend == "landlock" {
				if abi := withLandlock(t); abi < landlockNetABI {
					t.Skipf("Landlock ABI %d cannot restrict TCP", abi)
				}
			} else if name, _ := Backend(); name == "" || name == "landlock" && !NetworkDenyEnforced() {
				t.Skip("no backend that confines TCP here")
			}
			runProxyScenario(t, curl)
		})
	}
}

func runProxyScenario(t *testing.T, curl string) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "origin saw host=%s path=%s", r.Host, r.URL.Path)
	}))
	t.Cleanup(origin.Close)

	dir, err := os.MkdirTemp("", "egt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	reg := egress.NewRegistry()
	reg.SetDir(dir)
	public := netip.MustParseAddr("93.184.216.34")
	reg.SetNetwork(fakeResolver{
		"allowed.test":  {public},
		"sub.wild.test": {public},
		"rebind.test":   {netip.MustParseAddr("127.0.0.1")},
	}, func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, origin.Listener.Addr().String())
	})
	old := egress.Default
	egress.Default = reg
	t.Cleanup(func() { egress.Default = old; reg.Shutdown() })

	root := t.TempDir()
	p := Policy{
		Mode: ModeRequired, Egress: true, Writable: []string{root}, Hidden: []string{dir},
		EgressAllow: []string{"allowed.test", "*.wild.test", "rebind.test"},
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(curl, args...)
		cmd.Dir = root
		if ok, err := Wrap(cmd, p); !ok || err != nil {
			t.Fatalf("wrap: %v %v", ok, err)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	code := func(url string, extra ...string) string {
		out, _ := run(append([]string{"-s", "-m", "10", "-o", "/dev/null", "-w", "%{http_code}", url}, extra...)...)
		return strings.TrimSpace(out)
	}

	if out, err := run("-s", "-m", "10", "http://allowed.test/x"); err != nil || out != "origin saw host=allowed.test path=/x" {
		t.Fatalf("plain http through the proxy: %v %q", err, out)
	}
	if out, err := run("-s", "-m", "10", "-p", "http://allowed.test/tun"); err != nil || out != "origin saw host=allowed.test path=/tun" {
		t.Fatalf("a CONNECT tunnel through the proxy: %v %q", err, out)
	}
	if got := code("http://sub.wild.test/"); got != "200" {
		t.Errorf("a wildcard entry covers a subdomain: %s", got)
	}
	if got := code("http://blocked.test/"); got != "403" {
		t.Errorf("a host outside the allowlist: %s", got)
	}
	if got := code("http://wild.test/"); got != "403" {
		t.Errorf("the wildcard must not cover its apex: %s", got)
	}
	if got := code("http://allowed.test:8080/"); got != "403" {
		t.Errorf("a port outside 80 and 443: %s", got)
	}
	if got := code("http://rebind.test/"); got != "403" {
		t.Errorf("a name that resolves to loopback: %s", got)
	}
	// Bypassing the proxy finds a closed network.
	if out, err := run("-s", "-m", "3", "--noproxy", "*", origin.URL); err == nil {
		t.Fatalf("a direct connection reached %s: %q", origin.URL, out)
	}
	// The state directory stays hidden: the command sees the socket and nothing else.
	cmd := exec.Command("ls", dir)
	if ok, err := Wrap(cmd, p); !ok || err != nil {
		t.Fatal(err)
	}
	if name, _ := Backend(); name == "bwrap" {
		out, _ := cmd.CombinedOutput()
		if n := len(strings.Fields(string(out))); n != 1 {
			t.Errorf("the command sees %d entries in the state directory: %q", n, out)
		}
	}
	if len(reg.Denials()) == 0 {
		t.Error("denials were not recorded")
	}
	for _, d := range reg.Denials() {
		if strings.Contains(d.Host, "\n") {
			t.Errorf("denied host not cleaned: %q", d.Host)
		}
	}
}

func TestConfigNetworkValuesAgreeWithTheConstants(t *testing.T) {
	if NetworkProxy != config.SandboxNetworkProxy || NetworkDeny != config.SandboxNetworkDeny || NetworkAllow != config.SandboxNetworkAllow {
		t.Fatal("sandbox and config disagree on the network values")
	}
}
