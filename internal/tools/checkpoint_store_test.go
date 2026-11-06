package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// The checkpoint store holds copies of the user's files for /undo and /rewind.
// Even when the working directory contains the state directory (a session
// started in the home directory), no tool may read or write it.
func TestNoToolCanUseTheCheckpointStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", filepath.Join(home, ".vulnetix", "belai"))
	dir, err := config.CheckpointsDir(home, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(dir, "blobs", "abc")
	if err := os.WriteFile(blob, []byte("COPY-OF-A-FILE"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An ordinary file beside it still works, so the refusal is about the store.
	ok := filepath.Join(home, "ok.txt")
	if err := os.WriteFile(ok, []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := Default(home, false)
	ctx := context.Background()
	run := func(name string, args map[string]any) (Result, error) {
		tool, found := reg.Find(name)
		if !found {
			t.Fatalf("no %s tool", name)
		}
		return tool.Execute(ctx, args)
	}

	if res, err := run("Read", map[string]any{"file_path": ok}); err != nil || !strings.Contains(res.Content, "fine") {
		t.Fatalf("Read of an ordinary file: %q, %v", res.Content, err)
	}
	for _, p := range []string{blob, filepath.Join(".vulnetix", "belai", "checkpoints", filepath.Base(filepath.Dir(filepath.Dir(dir))), "s1", "blobs", "abc")} {
		res, err := run("Read", map[string]any{"file_path": p})
		if err == nil || strings.Contains(res.Content, "COPY-OF-A-FILE") {
			t.Fatalf("Read(%s) = %q, %v: the store was readable", p, res.Content, err)
		}
	}
	if _, err := run("Write", map[string]any{"file_path": filepath.Join(dir, "blobs", "new"), "content": "x"}); err == nil {
		t.Fatal("Write created a file in the store")
	}
	if _, err := os.Stat(filepath.Join(dir, "blobs", "new")); err == nil {
		t.Fatal("the store gained a file")
	}
	if _, err := run("Edit", map[string]any{"file_path": blob, "old_string": "COPY", "new_string": "EVIL"}); err == nil {
		t.Fatal("Edit changed a file in the store")
	}
	if b, _ := os.ReadFile(blob); string(b) != "COPY-OF-A-FILE" {
		t.Fatalf("the blob was changed: %q", b)
	}

	// A listing never names it, with or without fd.
	for _, fdLooked := range []bool{true, false} {
		g := &Glob{Root: home, MaxResults: 100, fdLooked: fdLooked}
		res, err := g.Execute(ctx, map[string]any{"pattern": "**/*"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Content, "checkpoints") || strings.Contains(res.Content, "abc") {
			t.Fatalf("Glob listed the store (fdLooked=%v): %s", fdLooked, res.Content)
		}
		if !strings.Contains(res.Content, "ok.txt") {
			t.Fatalf("Glob lost an ordinary file (fdLooked=%v): %s", fdLooked, res.Content)
		}
	}
}

// Checkpoints add no tool: nothing the model can call reaches the store, so the
// registry carries no checkpoint, undo or rewind tool.
func TestNoToolIsNamedForCheckpoints(t *testing.T) {
	for _, name := range Default(t.TempDir(), false).Names() {
		low := strings.ToLower(name)
		for _, bad := range []string{"checkpoint", "undo", "rewind", "restore"} {
			if strings.Contains(low, bad) {
				t.Errorf("tool %q names a checkpoint operation", name)
			}
		}
	}
}
