package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/offload"
)

// ReadResultName is the tool's name.
const ReadResultName = "ReadResult"

// readResultMaxChars bounds one ReadResult slice, about the size of a default
// offload preview's threshold, so reading back never re-floods the context.
const readResultMaxChars = 16 * 1024

// readResultMaxPattern bounds the pattern argument.
const readResultMaxPattern = 1024

// ReadResultTool is the ReadResult tool: it reads part of a tool result the harness offloaded. It is
// path-free: it takes the reference the preview named, and a reference
// resolves only inside this session's store, so it cannot be used to read
// anything else. Its result is KindOffload, which classifies.
//
// The argument shape reuses Read's offset/limit and Grep's pattern, which
// models are trained on; there is no established tool for offloaded output,
// so the name is new.
type ReadResultTool struct {
	Store *offload.Store
}

// Definition returns the static tool metadata.
func (ReadResultTool) Definition() Definition {
	return Definition{
		Name: ReadResultName,
		Description: "Read part of a tool result that was too large to keep in the conversation. " +
			"When a result is offloaded you see only its head and tail, ending with a line naming a ref such as r3; pass that ref here. " +
			"With pattern (RE2), returns the matching lines with context; otherwise returns limit lines starting at the 1-based offset. " +
			"Lines are numbered like Read. Output is bounded, so narrow the pattern or page with offset rather than asking for everything.",
		Properties: map[string]Property{
			"ref":     {Type: "string", Description: "The reference from the offloaded result's last line, e.g. r3"},
			"pattern": {Type: "string", Description: "Optional RE2 regular expression; returns matching lines with context"},
			"context": {Type: "integer", Description: "Lines of context around each pattern match (default 3, at most 20)"},
			"offset":  {Type: "integer", Description: "1-based line to start from when no pattern is given (default 1)"},
			"limit":   {Type: "integer", Description: "Number of lines to return when no pattern is given (default 200)"},
		},
		Required: []string{"ref"},
	}
}

// Kind returns KindOffload.
func (ReadResultTool) Kind() Kind { return KindOffload }

// Subject returns the reference.
func (ReadResultTool) Subject(args map[string]any) string {
	v, _ := argString(args, "ref")
	return v
}

// Mutates reports false.
func (ReadResultTool) Mutates() bool { return false }

// Execute returns the requested slice.
func (t ReadResultTool) Execute(ctx context.Context, args map[string]any) (Result, error) {
	ref, _ := argString(args, "ref")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Result{}, fmt.Errorf("ref is required: the reference named at the end of the offloaded result")
	}
	content, ok := t.Store.Get(ref)
	if !ok {
		return Result{}, fmt.Errorf("offloaded result %q is not available in this session; re-run the tool if you still need it", ref)
	}
	var re *regexp.Regexp
	if p, ok := argString(args, "pattern"); ok && p != "" {
		if len(p) > readResultMaxPattern {
			return Result{}, fmt.Errorf("pattern exceeds %d bytes", readResultMaxPattern)
		}
		var err error
		if re, err = regexp.Compile(p); err != nil {
			return Result{}, fmt.Errorf("invalid pattern: %w", err)
		}
	}
	contextLines := 3
	if n, ok := argInt64(args, "context"); ok && n >= 0 {
		contextLines = int(min(n, 20))
	}
	offset := 1
	if n, ok := argInt64(args, "offset"); ok && n > 0 {
		offset = int(n)
	}
	limit := 200
	if n, ok := argInt64(args, "limit"); ok && n > 0 {
		limit = int(n)
	}
	return Result{Kind: KindOffload, Content: offload.Slice(content, re, offset, limit, contextLines, readResultMaxChars)}, nil
}
