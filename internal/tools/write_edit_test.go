package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDefinitionAdvertisesFilePath(t *testing.T) {
	d := (&Write{}).Definition()
	if _, ok := d.Properties["file_path"]; !ok {
		t.Fatalf("Write schema does not advertise file_path: %+v", d.Properties)
	}
	if len(d.Required) != 2 || d.Required[0] != "file_path" {
		t.Fatalf("Write required = %v, want [file_path content]", d.Required)
	}
}

func TestWriteAcceptsPathAlias(t *testing.T) {
	root := t.TempDir()
	w := &Write{Root: root}
	if _, err := w.Execute(context.Background(), map[string]any{"path": "a.txt", "content": "x"}); err != nil {
		t.Fatalf("Write with path alias: %v", err)
	}
}

func TestWriteCreatesFile(t *testing.T) {
	root := t.TempDir()
	w := &Write{Root: root}
	res, err := w.Execute(context.Background(), map[string]any{
		"path":    "src/x.go",
		"content": "package x\n",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(res.Content, "wrote src/x.go (10 bytes, 1 line") {
		t.Fatalf("result = %q", res.Content)
	}
	body, err := os.ReadFile(filepath.Join(root, "src", "x.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "package x\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteOverwritesPreservingMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &Write{Root: root}
	if _, err := w.Execute(context.Background(), map[string]any{"path": "f.txt", "content": "new"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestWriteRejections(t *testing.T) {
	root := t.TempDir()
	w := &Write{Root: root, MaxBytes: 8}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing path", map[string]any{"content": "x"}, "missing path"},
		{"empty path", map[string]any{"path": "  ", "content": "x"}, "missing path"},
		{"missing content", map[string]any{"path": "a.txt"}, "missing content"},
		{"NUL content", map[string]any{"path": "a.txt", "content": "a\x00b"}, "NUL"},
		{"oversize", map[string]any{"path": "a.txt", "content": "123456789"}, "exceeds"},
		{"escape", map[string]any{"path": "../a.txt", "content": "x"}, "escapes root"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := w.Execute(context.Background(), c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want contains %q", err, c.want)
			}
		})
	}
}

func TestWriteDirectoryTarget(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "dir"), 0o755)
	w := &Write{Root: root}
	_, err := w.Execute(context.Background(), map[string]any{"path": "dir", "content": "x"})
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditDefinitionAdvertisesFilePath(t *testing.T) {
	d := (&Edit{}).Definition()
	if _, ok := d.Properties["file_path"]; !ok {
		t.Fatalf("Edit schema does not advertise file_path: %+v", d.Properties)
	}
	if len(d.Required) != 3 || d.Required[0] != "file_path" {
		t.Fatalf("Edit required = %v, want [file_path old_string new_string]", d.Required)
	}
}

func TestEditAcceptsPathAlias(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Edit{Root: root}
	if _, err := e.Execute(context.Background(), map[string]any{"path": "f.txt", "old_string": "a", "new_string": "b"}); err != nil {
		t.Fatalf("Edit with path alias: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "b" {
		t.Fatalf("body = %q", body)
	}
}

func TestEditReplacesUniqueOccurrence(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Edit{Root: root}
	res, err := e.Execute(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "world", "new_string": "there",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "edited f.txt (1 replacement)" {
		t.Fatalf("result = %q", res.Content)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "hello there\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestEditReplaceAll(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	if err := os.WriteFile(path, []byte("a a a"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Edit{Root: root}
	res, err := e.Execute(context.Background(), map[string]any{
		"path": "f.txt", "old_string": "a", "new_string": "b", "replace_all": true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Content != "edited f.txt (3 replacements)" {
		t.Fatalf("result = %q", res.Content)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "b b b" {
		t.Fatalf("body = %q", body)
	}
}

func TestEditRejectionsLeaveFileIntact(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	orig := []byte("a a a")
	if err := os.WriteFile(path, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Edit{Root: root}

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing old", map[string]any{"path": "f.txt", "new_string": "b"}, "old_string"},
		{"missing new", map[string]any{"path": "f.txt", "old_string": "a"}, "new_string"},
		{"identical", map[string]any{"path": "f.txt", "old_string": "a", "new_string": "a"}, "identical"},
		{"not found", map[string]any{"path": "f.txt", "old_string": "zzz", "new_string": "b"}, "not found"},
		{"multiple without replace_all", map[string]any{"path": "f.txt", "old_string": "a", "new_string": "b"}, "appears 3 times"},
		{"missing file", map[string]any{"path": "nope.txt", "old_string": "a", "new_string": "b"}, "no such file"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := e.Execute(context.Background(), c.args)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want contains %q", err, c.want)
			}
			body, _ := os.ReadFile(path)
			if string(body) != string(orig) {
				t.Fatalf("file changed on failure: %q", body)
			}
		})
	}
}

func TestEditRejectsBinaryFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.bin")
	if err := os.WriteFile(path, []byte("a\x00b"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Edit{Root: root}
	_, err := e.Execute(context.Background(), map[string]any{"path": "f.bin", "old_string": "a", "new_string": "b"})
	if err == nil || !strings.Contains(err.Error(), "binary file") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteEditSubjectsAreLexical(t *testing.T) {
	root := t.TempDir()
	w := &Write{Root: root}
	if got := w.Subject(map[string]any{"path": "./a/../secrets/x"}); got != "secrets/x" {
		t.Fatalf("Write.Subject = %q", got)
	}
	e := &Edit{Root: root}
	if got := e.Subject(map[string]any{"path": "./secrets/x"}); got != "secrets/x" {
		t.Fatalf("Edit.Subject = %q", got)
	}
}
