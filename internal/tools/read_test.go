package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "hello.txt")
	_ = os.WriteFile(f, []byte("world"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "world" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "data.txt")
	_ = os.WriteFile(f, []byte("abcdef"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": 2, "limit": 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Content != "cd" {
		t.Fatalf("got %q", res.Content)
	}
}

func TestReadBinaryRejection(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "bin.dat")
	_ = os.WriteFile(f, []byte{0x00, 0x01, 0x02}, 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "bin.dat"})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary rejection, got %v", err)
	}
}

func TestReadEscapeRejection(t *testing.T) {
	root := t.TempDir()
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "../escape.txt"})
	if err == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestReadMissingPath(t *testing.T) {
	root := t.TempDir()
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected missing path error")
	}
}

func TestReadDirectoryRejection(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "subdir"), 0o755)
	r := &Read{Root: root, MaxBytes: 1024}
	_, err := r.Execute(context.Background(), map[string]any{"path": "subdir"})
	if err == nil {
		t.Fatal("expected directory rejection")
	}
}

func TestReadMetaContainsPathAndLang(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "main.go")
	_ = os.WriteFile(f, []byte("package main\n"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Meta == nil {
		t.Fatal("expected Meta")
	}
	if res.Meta["path"] != "main.go" {
		t.Fatalf("path meta = %q", res.Meta["path"])
	}
	if res.Meta["lang"] != "Go" {
		t.Fatalf("lang meta = %q", res.Meta["lang"])
	}
}

func TestReadMetaStartLineForPartialRead(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "data.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	_ = os.WriteFile(f, []byte(content), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "data.txt", "offset": 12, "limit": 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Meta == nil {
		t.Fatal("expected Meta")
	}
	// offset 12 lands in "line3\n" (bytes 0-5 = line1\n, 6-11 = line2\n, 12-17 = line3\n)
	// start_line should be 3
	sl, ok := res.Meta["start_line"].(int)
	if !ok {
		t.Fatalf("start_line type = %T", res.Meta["start_line"])
	}
	if sl != 3 {
		t.Fatalf("start_line = %d, want 3", sl)
	}
	if res.Content != "line3" {
		t.Fatalf("content = %q, want line3", res.Content)
	}
}

func TestReadMetaOmitsStartLineAtZeroOffset(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "hello.txt")
	_ = os.WriteFile(f, []byte("world\n"), 0o600)

	r := &Read{Root: root, MaxBytes: 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "hello.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.Meta["start_line"]; ok {
		t.Fatal("start_line should not be present at offset 0")
	}
}

func TestReadMetaOmitsStartLineBeyondOneMiB(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "big.txt")
	var b strings.Builder
	for i := 0; i < 200_000; i++ {
		b.WriteString("this is a long line that will be repeated many times to exceed one mebibyte\n")
	}
	_ = os.WriteFile(f, []byte(b.String()), 0o600)

	r := &Read{Root: root, MaxBytes: 10 * 1024 * 1024}
	res, err := r.Execute(context.Background(), map[string]any{"path": "big.txt", "offset": 2 * 1024 * 1024})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res.Meta["start_line"]; ok {
		t.Fatal("start_line should not be present when offset > 1 MiB")
	}
}
