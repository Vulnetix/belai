package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/posture"
)

func TestRunnerResolvesAndExecutes(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "hook.sh"), []byte("#!/bin/sh\necho hook-ran\n"), 0o755)
	r := &Runner{Root: root}
	out, err := r.Run(context.Background(), Hook{Name: "x", Event: "post_tool", Command: "hook.sh"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out, "hook-ran") {
		t.Fatalf("out = %q", out)
	}
}

func TestRunnerRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "evil.sh"), []byte("#!/bin/sh\necho evil\n"), 0o755)
	if err := os.Symlink(filepath.Join(outside, "evil.sh"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	r := &Runner{Root: root}
	if _, err := r.Run(context.Background(), Hook{Name: "x", Command: "link"}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected symlink escape rejection, got %v", err)
	}
}

func TestLoadDirSkipsInvalid(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ok.json"), []byte(`{"name":"ok","event":"post_tool","command":"hook.sh"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"name":"bad","event":"nope","command":"x"}`), 0o600)
	hs, err := LoadDir(dir, posture.Defaults())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(hs) != 1 || hs[0].Name != "ok" {
		t.Fatalf("hooks = %+v, want only ok", hs)
	}
}
