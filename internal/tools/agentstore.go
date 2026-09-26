// Agent-store search tools. The three tools (SearchSessions, ReadSession,
// SearchMemory) read other agents' transcript and memory stores outside the
// confinement root set, through the static registry in internal/agentstore.
// They take no path argument: every path comes from that registry, so they
// cannot be used as a general read primitive. Results are kind agent_store
// and classify before promotion.
package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agentstore"
)

// AgentStore is the seam the three agent-store tools use to reach the
// registry. *agentstore.Registry implements it; tests inject fakes.
type AgentStore interface {
	SearchSessions(ctx context.Context, q agentstore.SessionQuery) (agentstore.SessionSearch, error)
	ReadSession(ctx context.Context, req agentstore.ReadRequest) (agentstore.SessionRead, error)
	SearchMemory(ctx context.Context, q agentstore.MemoryQuery) (agentstore.MemorySearch, error)
}

// NewAgentStoreTools builds the three read-only agent-store tools sharing one
// store. project is the session working directory, used as the default project
// scope of SearchSessions.
func NewAgentStoreTools(store AgentStore, project string) []Tool {
	return []Tool{
		&SearchSessions{Store: store, Project: project},
		&ReadSession{Store: store},
		&SearchMemory{Store: store},
	}
}

const maxAgentStorePatternBytes = 1024

// compileAgentStoreRegex validates and compiles the required regex argument.
func compileAgentStoreRegex(args map[string]any, key string) (*regexp.Regexp, error) {
	pattern, ok := argString(args, key)
	if !ok || pattern == "" {
		return nil, fmt.Errorf("missing %s argument", key)
	}
	if len(pattern) > maxAgentStorePatternBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", key, maxAgentStorePatternBytes)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex: %w", err)
	}
	return re, nil
}

// parseWhen parses a since/until argument: RFC3339, or a relative duration
// like "7d" measured back from now.
func parseWhen(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if d, err := parseRelativeDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q (want RFC3339 or a duration like 7d)", s)
}

// parseRelativeDuration parses a compact duration like "7d", "24h", "30m".
func parseRelativeDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("no number")
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(s[i:])
	var mult time.Duration
	switch unit {
	case "s":
		mult = time.Second
	case "m":
		mult = time.Minute
	case "h":
		mult = time.Hour
	case "d":
		mult = 24 * time.Hour
	case "w":
		mult = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	return time.Duration(n) * mult, nil
}

// shortID renders a session id as its first 8 runes plus an ellipsis.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8] + "…"
}

// formatWhen renders a hit timestamp for the result body.
func formatWhen(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04Z")
}

// projectName returns the basename of a recorded working directory.
func projectName(project string) string {
	if project == "" {
		return ""
	}
	if i := strings.LastIndexByte(project, '/'); i >= 0 && i < len(project)-1 {
		return project[i+1:]
	}
	return project
}
