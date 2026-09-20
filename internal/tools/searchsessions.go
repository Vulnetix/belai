// SearchSessions recovers prior context by searching other agents' transcript
// stores. It returns attributed matches so the caller can cite the true
// source, and its documentation states that a match is a recollection, never
// a fact.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/agentstore"
)

// SearchSessions searches other agents' past sessions on this machine.
type SearchSessions struct {
	Store   AgentStore
	Project string
}

// Definition returns the static tool metadata.
func (s *SearchSessions) Definition() Definition {
	return Definition{
		Name: "SearchSessions",
		Description: "Search other coding agents' past sessions on this machine for a regular expression, to recover context already gathered instead of re-exploring. " +
			"Returns attributed matches — agent, session id, turn, role, timestamp, project, and path — with a short snippet. " +
			"By default only sessions recorded under the current project are searched; set all_projects to search every agent store, and prompts_only to search only the fast history.jsonl prompt indexes. " +
			"A match is a record of what some agent once wrote, not a fact about the current repository — confirm it against the live files before acting on it.",
		Properties: map[string]Property{
			"regex":        {Type: "string", Description: "The RE2 regular expression to search for (≤ 1 KiB)"},
			"agent":        {Type: "string", Description: "Optional registry agent name; omit to search all present agents"},
			"all_projects": {Type: "boolean", Description: "When true, search every agent store instead of only the current project"},
			"project":      {Type: "string", Description: "Optional substring matched against the recorded working directory"},
			"role":         {Type: "string", Description: `Optional role filter: "user" or "assistant"`},
			"since":        {Type: "string", Description: "Optional lower bound on session time: RFC3339 or a duration like 7d"},
			"until":        {Type: "string", Description: "Optional upper bound on session time: RFC3339 or a duration like 7d"},
			"prompts_only": {Type: "boolean", Description: "When true, search only history.jsonl prompt indexes"},
			"max_matches":  {Type: "integer", Description: "Maximum matches to return (default 50)"},
		},
		Required: []string{"regex"},
	}
}

// Kind returns "agent_store".
func (s *SearchSessions) Kind() Kind { return KindAgentStore }

// Subject returns the regex for display.
func (s *SearchSessions) Subject(args map[string]any) string {
	if v, ok := argString(args, "regex"); ok {
		return v
	}
	return ""
}

// Mutates reports that SearchSessions only reads.
func (s *SearchSessions) Mutates() bool { return false }

// Execute searches and formats the attributed result.
func (s *SearchSessions) Execute(ctx context.Context, args map[string]any) (Result, error) {
	re, err := compileAgentStoreRegex(args, "regex")
	if err != nil {
		return Result{}, err
	}
	if s.Store == nil {
		return Result{}, fmt.Errorf("agent-store search is not available")
	}

	q := agentstore.SessionQuery{Re: re}
	if v, ok := argString(args, "agent"); ok && v != "" {
		if !agentstore.HasAgent(v) {
			return Result{}, fmt.Errorf("unknown agent %q", v)
		}
		q.Agent = v
	}
	q.AllProjects, _ = argBool(args, "all_projects")
	if v, ok := argString(args, "project"); ok {
		q.Project = v
	}
	if v, ok := argString(args, "role"); ok && v != "" {
		role := strings.ToLower(v)
		if role != "user" && role != "assistant" {
			return Result{}, fmt.Errorf("role must be \"user\" or \"assistant\"")
		}
		q.Role = role
	}
	if v, ok := argString(args, "since"); ok {
		if q.Since, err = parseWhen(v); err != nil {
			return Result{}, err
		}
	}
	if v, ok := argString(args, "until"); ok {
		if q.Until, err = parseWhen(v); err != nil {
			return Result{}, err
		}
	}
	q.PromptsOnly, _ = argBool(args, "prompts_only")
	q.MaxMatches = 50
	if n, ok := argInt64(args, "max_matches"); ok && n > 0 {
		q.MaxMatches = int(n)
	}

	res, err := s.Store.SearchSessions(ctx, q)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindAgentStore, Content: formatSessionSearch(res), Meta: map[string]any{"tool": "search_sessions"}}, nil
}

// formatSessionSearch renders the SearchSessions result body.
func formatSessionSearch(res agentstore.SessionSearch) string {
	var b strings.Builder

	var sources []string
	for _, sc := range res.Sources {
		sources = append(sources, fmt.Sprintf("%s(%d files)", sc.Agent, sc.Files))
	}
	if len(sources) > 0 {
		b.WriteString("sources: " + strings.Join(sources, " ") + "\n")
	}
	var skipped []string
	for _, sk := range res.Skipped {
		skipped = append(skipped, fmt.Sprintf("%s(%s)", sk.Agent, sk.Reason))
	}
	if len(skipped) > 0 {
		b.WriteString("skipped: " + strings.Join(skipped, " ") + "\n")
	}

	if len(res.Hits) == 0 {
		if len(sources)+len(skipped) == 0 {
			b.WriteString("no agent stores present\n")
		} else {
			b.WriteString("\nno matches\n")
		}
		return strings.TrimRight(b.String(), "\n")
	}

	b.WriteString("\n")
	for _, h := range res.Hits {
		project := ""
		if p := projectName(h.Project); p != "" {
			project = "  [" + p + "]"
		}
		b.WriteString(fmt.Sprintf("%s  %s  turn %d  %s  %s%s\n", h.Agent, shortID(h.SessionID), h.Turn, h.Role, formatWhen(h.At), project))
		b.WriteString("  " + h.Path + "\n")
		b.WriteString("  " + h.Snippet + "\n")
	}

	if res.Truncated || res.DeadlineHit {
		b.WriteString("\n… truncated: results were cut at a cap")
		if res.DeadlineHit {
			b.WriteString(" and the search deadline was reached")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
