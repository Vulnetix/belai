package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tui/components"
)

func swapApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "sk-test")
	a := New(Options{Workdir: t.TempDir()})
	a.settings.Routing = &config.RoutingSettings{Fast: &config.RoutingTarget{Provider: "openai", Model: "gpt-fast"}}
	a.cfg.Provider, a.cfg.Model = "openai", "gpt-main"
	_ = a.refreshProvider()
	if a.cfg.Routing.Fast == nil {
		t.Fatal("test setup: no fast tier resolved")
	}
	return a
}

func lastEphemeralSystem(a *App) string {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "system" {
			if !a.messages[i].Ephemeral {
				return "NOT EPHEMERAL: " + a.messages[i].Content
			}
			return a.messages[i].Content
		}
	}
	return ""
}

func TestModelSwapSchedulesAndAppliesOnNextTurn(t *testing.T) {
	a := swapApp(t)
	a.view = viewChat
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if got := lastEphemeralSystem(a); !strings.Contains(got, "next turn will use fast model") {
		t.Fatalf("thread line = %q", got)
	}
	if a.cfg.Model != "gpt-main" {
		t.Fatalf("model changed before the next turn: %q", a.cfg.Model)
	}
	_ = a.send(nil)
	if a.cfg.Model != "gpt-fast" || !a.swap.onFast {
		t.Fatalf("after send: model %q onFast %v", a.cfg.Model, a.swap.onFast)
	}
	if _, m := a.persistedModel(); m != "gpt-main" {
		t.Fatalf("saved selection must stay on main, got %q", m)
	}
	// And back.
	a.cancel = nil
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if got := lastEphemeralSystem(a); !strings.Contains(got, "next turn will use main model") {
		t.Fatalf("thread line = %q", got)
	}
	_ = a.send(nil)
	if a.cfg.Model != "gpt-main" || a.swap.onFast {
		t.Fatalf("after return: model %q onFast %v", a.cfg.Model, a.swap.onFast)
	}
}

func TestModelSwapSecondPressCancels(t *testing.T) {
	a := swapApp(t)
	a.view = viewChat
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if got := lastEphemeralSystem(a); !strings.Contains(got, "cancelled") || !strings.Contains(got, "stays on the main model") {
		t.Fatalf("thread line = %q", got)
	}
	_ = a.send(nil)
	if a.cfg.Model != "gpt-main" || a.swap.onFast {
		t.Fatalf("a cancelled switch applied: model %q", a.cfg.Model)
	}
}

func TestModelSwapWithoutFastTier(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.view = viewChat
	a.cfg.Routing = run.RoutingConfig{}
	a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if a.swap.pending != "" {
		t.Fatalf("pending = %q with no fast tier", a.swap.pending)
	}
	if got := lastEphemeralSystem(a); !strings.Contains(got, "no separate fast model") {
		t.Fatalf("thread line = %q", got)
	}
}

func TestModelSwapDuringTurnInterruptsAndRetries(t *testing.T) {
	a := swapApp(t)
	a.view = viewChat
	a.echoUser("do the thing")
	_ = a.sendTurnNoEcho("do the thing", nil, "")
	if !a.working() {
		t.Fatal("test setup: no running turn")
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if cmd == nil {
		t.Fatal("expected a retry command")
	}
	if a.cfg.Model != "gpt-fast" {
		t.Fatalf("retry runs on %q, want the fast model", a.cfg.Model)
	}
	if !a.working() {
		t.Fatal("the retried turn must be running")
	}
	var cancelled, retrying bool
	for _, m := range a.messages {
		if m.Role == "system" && m.Content == "request cancelled" {
			cancelled = true
		}
		if m.Role == "system" && strings.Contains(m.Content, "retrying the interrupted turn on the fast model") {
			retrying = true
		}
	}
	if !cancelled || !retrying {
		t.Fatalf("record lacks cancel/retry lines: cancelled=%v retrying=%v", cancelled, retrying)
	}
	users := 0
	for _, m := range a.messages {
		if m.Role == "user" {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("the retry must not echo the prompt again: %d user rows", users)
	}
}

func TestModelSwapForgetsAReplacedModel(t *testing.T) {
	a := swapApp(t)
	a.swap = modelSwap{onFast: true, main: modelTarget{"openai", "gpt-main"}, fast: modelTarget{"openai", "gpt-fast"}}
	a.cfg.Model = "gpt-picked"
	a.normaliseSwap()
	if a.swap.onFast {
		t.Fatal("a model picked by hand must end the swap")
	}
	if _, m := a.persistedModel(); m != "gpt-picked" {
		t.Fatalf("persisted %q", m)
	}
}

func TestTurnToolCountsIsTheFooterCounter(t *testing.T) {
	msgs := []components.Message{
		{Role: "user", Content: "now"},
		{Role: "tool", ToolName: "Read"},
		{Role: "tool", ToolName: "Edit"},
		{Role: "tool", ToolName: "Read", SubagentID: "sa-1"},
	}
	tools, edits := turnToolCounts(msgs)
	if tools != 2 || edits != 1 {
		t.Fatalf("tools=%d edits=%d", tools, edits)
	}
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	a.messages = msgs
	a.refreshFooter()
	if a.footer.Tools != 2 {
		t.Fatalf("footer.Tools = %d, want the completion counter's 2", a.footer.Tools)
	}
}
