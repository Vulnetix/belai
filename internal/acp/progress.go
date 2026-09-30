package acp

import (
	"fmt"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/sanitize"
)

// heartbeatEvery is how long a running prompt may go without an update before
// the editor is told it is still working. A var so tests can shorten it.
var heartbeatEvery = 10 * time.Second

const (
	// maxStatusLine caps one harness status line.
	maxStatusLine = 200
	// maxProgressText caps the partial output of a running tool.
	maxProgressText = 4000
	// maxActivityArgs caps the argument excerpt on a subagent activity line.
	maxActivityArgs = 120
)

// progress turns the agent's render-only events into ACP updates, so the
// editor shows work while the model, the classifier or a subagent is busy. It
// holds the per-prompt state that keeps those updates tidy. Every string that
// leaves it is either a fixed template or cleaned and capped; provider error
// text and model output never appear in a status line.
type progress struct {
	mu      sync.Mutex
	phases  map[string]bool // role-manager phases already announced
	pending map[string]bool // tool calls announced from their first fragment
	subs    map[string]bool // subagents already announced
}

func newProgress() *progress {
	return &progress{phases: map[string]bool{}, pending: map[string]bool{}, subs: map[string]bool{}}
}

// note sends one dim status line.
func (s *Server) note(ss *acpSession, format string, a ...any) {
	line := sanitize.Line(fmt.Sprintf(format, a...), maxStatusLine)
	if line == "" {
		return
	}
	s.update(ss, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": textContent(line + "\n")})
}

// firstOf reports whether key is new in set, and records it.
func (p *progress) firstOf(set map[string]bool, key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if set[key] {
		return false
	}
	set[key] = true
	return true
}

func (p *progress) wasPending(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pending[id]
}

// forwardProgress maps the event kinds forward does not handle. It reports
// whether it consumed the event.
func (s *Server) forwardProgress(ss *acpSession, ev agent.Event) bool {
	p := ss.prog
	if p == nil {
		return false
	}
	switch ev.Kind {
	case agent.EventRoleManagerKind:
		if p.firstOf(p.phases, ev.Phase) {
			switch ev.Phase {
			case agent.RoleManagerPhasePrePrompt:
				s.note(ss, "Checking the prompt")
			case agent.RoleManagerPhaseToolResult:
				s.note(ss, "Checking tool results")
			}
		}
	case agent.EventRetryKind:
		s.note(ss, "Retrying the model, attempt %d of %d in %s", ev.RetryAttempt, ev.RetryMax, ev.RetryDelay.Round(time.Second))
	case agent.EventPassKind:
		if ev.Pass > 0 {
			s.note(ss, "Pass %d", ev.Pass)
		}
	case agent.EventContinuationKind:
		s.note(ss, "Continuing (%d of %d)", ev.Pass, ev.MaxPasses)
	case agent.EventPrefetchKind:
		if n := len(ev.Paths); n > 0 {
			s.note(ss, "Reading %d files for context", n)
		}
	case agent.EventModeDecidedKind:
		if ev.Mode != nil && ev.Mode.Mode != "" {
			s.note(ss, "Mode: %s", ev.Mode.Mode)
		}
	case agent.EventWarningKind:
		s.note(ss, "%s", ev.Warning)
	case agent.EventToolCallDeltaKind:
		// A slow tool call (a large Write) is announced from its first
		// fragment; the start event upgrades it in place.
		if ev.ToolDelta != nil && ev.ToolDelta.ID != "" && ev.ToolDelta.Name != "" && p.firstOf(p.pending, ev.ToolDelta.ID) {
			s.update(ss, map[string]any{
				"sessionUpdate": "tool_call",
				"toolCallId":    ev.ToolDelta.ID,
				"title":         sanitize.Ident(ev.ToolDelta.Name, 64),
				"kind":          toolKind(ev.ToolDelta.Name),
				"status":        "pending",
			})
		}
	case agent.EventToolProgressKind:
		if text := sanitize.TextN(ev.ToolProgress, maxProgressText); text != "" {
			s.update(ss, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    ev.ToolCallID,
				"status":        "in_progress",
				"content":       []any{map[string]any{"type": "content", "content": textContent(text)}},
			})
		}
	case agent.EventSubagentKind:
		if ev.Subagent != nil {
			s.subagent(ss, ev.Subagent)
		}
	case agent.EventSubagentActivityKind:
		if ev.SubagentID != "" && ev.ToolName != "" && ev.ToolResult == "" {
			line := sanitize.Ident(ev.ToolName, 64)
			if args := sanitize.Line(ev.ToolArgs, maxActivityArgs); args != "" {
				line += " " + args
			}
			s.update(ss, map[string]any{
				"sessionUpdate": "tool_call_update",
				"toolCallId":    subagentCallID(ev.SubagentID),
				"status":        "in_progress",
				"content":       []any{map[string]any{"type": "content", "content": textContent(line)}},
			})
		}
	default:
		return false
	}
	return true
}

func subagentCallID(id string) string { return "subagent-" + sanitize.Ident(id, 64) }

// subagent announces a subagent as a tool call and follows its state.
func (s *Server) subagent(ss *acpSession, u *agent.SubagentUpdate) {
	id := subagentCallID(u.ID)
	status := "in_progress"
	switch u.State {
	case "queued":
		status = "pending"
	case "done", "completed":
		status = "completed"
	case "failed", "cancelled":
		status = "failed"
	}
	if ss.prog.firstOf(ss.prog.subs, u.ID) {
		title := sanitize.Line(u.Label, 80)
		if title == "" {
			title = "subagent"
		}
		s.update(ss, map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    id,
			"title":         title,
			"kind":          "think",
			"status":        status,
		})
		return
	}
	s.update(ss, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": status})
}
