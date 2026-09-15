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
