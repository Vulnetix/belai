package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/rolemanager"
)

func lastRole(a *App) string {
	if len(a.messages) == 0 {
		return ""
	}
	return a.messages[len(a.messages)-1].Role
}

func TestReportEventAddsSystemLine(t *testing.T) {
	a := New(Options{})
	mm, _ := a.Update(agentEventMsg{Kind: agent.EventReportKind, GoalSentinel: rolemanager.GoalComplete})
	a = mm.(*App)
	if lastRole(a) != "system" || !strings.Contains(a.messages[len(a.messages)-1].Content, "goal complete") {
		t.Fatalf("report event line = %+v", a.messages)
	}
	mm, _ = a.Update(agentEventMsg{Kind: agent.EventReportKind, GoalSentinel: rolemanager.GoalPartial})
	a = mm.(*App)
	if !strings.Contains(a.messages[len(a.messages)-1].Content, "goal stopped") {
		t.Fatalf("stop report line = %q", a.messages[len(a.messages)-1].Content)
	}
	// The report text that follows must open a fresh bubble, not append to a
	// pass's earlier reply.
	var tm tea.Model = a
	tm, _ = tm.Update(agentEventMsg{Kind: agent.EventTextKind, Text: "REPORT"})
	a = tm.(*App)
	if lastRole(a) != "assistant" || a.messages[len(a.messages)-1].Text() != "REPORT" {
		t.Fatalf("report text must stream into a new bubble: %+v", a.messages[len(a.messages)-1])
	}
}
