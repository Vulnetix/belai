package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareCopiesAndVerifies(t *testing.T) {
	src := filepath.Join(t.TempDir(), "cached.bin")
	_ = os.WriteFile(src, []byte("model"), 0o600)
	out := filepath.Join(t.TempDir(), "assets")
	verify := func(p string) error {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != "model" {
			return errors.New("bad")
		}
		return nil
	}
	calls := 0
	fetch := func() (string, error) { calls++; return src, nil }

	dest, err := prepare(out, "m.bin", verify, fetch)
	if err != nil || calls != 1 {
		t.Fatalf("first prepare: %v, %d fetches", err, calls)
	}
	if b, _ := os.ReadFile(dest); string(b) != "model" {
		t.Fatalf("dest holds %q", b)
	}
	// A verified file already there is not fetched again.
	if _, err := prepare(out, "m.bin", verify, fetch); err != nil || calls != 1 {
		t.Fatalf("second prepare: %v, %d fetches", err, calls)
	}
	// A corrupted file is replaced.
	_ = os.WriteFile(dest, []byte("corrupt"), 0o600)
	if _, err := prepare(out, "m.bin", verify, fetch); err != nil || calls != 2 {
		t.Fatalf("repair prepare: %v, %d fetches", err, calls)
	}
}

func TestPrepareFailures(t *testing.T) {
	out := filepath.Join(t.TempDir(), "assets")
	never := func(string) error { return errors.New("bad") }
	boom := errors.New("network down")
	if _, err := prepare(out, "m.bin", never, func() (string, error) { return "", boom }); !errors.Is(err, boom) {
		t.Fatalf("fetch failure: %v", err)
	}
	src := filepath.Join(t.TempDir(), "x.bin")
	_ = os.WriteFile(src, []byte("x"), 0o600)
	if _, err := prepare(out, "m.bin", never, func() (string, error) { return src, nil }); err == nil {
		t.Fatal("a copy that fails verification was accepted")
	}
	if _, err := os.Stat(filepath.Join(out, "m.bin")); err == nil {
		t.Fatal("a failed copy was left in place")
	}
	if _, err := prepare(out, "m.bin", never, func() (string, error) { return filepath.Join(out, "missing"), nil }); err == nil {
		t.Fatal("a missing source was accepted")
	}
}

func TestRepoRootFindsTheModule(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repoRoot = %q has no go.mod", root)
	}
}
