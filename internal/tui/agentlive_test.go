package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/tui/components"
)

func bgEvent(kind agent.EventKind) bgAgentEventMsg {
	return bgAgentEventMsg{AgentName: "triage", Kind: kind}
}

// A background agent's work lands in its own thread: streamed text is joined
// into one row, a tool call and its result share a row, and the main view
// keeps only the line saying the agent finished.
func TestBackgroundEventsBuildTheirOwnThread(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.noteAgentStarted("triage")

	for _, e := range []bgAgentEventMsg{
		{AgentName: "triage", Kind: agent.EventTextKind, Text: "Look"},
		{AgentName: "triage", Kind: agent.EventTextKind, Text: "ing at go.sum"},
		{AgentName: "triage", Kind: agent.EventToolStartKind, ToolName: "Read", ToolArgs: `{"path":"go.sum"}`, ToolCallID: "c1"},
		{AgentName: "triage", Kind: agent.EventToolResultKind, ToolName: "Read", ToolResult: "line one\nline two", ToolCallID: "c1"},
		{AgentName: "triage", Kind: agent.EventTextKind, Text: "Three findings."},
		bgEvent(agent.EventDoneKind),
	} {
		a.handleBgAgentEvent(e)
	}

	var thread []components.Message
	for _, m := range a.messages {
		if m.SubagentID == "bg:triage" {
			thread = append(thread, m)
		}
	}
	if len(thread) != 3 {
		t.Fatalf("thread rows = %d, want reply, tool, reply: %+v", len(thread), thread)
	}
	if thread[0].Text() != "Looking at go.sum" {
		t.Errorf("streamed deltas were not joined: %q", thread[0].Text())
	}
	if thread[1].Role != "tool" || thread[1].ToolName != "Read" || thread[1].Text() != "line one\nline two" {
		t.Errorf("tool row = %+v", thread[1])
	}

	main := a.filteredMessages()
	var sawDone bool
	for _, m := range main {
		if strings.HasPrefix(m.SubagentID, "bg:") {
			t.Fatalf("background row leaked into the main view: %+v", m)
		}
		if strings.Contains(m.Text(), "■ triage done · 1 tool") {
			sawDone = true
		}
	}
	if !sawDone {
		t.Errorf("main view never heard the agent finish: %+v", main)
	}

	l, ok := a.lookupLive("bg:triage")
	if !ok || l.Tools != 1 || l.Tool != "" || l.Last != "Three findings." {
		t.Fatalf("ledger = %+v", l)
	}
}

// Following the thread shows it, with the banner first.
func TestFollowingABackgroundThreadShowsIt(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "triage", Kind: agent.EventToolStartKind, ToolName: "Grep", ToolCallID: "g1"})
	a.followAgent("bg:triage")

	msgs := a.filteredMessages()
	if len(msgs) != 2 || !strings.Contains(msgs[0].Text(), "following triage") || msgs[1].ToolName != "Grep" {
		t.Fatalf("followed thread = %+v", msgs)
	}
}

// esc steps out of a followed thread before it would cancel anything.
func TestEscReturnsFromAFollowedThread(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.threadFilter = "e1"
	cancelled := false
	a.cancel = func() { cancelled = true }

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.threadFilter != "" {
		t.Fatalf("esc left the filter on %q", a.threadFilter)
	}
	if cancelled {
		t.Fatalf("esc cancelled the main turn while returning from a thread")
	}
}

// An error ends the stream: it is counted, kept in the thread, and reported
// once in the main view.
func TestBackgroundErrorIsCountedAndReported(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.handleBgAgentEvent(bgAgentEventMsg{AgentName: "triage", Kind: agent.EventErrorKind, Err: errors.New("rate limited")})

	l, _ := a.lookupLive("bg:triage")
	if l.Errors != 1 {
		t.Fatalf("errors = %d, want 1", l.Errors)
	}
	var main, thread int
	for _, m := range a.messages {
		if !strings.Contains(m.Text(), "rate limited") {
			continue
		}
		if m.SubagentID == "bg:triage" {
			thread++
		} else {
			main++
		}
	}
	if main != 1 || thread != 1 {
		t.Fatalf("error rows: main %d, thread %d; want 1 each", main, thread)
	}
}

// Agent output is untrusted; the audit trail keeps one flat line per step.
func TestAuditLineFlattensUntrustedText(t *testing.T) {
	got := auditLine("a\x1b[2Jb\n\tc\u202Ed\u2066e")
	if got != "a [2Jb cde" {
		t.Fatalf("auditLine = %q", got)
	}
	long := auditLine(strings.Repeat("x", 500))
	if n := len([]rune(long)); n != auditTextCap+1 || !strings.HasSuffix(long, "…") {
		t.Fatalf("long line not capped: %d runes", n)
	}
}

func TestAuditTrailIsBounded(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	for i := 0; i < auditCap+50; i++ {
		a.noteAudit("e1", "tool", "Read")
	}
	if len(a.ledger.audit) != auditCap {
		t.Fatalf("audit = %d rows, want %d", len(a.ledger.audit), auditCap)
	}
}

// The footer names what the followed agent is doing.
func TestFooterPulseFollowsTheFilteredAgent(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 160
	a.noteAgentState("e2", "explore 2", "running")
	a.noteAgentTool("e2", "Grep", `{"pattern":"TODO"}`)
	a.noteAgentTool("e2", "Grep", `{"pattern":"FIXME"}`)
	a.threadFilter = "e2"
	a.refreshFooter()

	if a.footer.Pulse == nil {
		t.Fatalf("no pulse while a thread is followed")
	}
	line := a.footer.View()
	for _, want := range []string{"explore 2", "running", "⚙ Grep", "2 tools", "esc main"} {
		if !strings.Contains(line, want) {
			t.Errorf("footer is missing %q:\n%s", want, line)
		}
	}

	a.threadFilter = ""
	a.refreshFooter()
	if a.footer.Pulse != nil {
		t.Fatalf("pulse stayed after returning to main")
	}
	if got := a.footer.Subagents; len(got) != 0 {
		t.Fatalf("ledger-only agents should not add roster chips: %+v", got)
	}
}

// Roster chips carry the running tool beside them.
func TestFooterChipsCarryTheRunningTool(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.width = 160
	a.handleSubagentUpdate(agent.SubagentUpdate{ID: "e1", Label: "explore 1", State: "running"})
	a.noteAgentTool("e1", "Read", "")
	a.refreshFooter()
	if len(a.footer.Subagents) != 1 || a.footer.Subagents[0].Detail != "Read" {
		t.Fatalf("chips = %+v", a.footer.Subagents)
	}
	if !strings.Contains(a.footer.View(), "Read") {
		t.Fatalf("footer does not show the running tool:\n%s", a.footer.View())
	}
}

// /agents opens the profiles when nothing has run, the running tab after.
func TestAgentsCommandPicksTheUsefulTab(t *testing.T) {
	agentTestHome(t)
	a := New(Options{Workdir: t.TempDir()})

	a.handleCommand("/agents")
	if a.view != viewAgent || a.agentState.tab != agentTabProfiles {
		t.Fatalf("empty ledger: view %v tab %d, want profiles", a.view, a.agentState.tab)
	}
	a.pop()

	a.noteAgentState("e1", "explore 1", "running")
	a.handleCommand("/agents")
	if a.agentState.tab != agentTabLive {
		t.Fatalf("tab = %d, want running", a.agentState.tab)
	}
	if !strings.Contains(a.View(), "explore 1") {
		t.Fatalf("running tab does not list the agent:\n%s", a.View())
	}

	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if a.agentState.tab != agentTabAudit || !strings.Contains(a.View(), "state") {
		t.Fatalf("3 did not open the audit trail:\n%s", a.View())
	}
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyShiftTab})
	if a.agentState.tab != agentTabProfiles {
		t.Fatalf("shift+tab: tab = %d, want profiles", a.agentState.tab)
	}
}

// enter on a running agent follows its thread back in chat.
func TestAgentsEnterFollowsTheThread(t *testing.T) {
	agentTestHome(t)
	a := New(Options{Workdir: t.TempDir()})
	a.noteAgentState("e1", "explore 1", "running")
	a.handleCommand("/agents")
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.view != viewChat || a.threadFilter != "e1" || len(a.viewStack) != 0 {
		t.Fatalf("view %v filter %q stack %v", a.view, a.threadFilter, a.viewStack)
	}
}

// /agent log opens the agent's audit trail rather than dumping text.
func TestAgentLogOpensTheAuditTrail(t *testing.T) {
	agentTestHome(t)
	a := New(Options{Workdir: t.TempDir()})
	a.noteAgentTool("bg:triage", "Read", "")
	a.noteAgentTool("e1", "Grep", "")
	a.handleCommand("/agent log triage")
	if a.view != viewAgent || a.agentState.tab != agentTabAudit || a.agentState.auditFilter != "bg:triage" {
		t.Fatalf("view %v tab %d filter %q", a.view, a.agentState.tab, a.agentState.auditFilter)
	}
	if rows := a.auditRows(); len(rows) != 1 || rows[0].ID != "bg:triage" {
		t.Fatalf("audit rows = %+v", rows)
	}
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if len(a.auditRows()) != 2 {
		t.Fatalf("c did not clear the filter")
	}
}

// f1 opens the switcher over chat, a letter jumps, and the draft survives.
func TestScreenSwitcherJumpsAndKeepsTheDraft(t *testing.T) {
	agentTestHome(t)
	a := New(Options{Workdir: t.TempDir()})
	a.editor.SetValue("half a prompt")

	a.Update(tea.KeyMsg{Type: tea.KeyF1})
	if a.view != viewScreens {
		t.Fatalf("f1 opened %v, want the switcher", a.view)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if a.view != viewPermissions {
		t.Fatalf("k opened %v, want permissions", a.view)
	}
	// A second jump to a screen already open walks back to it.
	a.Update(tea.KeyMsg{Type: tea.KeyF1})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if len(a.viewStack) != 1 {
		t.Fatalf("stack = %v, want permissions once", a.viewStack)
	}
	a.pop()
	if a.view != viewChat || a.editor.Value() != "half a prompt" {
		t.Fatalf("back in %v with draft %q", a.view, a.editor.Value())
	}
}

// f1 never leaves a screen that is waiting on an answer.
func TestScreenSwitcherWaitsOnModalScreens(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.push(viewPermissionAsk)
	a.Update(tea.KeyMsg{Type: tea.KeyF1})
	if a.view != viewPermissionAsk {
		t.Fatalf("f1 left the permission ask for %v", a.view)
	}
}

// Every destination in the switcher is a registered view with its own letter.
func TestScreenEntriesAreDistinct(t *testing.T) {
	keys := map[string]bool{}
	for _, e := range screenEntries {
		if keys[e.key] {
			t.Errorf("letter %q used twice", e.key)
		}
		keys[e.key] = true
		if _, ok := viewHandlers[e.view]; !ok {
			t.Errorf("%s points at an unregistered view", e.name)
		}
	}
}

// The pulse only ticks while something is working, and never twice at once.
func TestAgentPulseTicksOnlyWhileWorking(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	if a.armAgentPulse() != nil {
		t.Fatalf("pulse armed with nothing running")
	}
	a.noteAgentState("e1", "explore 1", "running")
	if a.armAgentPulse() == nil {
		t.Fatalf("pulse not armed while an agent runs")
	}
	if a.armAgentPulse() != nil {
		t.Fatalf("a second tick was scheduled while one is pending")
	}
	a.noteAgentState("e1", "", "done")
	if a.handleAgentPulse() != nil {
		t.Fatalf("pulse re-armed after the last agent finished")
	}
}
