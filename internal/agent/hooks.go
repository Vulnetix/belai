package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/hooks"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/tools"
	"github.com/vulnetix/signet/internal/trace"
)

// Hook runner bounds. A hook's own timeout_ms overrides hookTimeout.
const (
	hookTimeout  = 5 * time.Second
	hookMaxBytes = 64 * 1024
	// hookResultSummaryBytes bounds the tool output a post_tool hook sees.
	hookResultSummaryBytes = 2 * 1024
)

// LoadHooks reads the global hooks directory, then enabled plugins' hooks.
// Discovery fails closed: an unreadable dir or a disabled setting yields no
// hooks, never an error.
func LoadHooks(settings config.Settings, pol posture.Policy) *hooks.Set {
	if !settings.Hooks.HooksEnabled() {
		return nil
	}
	var loaded []*hooks.Hook
	if dir, err := config.GlobalHooksDir(); err == nil {
		if hs, err := hooks.LoadDir(dir, pol); err == nil {
			loaded = hs
		}
	}
	// Enabled plugins' hooks follow the user's own; each keeps the directory
	// it was loaded from, so its command resolves inside its plugin.
	if hooks.Extra != nil {
		loaded = append(loaded, hooks.Extra(pol)...)
	}
	if len(loaded) == 0 {
		return nil
	}
	return &hooks.Set{Hooks: loaded, Runner: &hooks.Runner{Timeout: hookTimeout, MaxBytes: hookMaxBytes}}
}

// Hooks returns the session's loaded hooks, so a caller (the TUI) can fire
// the session-level events against the same set. nil means none.
func (s *Session) Hooks() *hooks.Set { return s.hookSet }

// FireHook runs the hooks for a non-tool event (session_start, session_end,
// pre_compact, notification) and returns the outcome. The session id and
// working directory are filled in.
func (s *Session) FireHook(ctx context.Context, in hooks.Input) hooks.Outcome {
	if !s.hookSet.Has(in.Event) {
		return hooks.Outcome{}
	}
	in.SessionID = s.sessionID
	if in.Cwd == "" {
		in.Cwd = s.hookCwd()
	}
	return s.hookSet.Dispatch(ctx, in)
}

func (s *Session) hookCwd() string {
	if cwd := s.registry.Cwd(); cwd != nil {
		return cwd.Dir()
	}
	return s.workdir
}

// isEditTool reports whether a call is one pre_edit/post_edit hooks see.
func isEditTool(name string) bool {
	return strings.EqualFold(name, "Write") || strings.EqualFold(name, "Edit")
}

// preToolHooks runs pre_tool, and pre_edit for Write/Edit, for one call.
func (s *Session) preToolHooks(ctx context.Context, call rolemanager.ToolCall, emit func(Event)) hooks.Outcome {
	o := s.toolHooks(ctx, hooks.EventPreTool, call, "", emit)
	if isEditTool(call.Name) {
		o = mergeOutcome(o, s.toolHooks(ctx, hooks.EventPreEdit, call, "", emit))
	}
	return o
}

// postToolHooks runs post_tool, and post_edit for Write/Edit, after a call.
func (s *Session) postToolHooks(ctx context.Context, call rolemanager.ToolCall, res tools.Result, emit func(Event)) hooks.Outcome {
	if !s.hookSet.Has(hooks.EventPostTool) && !(isEditTool(call.Name) && s.hookSet.Has(hooks.EventPostEdit)) {
		return hooks.Outcome{}
	}
	summary := res.Content
	if len(summary) > hookResultSummaryBytes {
		summary = summary[:hookResultSummaryBytes]
	}
	o := s.toolHooks(ctx, hooks.EventPostTool, call, summary, emit)
	if isEditTool(call.Name) {
		o = mergeOutcome(o, s.toolHooks(ctx, hooks.EventPostEdit, call, summary, emit))
	}
	return o
}

func (s *Session) toolHooks(ctx context.Context, event string, call rolemanager.ToolCall, summary string, emit func(Event)) hooks.Outcome {
	if !s.hookSet.Has(event) {
		return hooks.Outcome{}
	}
	start := time.Now()
	o := s.hookSet.Dispatch(ctx, hooks.Input{
		Event:             event,
		SessionID:         s.sessionID,
		Cwd:               s.hookCwd(),
		ToolName:          call.Name,
		ToolInput:         call.Args,
		ToolResultSummary: summary,
	})
	s.traceHook(event, o, time.Since(start))
	reportHookFailures(o, emit)
	return o
}

func (s *Session) traceHook(event string, o hooks.Outcome, d time.Duration) {
	if o.Ran == 0 {
		return
	}
	if s.trace == nil {
		return
	}
	verdict := o.Decision
	if verdict == "" {
		verdict = "none"
	}
	s.trace.Record(trace.Record{Phase: "hook", Event: event, Verdict: verdict, Duration: d.String(), Detail: fmt.Sprintf("ran=%d failed=%d", o.Ran, len(o.Failures))})
}

// reportHookFailures surfaces hook errors as warnings. The error text is the
// harness's own (exit status, timeout), never the hook's output.
func reportHookFailures(o hooks.Outcome, emit func(Event)) {
	if emit == nil {
		return
	}
	for _, err := range o.Failures {
		emit(Event{Kind: EventWarningKind, Warning: err.Error()})
	}
}

// mergeOutcome folds b into a with deny > ask > allow precedence.
func mergeOutcome(a, b hooks.Outcome) hooks.Outcome {
	rank := map[string]int{hooks.DecisionNone: 0, hooks.DecisionAllow: 1, hooks.DecisionAsk: 2, hooks.DecisionDeny: 3}
	if rank[b.Decision] > rank[a.Decision] {
		a.Decision = b.Decision
		if b.Decision == hooks.DecisionDeny {
			a.DeniedBy = b.DeniedBy
		}
	}
	a.Reasons = append(a.Reasons, b.Reasons...)
	a.Context = append(a.Context, b.Context...)
	a.Failures = append(a.Failures, b.Failures...)
	a.Ran += b.Ran
	return a
}

// hookDenied renders the placeholder for a call a hook denied. The reasons
// are the hook's text, so they are promoted as KindHook: sanitized and
// classified before the model may read them.
func (s *Session) hookDenied(ctx context.Context, call rolemanager.ToolCall, o hooks.Outcome, emit func(Event)) string {
	msg := fmt.Sprintf("tool result withheld: denied by hook %q", o.DeniedBy)
	return msg + s.promoteHookNotes(ctx, call, o.Reasons, emit)
}

// promoteHookNotes renders hook-written notes as one KindHook result and
// promotes it. The attribution line is the harness's; the text is not.
func (s *Session) promoteHookNotes(ctx context.Context, call rolemanager.ToolCall, notes []hooks.Note, emit func(Event)) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	for i, n := range notes {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "hook %s (untrusted text from a hook command): %s", n.Hook, n.Text)
	}
	return "\n\n" + s.promoteResult(ctx, call, tools.Result{Kind: tools.KindHook, Content: b.String()}, emit)
}

// promptHookNotes renders user_prompt_submit context for the prompt. It is
// appended to the sanitized prompt before admission, so the prompt
// classifier reads it along with the prompt.
func promptHookNotes(notes []hooks.Note) string {
	if len(notes) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nContext from user hooks (untrusted):")
	for _, n := range notes {
		fmt.Fprintf(&b, "\n- %s: %s", n.Hook, sanitize.Sanitize(n.Text))
	}
	return b.String()
}

// HookBlockedError reports a prompt a user_prompt_submit hook denied.
type HookBlockedError struct {
	Hook   string
	Reason string
}

func (e *HookBlockedError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("prompt blocked by hook %q", e.Hook)
	}
	return fmt.Sprintf("prompt blocked by hook %q: %s", e.Hook, e.Reason)
}

// promptHooks runs user_prompt_submit. A deny returns a HookBlockedError; the
// reason is flattened to one sanitized line because it is shown to the user,
// never sent to the model.
func (s *Session) promptHooks(ctx context.Context, prompt string, emit func(Event)) (string, error) {
	// A subagent's prompt is the harness's own task text, not the user's.
	if s.exploreSubagent || !s.hookSet.Has(hooks.EventUserPromptSubmit) {
		return "", nil
	}
	start := time.Now()
	o := s.hookSet.Dispatch(ctx, hooks.Input{
		Event:     hooks.EventUserPromptSubmit,
		SessionID: s.sessionID,
		Cwd:       s.hookCwd(),
		Prompt:    prompt,
	})
	s.traceHook(hooks.EventUserPromptSubmit, o, time.Since(start))
	reportHookFailures(o, emit)
	if o.Decision == hooks.DecisionDeny {
		var reason string
		for _, r := range o.Reasons {
			if r.Hook == o.DeniedBy {
				reason = strings.Join(strings.Fields(sanitize.Sanitize(r.Text)), " ")
				break
			}
		}
		return "", &HookBlockedError{Hook: o.DeniedBy, Reason: reason}
	}
	return promptHookNotes(o.Context), nil
}

// stopHooks runs the stop event at the end of a turn. Stop hooks cannot
// change the turn, so their output is not read.
func (s *Session) stopHooks(ctx context.Context, emit func(Event)) {
	// A subagent's end is subagent_stop, fired by its parent.
	if s.exploreSubagent || !s.hookSet.Has(hooks.EventStop) {
		return
	}
	start := time.Now()
	o := s.hookSet.Dispatch(context.WithoutCancel(ctx), hooks.Input{Event: hooks.EventStop, SessionID: s.sessionID, Cwd: s.hookCwd()})
	s.traceHook(hooks.EventStop, o, time.Since(start))
	reportHookFailures(o, emit)
}
