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
		Name: "Read",
		Description: "Read the contents of a text file under the working directory. " +
			"Returns the file's bytes verbatim, with no line numbers added. " +
			"The path is confined to the working directory: a path escaping it, a directory, or a binary file (one containing a NUL byte) is an error rather than a partial answer. " +
			"Reads are bounded (64 KiB by default); a larger file comes back truncated, so page through it with offset and limit. " +
			"Read a file before editing it — Edit matches exact bytes and will fail on a guess.",
		Properties: map[string]Property{
			"file_path": {Type: "string", Description: "Path to the file: an absolute filesystem path under one of the session roots, or relative to the working directory; a leading `/` not under any root is relative to the session root"},
			"offset":    {Type: "integer", Description: "Optional byte (not line) offset to start reading from; omit to start at the beginning"},
			"limit":     {Type: "integer", Description: "Optional maximum number of bytes to read; values above the tool's own cap are clamped to it"},
		},
		Required: []string{"file_path"},
	}
}

// Kind returns the tool kind.
func (r *Read) Kind() Kind { return KindRead }

// Subject returns the permission-rule subject: the path argument resolved
// through the working directory, so a rule keeps matching after a move.
func (r *Read) Subject(args map[string]any) string {
	if s, ok := argString(args, "file_path"); ok {
		return subjectPath(r.Root, r.Cwd, s)
	}
	return ""
}

// Execute reads the file, enforcing root confinement and size limits.
func (r *Read) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := argString(args, "file_path")
	if !ok || pathArg == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	res, err := resolvePath(r.Root, r.Cwd, pathArg)
	if err != nil {
		return Result{}, err
	}
	full := res.Abs()

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

	meta := map[string]any{"path": res.Rel}
	if startLine > 1 {
		meta["start_line"] = startLine
	}
	if l := lexers.Match(filepath.Base(res.Rel)); l != nil {
		meta["lang"] = l.Config().Name
	}

	return ReadResultMeta(string(buf[:n]), meta), nil
}
