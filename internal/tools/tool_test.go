package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizePathConfined(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "foo.txt")
	_ = os.WriteFile(f, []byte("hello"), 0o600)

	rel, err := SanitizePath(root, "foo.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rel != "foo.txt" {
		t.Fatalf("got %q", rel)
	}
}

func TestSanitizePathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	_, err := SanitizePath(root, "../etc/passwd")
	if err == nil {
		t.Fatal("expected escape error")
	}
}

func TestSanitizePathRejectsNUL(t *testing.T) {
	root := t.TempDir()
	_, err := SanitizePath(root, "foo\x00.txt")
	if err == nil {
		t.Fatal("expected NUL error")
	}
}

func TestRegistryNamesAndDefinitions(t *testing.T) {
	r := NewRegistry(&Read{Root: "/tmp"})
	if len(r.Names()) != 1 || r.Names()[0] != "Read" {
		t.Fatalf("unexpected names: %v", r.Names())
	}
	defs := r.Definitions()
	if len(defs) != 1 || defs[0].Name != "Read" {
		t.Fatalf("unexpected defs")
	}
	found, ok := r.Find("Read")
	if !ok || found == nil {
		t.Fatal("expected to find Read")
	}
	_, ok = r.Find("Missing")
	if ok {
		t.Fatal("expected not to find Missing")
	}
}
