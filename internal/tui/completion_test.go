package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestAgentTurnCompleted(t *testing.T) {
	agentRes := run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeAgent}}
	cases := []struct {
		name     string
		res      run.Result
		planMode bool
		want     bool
	}{
		{"agent", agentRes, false, true},
		{"unresolved mode reads as agent", run.Result{}, false, true},
		{"plan mode session", agentRes, true, false},
		{"goal mode", run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeGoal}}, false, false},
		{"plan mode decision", run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModePlan}}, false, false},
		// An approved plan runs the goal loop under an agent decision; its
		// sentinel is what marks it as reported.
		{"approved plan", run.Result{ModeDecision: agentRes.ModeDecision, GoalSentinel: rolemanager.GoalComplete}, false, false},
		{"plan loop sentinel", run.Result{PlanSentinel: rolemanager.PlanComplete}, false, false},
	}
	for _, c := range cases {
		if got := agentTurnCompleted(c.res, c.planMode); got != c.want {
			t.Errorf("%s: agentTurnCompleted = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCompletionSummaryCountsThisTurnOnly(t *testing.T) {
	msgs := []components.Message{
		{Role: "user", Content: "earlier"},
		{Role: "tool", ToolName: "Read"},
		{Role: "user", Content: "now"},
		{Role: "tool", ToolName: "Read"},
		{Role: "user", Content: "steer", Steering: true},
		{Role: "tool", ToolName: "Edit"},
		{Role: "tool", ToolName: "Write"},
		{Role: "tool", ToolName: "Read", SubagentID: "sa-1"},
		{Role: "assistant", Content: "done"},
	}
	got := completionSummary(msgs, 1234*time.Millisecond)
	want := "agent turn complete · 3 tool calls · 2 edits · 1.2s"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := completionSummary([]components.Message{{Role: "user"}, {Role: "tool", ToolName: "Edit"}}, 0); got != "agent turn complete · 1 tool call · 1 edit" {
		t.Fatalf("singular summary = %q", got)
	}
}

func doneStep(t *testing.T, a *App, res run.Result) *App {
	t.Helper()
	mm, _ := a.Update(agentEventMsg{Kind: agent.EventDoneKind, Result: res})
	return mm.(*App)
}

func TestDoneAddsCompletionPanelInAgentModeOnly(t *testing.T) {
	a := New(Options{})
	// New restores the last mode from on-disk state; pin it to agent.
	a.mode, a.planMode = "agent", false
	a.messages = []components.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}
	a = doneStep(t, a, run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeAgent}, Reply: "hello"})
	if lastRole(a) != completionRole {
		t.Fatalf("agent turn must end on the completion panel, last role = %q", lastRole(a))
	}
	if !strings.HasPrefix(a.messages[len(a.messages)-1].Content, "agent turn complete") {
		t.Fatalf("panel body = %q", a.messages[len(a.messages)-1].Content)
	}

	g := New(Options{})
	g.messages = []components.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "report"}}
	g = doneStep(t, g, run.Result{ModeDecision: rolemanager.ModeDecision{Mode: modes.ModeGoal}, GoalSentinel: rolemanager.GoalComplete, Reply: "report"})
	for _, m := range g.messages {
		if m.Role == completionRole {
			t.Fatal("a goal turn ends on its report, not the completion panel")
		}
	}
}

func TestCompletionPanelNeverReachesTheModel(t *testing.T) {
	a := New(Options{})
	a.messages = []components.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: completionRole, Content: "agent turn complete · 0 tool calls · 0 edits"},
	}
	for _, tn := range a.buildTurns() {
		if strings.Contains(tn.Content, "agent turn complete") {
			t.Fatalf("completion panel leaked into provider turns: %+v", tn)
		}
	}
}

func TestCompletionPanelRoundTripsThroughSession(t *testing.T) {
	entries := []session.Entry{
		{Type: "user", Role: "user", Content: "hi"},
		{Type: "assistant", Role: "assistant", Content: "hello"},
		{Type: completionRole, Role: completionRole, Content: "agent turn complete · 0 tool calls · 0 edits"},
		{Type: completionRole, Role: completionRole, Content: "  "},
	}
	msgs, _ := messagesFromEntries(entries)
	var n int
	for _, m := range msgs {
		if m.Role == completionRole {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("rehydrated %d completion panels, want 1 (blank dropped)", n)
	}
	if !settled(msgs, len(msgs)-1, true, false) {
		t.Fatal("a completion panel is final as soon as it exists")
	}
	if neverPersisted(components.Message{Role: completionRole, Content: "x"}) {
		t.Fatal("a non-empty completion panel must persist")
	}
}

func TestCompletionPanelRenders(t *testing.T) {
	ml := components.MessageList{
		Width:    80,
		Messages: []components.Message{{Role: completionRole, Content: "agent turn complete · 1 tool call · 0 edits"}},
	}
	out := ml.View()
	if !strings.Contains(out, components.CompletionTitle) || !strings.Contains(out, "agent turn complete") {
		t.Fatalf("completion panel render = %q", out)
	}
}
