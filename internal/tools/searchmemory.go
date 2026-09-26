// SearchMemory searches other agents' memory and rules files (AGENTS.md,
// CLAUDE.md, cursor rules, and agent memory directories) across the registry.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agentstore"
)

// SearchMemory searches memory/rules files across the registered agents.
type SearchMemory struct {
	Store AgentStore
}

// Definition returns the static tool metadata.
func (s *SearchMemory) Definition() Definition {
	return Definition{
		Name: "SearchMemory",
		Description: "Search memory and rules files (AGENTS.md, CLAUDE.md, cursor rules, and per-agent memory directories) across the registered agents for a regular expression. " +
			"Without file, returns path:line: text hits with surrounding context lines. " +
			"With file (exactly one path from the probed memory set), returns that whole file capped at 64 KiB. " +
			"A match is a record of what was written, not a fact about the current repository — confirm it against the live files before acting on it.",
		Properties: map[string]Property{
			"regex":         {Type: "string", Description: "The RE2 regular expression to search for (≤ 1 KiB)"},
			"agent":         {Type: "string", Description: "Optional registry agent name; omit to search all agents' memory"},
			"file":          {Type: "string", Description: "Optional exact path from the probed memory set; when given, returns that whole file"},
			"context_lines": {Type: "integer", Description: "Lines of context around each match (default 2, max 20)"},
			"max_matches":   {Type: "integer", Description: "Maximum matches to return (default 50)"},
		},
		Required: []string{"regex"},
	}
}

// Kind returns "agent_store".
func (s *SearchMemory) Kind() Kind { return KindAgentStore }

// Subject returns the regex for display.
func (s *SearchMemory) Subject(args map[string]any) string {
	if v, ok := argString(args, "regex"); ok {
		return v
	}
	return ""
}

// Mutates reports that SearchMemory only reads.
func (s *SearchMemory) Mutates() bool { return false }

// Execute searches memory files and formats the result.
func (s *SearchMemory) Execute(ctx context.Context, args map[string]any) (Result, error) {
	re, err := compileAgentStoreRegex(args, "regex")
	if err != nil {
		return Result{}, err
	}
	if s.Store == nil {
		return Result{}, fmt.Errorf("agent-store search is not available")
	}

	q := agentstore.MemoryQuery{Re: re}
	if v, ok := argString(args, "agent"); ok && v != "" {
		if !agentstore.HasAgent(v) {
			return Result{}, fmt.Errorf("unknown agent %q", v)
		}
		q.Agent = v
	}
	if v, ok := argString(args, "file"); ok && v != "" {
		q.File = v
	}
	q.ContextLines = 2
	if n, ok := argInt64(args, "context_lines"); ok {
		if n < 0 || n > 20 {
			return Result{}, fmt.Errorf("context_lines must be between 0 and 20")
		}
		q.ContextLines = int(n)
	}
	q.MaxMatches = 50
	if n, ok := argInt64(args, "max_matches"); ok && n > 0 {
		q.MaxMatches = int(n)
	}

	res, err := s.Store.SearchMemory(ctx, q)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindAgentStore, Content: formatMemorySearch(res), Meta: map[string]any{"tool": "search_memory"}}, nil
}

// formatMemorySearch renders the SearchMemory result body.
func formatMemorySearch(res agentstore.MemorySearch) string {
	if res.Content != "" || (res.Files != nil && len(res.Files) == 1 && len(res.Hits) == 0 && !res.Truncated) {
		out := res.Content
		if res.Truncated {
			out += "\n… truncated at 64 KiB"
		}
		return strings.TrimRight(out, "\n")
	}
	var b strings.Builder
	if len(res.Hits) == 0 {
		if len(res.Files) == 0 {
			b.WriteString("no memory files present\n")
		} else {
			b.WriteString("no matches\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}
	for _, h := range res.Hits {
		b.WriteString(fmt.Sprintf("%s:%d: %s\n", h.Path, h.Line, h.Text))
	}
	if res.Truncated {
		b.WriteString("… truncated at the match cap\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
