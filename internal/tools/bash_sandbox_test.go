package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/egress"
	"github.com/vulnetix/belai/internal/sandbox"
)

// Bash honours the sandbox policy on its context: a write outside the roots
// fails and the result carries the harness's sandbox note.
func TestBashRunsInsideSandbox(t *testing.T) {
	if name, _ := sandbox.Backend(); name != "bwrap" && name != "landlock" {
		t.Skip("no Linux sandbox backend usable here")
	}
	// The outside directory is under the home directory, which every backend
	// keeps read-only: under Landlock the host's /tmp is writable by design,
	// so a second t.TempDir() is not outside the roots there.
	root, outside := t.TempDir(), homeTemp(t)
	b := &Bash{Root: root, Timeout: BashDefaultTimeout}
	ctx := sandbox.WithPolicy(context.Background(), sandbox.Policy{Mode: sandbox.ModeAuto, DenyNetwork: true, Writable: []string{root}})
	res, err := b.Execute(ctx, map[string]any{"command": "echo x > " + filepath.Join(outside, "f")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("sandboxed Bash wrote outside the roots")
	}
	if !strings.Contains(res.Content, "ran inside the Belai sandbox") || strings.Contains(res.Content, "network is off") != sandbox.NetworkDenyEnforced() {
		t.Fatalf("result = %q", res.Content)
	}
	if _, err := b.Execute(ctx, map[string]any{"command": "echo ok > inside"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "inside")); err != nil {
		t.Fatal("sandboxed Bash could not write inside the root")
	}
}

// Required mode with no backend refuses rather than running bare.
func TestBashRequiredSandboxRefusesWithoutBackend(t *testing.T) {
	if name, _ := sandbox.Backend(); name != "" {
		t.Skip("a backend exists here")
	}
	b := &Bash{Root: t.TempDir(), Timeout: BashDefaultTimeout}
	ctx := sandbox.WithPolicy(context.Background(), sandbox.Policy{Mode: sandbox.ModeRequired})
	if _, err := b.Execute(ctx, map[string]any{"command": "true"}); err == nil {
		t.Fatal("required sandbox ran without a backend")
	}
}

// homeTemp makes a directory under the home directory, which the sandbox keeps
// read-only. /tmp will not do: under Landlock the host's /tmp is writable.
func homeTemp(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	dir, err := os.MkdirTemp(home, ".belai-sandbox-test-")
	if err != nil {
		t.Skipf("the home directory is not writable: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// With network proxy, a host outside the allowlist is refused by the harness's
// egress proxy and the result says so in the harness's words, with the host
// cleaned and no suggestion to route around it.
func TestBashReportsWhatTheEgressProxyRefused(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	if name, _ := sandbox.Backend(); (name != "bwrap" && name != "landlock") || !sandbox.NetworkDenyEnforced() {
		t.Skip("no Linux sandbox backend that confines TCP here")
	}
	dir, err := os.MkdirTemp("", "egb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	reg := egress.NewRegistry()
	reg.SetDir(dir)
	old := egress.Default
	egress.Default = reg
	t.Cleanup(func() { egress.Default = old; reg.Shutdown() })

	root := t.TempDir()
	b := &Bash{Root: root, Timeout: BashDefaultTimeout}
	ctx := sandbox.WithPolicy(context.Background(), sandbox.Policy{
		Mode: sandbox.ModeRequired, Egress: true, EgressAllow: []string{"allowed.test"}, Writable: []string{root},
	})
	res, err := b.Execute(ctx, map[string]any{"command": curl + " -s -m 5 -o /dev/null http://blocked.test/x; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exit status 3", "the network reaches only the hosts the user allowed", "the egress proxy refused: blocked.test:80 (not_allowed)", "/egress allow HOST"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("result lacks %q:\n%s", want, res.Content)
		}
	}
	if res.Kind != KindBash {
		t.Errorf("kind %v: the note must ride on a classified Bash result", res.Kind)
	}
}

// Every place that starts a sandboxed command goes through sandbox.Wrap, which
// is where the egress proxy is started and the command confined to it, so a
// caller cannot run a command with the network settings of the policy ignored.
func TestEverySandboxedCallerGoesThroughWrap(t *testing.T) {
	for _, f := range []string{"bash.go", "../bgproc/manager.go", "../testrun/testrun.go", "../mcp/stdio.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), "sandbox.Wrap(") {
			t.Errorf("%s does not call sandbox.Wrap", f)
		}
	}
}
