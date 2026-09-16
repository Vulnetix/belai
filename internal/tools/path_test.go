package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeNewPathNewFileInExistingDir(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a"), 0o755)

	rel, err := SanitizeNewPath(root, "a/new.txt")
	if err != nil {
		t.Fatalf("SanitizeNewPath: %v", err)
	}
	if rel != filepath.Join("a", "new.txt") {
		t.Fatalf("got %q", rel)
	}
}

func TestSanitizeNewPathNewFileInMissingNestedDir(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "a"), 0o755)

	rel, err := SanitizeNewPath(root, "a/b/c.txt")
	if err != nil {
		t.Fatalf("SanitizeNewPath: %v", err)
	}
	if rel != filepath.Join("a", "b", "c.txt") {
		t.Fatalf("got %q", rel)
	}
}

func TestSanitizeNewPathRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := SanitizeNewPath(root, "../etc/passwd"); err == nil {
		t.Fatal("expected escape error")
	}
}

func TestSanitizeNewPathRejectsSymlinkedParentOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := SanitizeNewPath(root, "link/new.txt"); err == nil {
		t.Fatal("expected escape error for symlinked parent outside root")
	}
}

func TestSanitizeNewPathRejectsExistingSymlinkOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := SanitizeNewPath(root, "link"); err == nil {
		t.Fatal("expected escape error for symlink target outside root")
	}
}

func TestSanitizeNewPathRejectsNUL(t *testing.T) {
	root := t.TempDir()
	if _, err := SanitizeNewPath(root, "foo\x00.txt"); err == nil {
		t.Fatal("expected NUL error")
	}
}

func TestSubjectPathCleansIndirection(t *testing.T) {
	root := t.TempDir()
	for in, want := range map[string]string{
		"./a/../secrets/x": "secrets/x",
		"a/../secrets/x":   "secrets/x",
		"secrets/x":        "secrets/x",
		"./secrets/x":      "secrets/x",
	} {
		if got := SubjectPath(root, in); got != want {
			t.Fatalf("SubjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestArgBoolAcceptForms(t *testing.T) {
	for in, want := range map[any]bool{
		true: true, "true": true, "1": true, 1: true, 1.0: true, int64(1): true,
		false: false, "false": false, "0": false, 0: false, 0.0: false,
	} {
		got, ok := argBool(map[string]any{"v": in}, "v")
		if !ok || got != want {
			t.Fatalf("argBool(%v) = %v, %v; want %v", in, got, ok, want)
		}
	}
	if _, ok := argBool(map[string]any{"v": "yes"}, "v"); ok {
		t.Fatal("argBool(\"yes\") should be absent")
	}
	if _, ok := argBool(map[string]any{}, "v"); ok {
		t.Fatal("missing key should be absent")
	}
}

func TestArgInt64AcceptForms(t *testing.T) {
	args := map[string]any{"v": 42.0}
	if got, ok := argInt64(args, "v"); !ok || got != 42 {
		t.Fatalf("float64: %d, %v", got, ok)
	}
	args = map[string]any{"v": "42"}
	if got, ok := argInt64(args, "v"); !ok || got != 42 {
		t.Fatalf("string: %d, %v", got, ok)
	}
	args = map[string]any{"v": int(42)}
	if got, ok := argInt64(args, "v"); !ok || got != 42 {
		t.Fatalf("int: %d, %v", got, ok)
	}
	if _, ok := argInt64(map[string]any{"v": "nope"}, "v"); ok {
		t.Fatal("non-numeric string should be absent")
	}
}

func TestArgStringStrict(t *testing.T) {
	if got, ok := argString(map[string]any{"v": "x"}, "v"); !ok || got != "x" {
		t.Fatalf("string: %q, %v", got, ok)
	}
	if _, ok := argString(map[string]any{"v": 7}, "v"); ok {
		t.Fatal("non-string should be absent")
	}
}
