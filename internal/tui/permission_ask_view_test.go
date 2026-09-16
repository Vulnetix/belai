package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/config"
)

func sampleAskRequest() *agent.AskRequest {
	return &agent.AskRequest{Name: "Write", Subject: "src/x.go", Args: map[string]any{"path": "src/x.go", "content": "x"}}
}

func TestEventPermissionAskPushesViewAndStopsPump(t *testing.T) {
	a := New(Options{})
	a.cancel = func() {}
	reply := make(chan agent.PermissionAskReply)

	_, cmd := a.Update(agentEventMsg{Kind: agent.EventPermissionAskKind, Ask: sampleAskRequest(), AskReply: reply})

	if a.view != viewPermissionAsk {
		t.Fatalf("expected viewPermissionAsk, got %d", a.view)
	}
	if a.permAskState.reply != reply {
		t.Fatal("permission-ask state did not capture reply channel")
	}
	if a.permAskState.ask == nil || a.permAskState.ask.Name != "Write" {
		t.Fatalf("ask = %+v", a.permAskState.ask)
	}
	if cmd != nil {
		t.Fatal("expected no command; the agent pump must not re-arm yet")
	}
}

func TestPermissionAskEnterAllowSendsReplyAndRearmsPump(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply)
	a.permAskState = newPermissionAskState(sampleAskRequest(), reply)

	ch := make(chan agent.Event)
	a.events = ch
	close(ch)

	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if a.view != viewChat {
		t.Fatalf("expected pop back to chat, got %d", a.view)
	}
	select {
	case ans := <-reply:
		if !ans.Allow {
			t.Fatal("enter on allow once should answer allow")
		}
	case <-time.After(time.Second):
		t.Fatal("no reply sent")
	}
	if cmd == nil {
		t.Fatal("expected pump to re-arm")
	}
}

func TestPermissionAskEscDeniesWithoutCancellingTurn(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	reply := make(chan agent.PermissionAskReply)
	a.permAskState = newPermissionAskState(sampleAskRequest(), reply)

	cancelled := false
	a.cancel = func() { cancelled = true }

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	a = m.(*App)

	if a.view != viewChat {
		t.Fatalf("expected pop back to chat, got %d", a.view)
	}
	if cancelled {
		t.Fatal("esc must deny the call, not cancel the whole turn")
	}
	select {
	case ans := <-reply:
		if ans.Allow {
			t.Fatal("esc should answer deny")
		}
	case <-time.After(time.Second):
		t.Fatal("no deny reply sent")
	}
}

func TestPermissionAskAllowAlwaysWritesScopedRule(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.push(viewPermissionAsk)
	a.permAskState = newPermissionAskState(sampleAskRequest(), make(chan agent.PermissionAskReply))
	a.permAskState.selected = permAskAllowAlways

	ch := make(chan agent.Event)
	a.events = ch
	close(ch)

	_, _ = a.Update(tea.KeyMsg{Type: tea.KeyEnter})

	got, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	found := false
	for _, r := range got.Permissions.Allow {
		if r == "Write(src/x.go)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("allow always should persist Write(src/x.go), got %v", got.Permissions.Allow)
	}
}

func TestPermissionAskViewRendersToolAndDiff(t *testing.T) {
	a := New(Options{})
	a.push(viewPermissionAsk)
	a.permAskState = newPermissionAskState(sampleAskRequest(), make(chan agent.PermissionAskReply))

	v := a.permissionAskView()
	if !strings.Contains(v, "Write") || !strings.Contains(v, "src/x.go") {
		t.Fatalf("permission ask view missing tool/subject:\n%s", v)
	}
}
