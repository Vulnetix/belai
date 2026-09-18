package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxWriteBytes bounds a single Write or Edit content. It matches
// filediff.DefaultMaxBytes so a file too large to diff is also too large to
// write through the tool.
const MaxWriteBytes = 1 << 20

// Write is the file-write tool. It writes whole files; there is no truncation
// or partial-write mode.
type Write struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
}

// Definition returns the static tool metadata.
func (w *Write) Definition() Definition {
	return Definition{
		Name:        "Write",
		Description: "Write the full content of a file at a given relative path, creating parent directories as needed. The existing file, if any, is replaced atomically.",
		Properties: map[string]Property{
			"path":    {Type: "string", Description: "Relative path to the file to write"},
			"content": {Type: "string", Description: "The exact bytes to write"},
		},
		Required: []string{"path", "content"},
	}
}

// Kind returns the tool kind.
func (w *Write) Kind() Kind { return KindWrite }

// Mutates reports that Write always mutates the workspace.
func (w *Write) Mutates() bool { return true }

// Subject returns the permission-rule subject (the lexical cleaned path).
func (w *Write) Subject(args map[string]any) string {
	path, _ := argString(args, "path")
	return subjectPath(w.Root, w.Cwd, path)
}

// Targets returns the sanitised destination path for the diff recorder.
func (w *Write) Targets(args map[string]any) []string {
	path, _ := argString(args, "path")
	rel, err := resolveNewPath(w.Root, w.Cwd, path)
	if err != nil {
		return nil
	}
	return []string{rel}
}

// Preview returns the before/after contents for the approval diff without
// touching disk.
func (w *Write) Preview(args map[string]any) (path, old, new string, ok bool) {
	pathArg, ok := argString(args, "path")
	if !ok || strings.TrimSpace(pathArg) == "" {
		return "", "", "", false
	}
	content, ok := argString(args, "content")
	if !ok {
		return "", "", "", false
	}
	rel, err := resolveNewPath(w.Root, w.Cwd, pathArg)
	if err != nil {
		return "", "", "", false
	}
	old, _ = readExisting(filepath.Join(w.Root, rel), w.maxBytes())
	return rel, old, content, true
}

// Execute writes the file, enforcing root confinement, size limits, and an
// atomic write so a failure never leaves a half-written file.
func (w *Write) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := argString(args, "path")
	if !ok || strings.TrimSpace(pathArg) == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	content, ok := argString(args, "content")
	if !ok {
		return Result{}, fmt.Errorf("missing content argument")
	}
	if strings.Contains(content, "\x00") {
		return Result{}, fmt.Errorf("content contains NUL bytes")
	}
	max := w.maxBytes()
	if int64(len(content)) > max {
		return Result{}, fmt.Errorf("content exceeds %d bytes", max)
	}
	rel, err := resolveNewPath(w.Root, w.Cwd, pathArg)
	if err != nil {
		return Result{}, err
	}
	full := filepath.Join(w.Root, rel)
	if fi, err := os.Stat(full); err == nil && fi.IsDir() {
		return Result{}, fmt.Errorf("path is a directory")
	}
	if err := writeFileAtomic(full, []byte(content)); err != nil {
		return Result{}, err
	}
	lineWord := "lines"
	if countLines(content) == 1 {
		lineWord = "line"
	}
	return WriteResult(fmt.Sprintf("wrote %s (%d bytes, %d %s)", rel, len(content), countLines(content), lineWord)), nil
}

func (w *Write) maxBytes() int64 {
	if w.MaxBytes <= 0 {
		return MaxWriteBytes
	}
	return w.MaxBytes
}

// writeFileAtomic writes content via a temp file in the same directory,
// preserving the existing file's mode (else 0o644), then renames over the
// target so a failure never leaves a half-written file.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Lstat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".signet-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = "" // committed: do not remove
	return nil
}

// readExisting returns a file's contents capped at max, or "" when it is
// unreadable or absent.
func readExisting(path string, max int64) (string, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return "", false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(body), true
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}
