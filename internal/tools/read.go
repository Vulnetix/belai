package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Read is the file-read tool.
type Read struct {
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

// Subject returns the permission-rule subject (the path argument).
func (r *Read) Subject(args map[string]any) string {
	if s, ok := args["path"].(string); ok {
		return s
	}
	return ""
}

// Execute reads the file, enforcing root confinement and size limits.
func (r *Read) Execute(ctx context.Context, args map[string]any) (Result, error) {
	pathArg, ok := args["path"].(string)
	if !ok || pathArg == "" {
		return Result{}, fmt.Errorf("missing path argument")
	}
	rel, err := SanitizePath(r.Root, pathArg)
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
	if v, ok := args["offset"]; ok {
		switch t := v.(type) {
		case float64:
			offset = int64(t)
		case int:
			offset = int64(t)
		case int64:
			offset = t
		case string:
			offset, _ = strconv.ParseInt(t, 10, 64)
		}
	}
	if offset > 0 {
		if _, err := f.Seek(offset, 0); err != nil {
			return Result{}, err
		}
	}

	limit := max
	if v, ok := args["limit"]; ok {
		switch t := v.(type) {
		case float64:
			limit = int64(t)
		case int:
			limit = int64(t)
		case int64:
			limit = t
		case string:
			limit, _ = strconv.ParseInt(t, 10, 64)
		}
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

	return ReadResult(string(buf[:n])), nil
}
