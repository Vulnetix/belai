// SubAgentLog reads the log of a supervised process by its handle. The log is
// a process's own arbitrary stdout/stderr, so results are kind "process" and
// classify before promotion.
package tools

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// ProcessLog is the seam the supervised-process manager exposes to
// SubAgentLog. Implementations live in internal/bgproc so that the tools
// package does not import the manager and create an import cycle.
type ProcessLog interface {
	// LogGrep returns matching lines from the process log, with optional
	// surrounding context lines. Lines are formatted as "id:lineno: text".
	LogGrep(id, pattern string, maxMatches, context int) (string, error)
	// LogIDs returns the IDs of processes currently known to the manager.
	LogIDs() []string
}

// SubAgentLog lets a recovery subagent (and the main agent) search the
// captured log of a supervised process.
type SubAgentLog struct {
	Logs ProcessLog
}

// Definition returns the static tool metadata.
func (s *SubAgentLog) Definition() Definition {
	return Definition{
		Name: "SubAgentLog",
		Description: "Search the captured log of a supervised process by its handle. " +
			"Returns matching lines with line numbers in the form \"p1:42: <text>\". " +
			"The log may be up to 16 MiB; streaming is used so only the reported lines are materialised.",
		Properties: map[string]Property{
			"process":     {Type: "string", Description: "The process handle, e.g. \"p1\""},
			"pattern":     {Type: "string", Description: "A RE2 regular expression to search for"},
			"max_matches": {Type: "integer", Description: "Maximum matches to return (default 100)"},
			"context":     {Type: "integer", Description: "Lines of context before and after each match (default 0)"},
		},
		Required: []string{"process", "pattern"},
	}
}

// Kind returns "process".
func (s *SubAgentLog) Kind() Kind { return KindProcess }

// Subject returns the process handle for permission/rule display.
func (s *SubAgentLog) Subject(args map[string]any) string {
	if id, ok := argString(args, "process"); ok {
		return id
	}
	return ""
}

// Mutates reports that SubAgentLog only reads.
func (s *SubAgentLog) Mutates() bool { return false }

const maxSubAgentLogPatternBytes = 1024

// Execute runs the regex search over the process log.
func (s *SubAgentLog) Execute(ctx context.Context, args map[string]any) (Result, error) {
	id, ok := argString(args, "process")
	if !ok || id == "" {
		return Result{}, fmt.Errorf("missing process argument")
	}
	pattern, ok := argString(args, "pattern")
	if !ok || pattern == "" {
		return Result{}, fmt.Errorf("missing pattern argument")
	}
	if len(pattern) > maxSubAgentLogPatternBytes {
		return Result{}, fmt.Errorf("pattern exceeds %d bytes", maxSubAgentLogPatternBytes)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return Result{}, fmt.Errorf("invalid regex: %w", err)
	}
	maxMatches := int64(100)
	if n, ok := argInt64(args, "max_matches"); ok && n > 0 {
		maxMatches = n
	}
	context := int64(0)
	if n, ok := argInt64(args, "context"); ok && n >= 0 {
		context = n
	}
	if s.Logs == nil {
		return Result{}, fmt.Errorf("process logging is not available")
	}
	out, err := s.Logs.LogGrep(id, pattern, int(maxMatches), int(context))
	if err != nil {
		return Result{}, err
	}
	const maxResultBytes = 64 * 1024
	if len(out) > maxResultBytes {
		cut := out[:maxResultBytes]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i]
		}
		out = cut + fmt.Sprintf("\n… truncated at %d bytes", maxResultBytes)
	}
	return Result{Kind: KindProcess, Content: out}, nil
}
