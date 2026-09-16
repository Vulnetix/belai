package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agentprofile"
)

func writeAgentProfile(t *testing.T, name string, p agentprofile.AgentProfile) {
	t.Helper()
	if p.Name == "" {
		p.Name = name
	}
	if p.Description == "" {
		p.Description = "test agent"
	}
	if p.SystemPrompt == "" {
		p.SystemPrompt = "You are a test agent."
	}
	if p.Mode == "" {
		p.Mode = agentprofile.ModeSingle
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save(%q): %v", name, err)
	}
}

func agentTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)
	return home
}

func TestAgentViewListsDiscoveredProfiles(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "alpha", agentprofile.AgentProfile{Description: "first agent"})
	writeAgentProfile(t, "beta", agentprofile.AgentProfile{Description: "second agent"})

	a := New(Options{})
	a.width = 200
	a.push(viewAgent)

	out := a.agentView()
	for _, want := range []string{"alpha", "beta", "first agent", "second agent"} {
		if !strings.Contains(out, want) {
			t.Errorf("agentView missing %q:\n%s", want, out)
		}
	}
	dir, _ := agentprofile.Dir()
	for _, name := range []string{"alpha.json", "beta.json"} {
		path := filepath.Join(dir, name)
		if !strings.Contains(out, path) {
			t.Errorf("agentView missing path %q:\n%s", path, out)
		}
	}
}

func TestAgentViewEditDescription(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "edit-bot", agentprofile.AgentProfile{Description: "before"})

	a := New(Options{})
	a.push(viewAgent)
	a.width = 200
	if a.selectedAgentProfile() == nil {
		t.Fatal("no profile selected")
	}

	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}}); cmd != nil {
		t.Fatalf("edit key should be synchronous")
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode after pressing e")
	}

	// Enter opens the inline text editor for the description field.
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.agentState.fieldEdit {
		t.Fatal("expected fieldEdit after pressing enter on description")
	}
	a.editor.SetValue("after")
	if _, cmd := a.handleAgentKey(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("commit should be synchronous")
	}
	if a.agentState.fieldEdit {
		t.Fatalf("fieldEdit should close after commit")
	}

	reloaded, err := agentprofile.Load("edit-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Description != "after" {
		t.Fatalf("description = %q, want after", reloaded.Description)
	}
}

func TestAgentViewCycleMode(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "mode-bot", agentprofile.AgentProfile{
		Mode:             agentprofile.ModeSingle,
		Schedule:         "0 * * * *",
		MonitorCondition: "file changes",
	})

	a := New(Options{})
	a.width = 200
	a.push(viewAgent)
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	// Move to mode field (index 1).
	a.handleAgentKey(tea.KeyMsg{Type: tea.KeyDown})
	if a.agentState.fields[a.agentState.fieldSel].key != "mode" {
		t.Fatalf("selected field %q, want mode", a.agentState.fields[a.agentState.fieldSel].key)
	}

	for _, want := range []string{agentprofile.ModeLoop, agentprofile.ModeScheduled, agentprofile.ModeMonitor, agentprofile.ModeSingle} {
		a.handleAgentKey(tea.KeyMsg{Type: tea.KeySpace})
		got := a.agentState.fields[1].value
		if got != want {
			t.Fatalf("mode after space = %q, want %q", got, want)
		}
	}
}

func TestAgentBuilderDoneOpensEditForNewProfile(t *testing.T) {
	agentTestHome(t)
	p := agentprofile.AgentProfile{
		Name:         "new-bot",
		Description:  "created",
		SystemPrompt: "sp",
		Mode:         agentprofile.ModeSingle,
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	a := New(Options{})
	a.handleAgentBuilderDone(agentBuilderDoneMsg{profile: p, path: "/tmp/new-bot.json"})

	if a.view != viewAgent {
		t.Fatalf("view = %v, want viewAgent", a.view)
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode for newly created agent")
	}
	if p := a.selectedAgentProfile(); p == nil || p.Name != "new-bot" {
		t.Fatalf("selected profile = %v, want new-bot", p)
	}
}

func TestAgentEditCommandMissingName(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.handleCommand("/agent edit")
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "agent edit <name>") {
		t.Fatalf("expected usage message, got %q", last.Text())
	}
}

func TestAgentEditCommandOpensView(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "open-bot", agentprofile.AgentProfile{})

	a := New(Options{})
	a.handleCommand("/agent edit open-bot")

	if a.view != viewAgent {
		t.Fatalf("view = %v, want viewAgent", a.view)
	}
	if !a.agentState.editMode {
		t.Fatal("expected editMode")
	}
}
