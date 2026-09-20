// ReadSession reads a turn range from one of the agent's past sessions, so a
// SearchSessions hit can be read in context without a full re-exploration.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/agentstore"
)

// ReadSession reads a turn range from another agent's stored session.
type ReadSession struct {
	Store AgentStore
}

// Definition returns the static tool metadata.
func (r *ReadSession) Definition() Definition {
	return Definition{
		Name: "ReadSession",
		Description: "Read a turn range from another agent's stored session by agent and session id (a unique prefix is enough), to see the surrounding context of a SearchSessions hit. " +
			"Returns the turns verbatim with role and timestamp, capped at 40 turns / 64 KiB. " +
			"A session is a record of what some agent once wrote, not a fact about the current repository — confirm it against the live files before acting on it.",
		Properties: map[string]Property{
			"agent":      {Type: "string", Description: "The registry agent name, e.g. \"claude-code\""},
			"session_id": {Type: "string", Description: "The session id, or a unique prefix of it"},
			"from":       {Type: "integer", Description: "Inclusive turn index to start at (default 0)"},
			"to":         {Type: "integer", Description: "Exclusive turn index to end at (default: end of session)"},
		},
		Required: []string{"agent", "session_id"},
	}
}

// Kind returns "agent_store".
func (r *ReadSession) Kind() Kind { return KindAgentStore }

// Subject returns the agent for display.
func (r *ReadSession) Subject(args map[string]any) string {
	if v, ok := argString(args, "agent"); ok {
		return v
	}
	return ""
}

// Mutates reports that ReadSession only reads.
func (r *ReadSession) Mutates() bool { return false }

// Execute resolves the session and returns the turn range.
func (r *ReadSession) Execute(ctx context.Context, args map[string]any) (Result, error) {
	agent, ok := argString(args, "agent")
	if !ok || agent == "" {
		return Result{}, fmt.Errorf("missing agent argument")
	}
	sessionID, ok := argString(args, "session_id")
	if !ok || sessionID == "" {
		return Result{}, fmt.Errorf("missing session_id argument")
	}
	if !agentstore.HasAgent(agent) {
		return Result{}, fmt.Errorf("unknown agent %q", agent)
	}
	if r.Store == nil {
		return Result{}, fmt.Errorf("agent-store search is not available")
	}
	req := agentstore.ReadRequest{Agent: agent, SessionID: sessionID}
	if n, ok := argInt64(args, "from"); ok && n > 0 {
		req.From = int(n)
	}
	if n, ok := argInt64(args, "to"); ok && n > 0 {
		req.To = int(n)
	}

	res, err := r.Store.ReadSession(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: KindAgentStore, Content: formatSessionRead(res), Meta: map[string]any{"tool": "read_session"}}, nil
}

// formatSessionRead renders the ReadSession result body.
func formatSessionRead(res agentstore.SessionRead) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s  %s  %s\n", res.Agent, shortID(res.SessionID), res.Path))
	if res.Project != "" {
		b.WriteString("  cwd: " + res.Project + "\n")
	}
	b.WriteString("\n")
	for _, t := range res.Turns {
		b.WriteString(fmt.Sprintf("turn %d  %s  %s\n", t.Index, t.Role, formatWhen(t.At)))
		b.WriteString(t.Text + "\n\n")
	}
	if res.Truncated {
		b.WriteString("… truncated at 40 turns / 64 KiB\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
