package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/sandbox"
)

// Bash honours the sandbox policy on its context: a write outside the roots
// fails and the result carries the harness's sandbox note.
func TestBashRunsInsideSandbox(t *testing.T) {
	if name, _ := sandbox.Backend(); name != "bwrap" {
		t.Skip("bwrap not usable here")
	}
	root, outside := t.TempDir(), t.TempDir()
	b := &Bash{Root: root, Timeout: BashDefaultTimeout}
	ctx := sandbox.WithPolicy(context.Background(), sandbox.Policy{Mode: sandbox.ModeAuto, DenyNetwork: true, Writable: []string{root}})
	res, err := b.Execute(ctx, map[string]any{"command": "echo x > " + filepath.Join(outside, "f")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Fatal("sandboxed Bash wrote outside the roots")
	}
	if !strings.Contains(res.Content, "ran inside the Belai sandbox") || !strings.Contains(res.Content, "network is off") {
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
