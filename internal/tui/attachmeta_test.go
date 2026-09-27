package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectMIMETypeByExtension(t *testing.T) {
	if got := detectMIMEType("README.md"); !strings.HasPrefix(got, "text/markdown") {
		t.Fatalf("README.md mime = %q, want text/markdown", got)
	}
	if got := detectMIMEType("data.json"); got != "application/json" {
		t.Fatalf("data.json mime = %q, want application/json", got)
	}
}

func TestDetectMIMETypeByContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noext")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectMIMEType(path)
	if got != "text/plain; charset=utf-8" {
		t.Fatalf("shell script mime = %q, want text/plain", got)
	}
}

func TestFileAttachmentMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := fileAttachmentMeta(path, "hello world")
	if m.FileSize != 11 {
		t.Fatalf("FileSize = %d, want 11", m.FileSize)
	}
	if m.Tokens == 0 {
		t.Fatal("Tokens should be non-zero")
	}
	if m.MIMEType != "text/plain; charset=utf-8" {
		t.Fatalf("MIMEType = %q, want text/plain", m.MIMEType)
	}
}

func TestDirAttachmentMeta(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("bb"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := dirAttachmentMeta(dir, "a.txt\nsub/\n")
	if m.FileCount != 2 {
		t.Fatalf("FileCount = %d, want 2", m.FileCount)
	}
	if m.TotalSize != 3 {
		t.Fatalf("TotalSize = %d, want 3", m.TotalSize)
	}
	if m.MIMEType != "directory" {
		t.Fatalf("MIMEType = %q, want directory", m.MIMEType)
	}
}

func TestDirSummaryEmpty(t *testing.T) {
	dir := t.TempDir()
	count, size := dirSummary(dir)
	if count != 0 || size != 0 {
		t.Fatalf("empty dir summary = (%d, %d), want (0, 0)", count, size)
	}
}

func TestAttachmentFileCountLabel(t *testing.T) {
	if got := attachmentFileCountLabel(1); got != "1 file" {
		t.Fatalf("label(1) = %q, want 1 file", got)
	}
	if got := attachmentFileCountLabel(3); got != "3 files" {
		t.Fatalf("label(3) = %q, want 3 files", got)
	}
}
