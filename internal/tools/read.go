package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/alecthomas/chroma/v2/lexers"
)

// Read is the file-read tool.
type Read struct {
	Cwd      *Cwd
	Root     string
	MaxBytes int64
}

// Definition returns the static tool metadata.
func (r *Read) Definition() Definition {
	return Definition{
		Name:        "Read",
		Description: "Read the contents of a file at a given path.",
		Properties: map[string]Property{
			"path":   {Type: "string", Description: "Relative path to the file"},
			"offset": {Type: "integer", Description: "Optional byte offset to start reading"},
			"limit":  {Type: "integer", Description: "Optional maximum bytes to read"},
		},
		Required: []string{"path"},
	}
}

// Kind returns the tool kind.
func (r *Read) Kind() Kind { return KindRead }

// Subject returns the permission-rule subject: the path argument resolved
// through the working directory, so a rule keeps matching after a move.
func (r *Read) Subject(args map[string]any) string {
	if s, ok := args["path"].(string); ok {
		return subjectPath(r.Root, r.Cwd, s)
	}
	return ""
}

// Execute reads the file, enforcing root confinement and size limits.
func (r *Read) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := args["path"].(string)
	if !ok || pathArg == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	rel, err := resolvePath(r.Root, r.Cwd, pathArg)
	if err != nil {
		return Result{}, err
	}
	full := filepath.Join(r.Root, rel)

	f, err := os.Open(full)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Result{}, err
	}
	if info.IsDir() {
		return Result{}, fmt.Errorf("path is a directory")
	}

	max := r.MaxBytes
	if max <= 0 {
		max = 64 * 1024 // 64 KiB default
	}

	offset := int64(0)
	if v, ok := argInt64(args, "offset"); ok {
		offset = v
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return Result{}, err
		}
	}

	limit := max
	if v, ok := argInt64(args, "limit"); ok {
		limit = v
		if limit > max {
			limit = max
		}
	}

	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if err != nil {
		return Result{}, err
	}

	// Reject binaries that contain NUL bytes.
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return Result{}, fmt.Errorf("binary file (contains NUL bytes)")
		}
	}

	// Compute start_line for partial reads so the TUI can number them.
	startLine := 1
	if offset > 0 && offset <= 1024*1024 {
		prefix := make([]byte, offset)
		if pn, err := f.ReadAt(prefix, 0); err == nil || err == io.EOF || err == io.ErrUnexpectedEOF {
			startLine = bytes.Count(prefix[:pn], []byte{'\n'}) + 1
		}
	}

	meta := map[string]any{"path": rel}
	if startLine > 1 {
		meta["start_line"] = startLine
	}
	if l := lexers.Match(filepath.Base(rel)); l != nil {
		meta["lang"] = l.Config().Name
	}

	return ReadResultMeta(string(buf[:n]), meta), nil
}
