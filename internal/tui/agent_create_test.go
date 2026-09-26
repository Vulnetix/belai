package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agentprofile"
)

const validAgentBuilderJSON = `{"name":"ignored","description":"designed","system_prompt":"designed prompt","mode":"single"}`

func TestAgentCreateRegistersSilentActivityAndPhase(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{raw: validAgentBuilderJSON})

	cmd := a.handleCommand("/agent create triage-deps")
	if cmd == nil {
		t.Fatal("/agent create returned no command")
	}
	if a.phase == phaseIdle {
		t.Fatal("expected a working phase")
	}
	if a.rmPhase != "agent designer" {
		t.Fatalf("rmPhase = %q, want agent designer", a.rmPhase)
	}

	found := false
	for _, act := range a.activity.List() {
		if act.Label == "agent design: triage-deps" {
			found = true
			if !act.Silent {
				t.Fatalf("design activity must be Silent")
			}
			if !reflect.DeepEqual(act.Argv, []string{"agent", "create", "triage-deps"}) {
				t.Fatalf("Argv = %v", act.Argv)
			}
			if act.State != "running" {
				t.Fatalf("State = %q, want running", act.State)
			}
		}
	}
	if !found {
		t.Fatal("design activity not registered")
	}

	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want tea.BatchMsg", msg)
	}
	if len(batch) != 2 {
		t.Fatalf("batch length = %d, want 2 (build + spinner tick)", len(batch))
	}
}

func TestAgentCreateInvalidNameShortCircuits(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{raw: validAgentBuilderJSON})

	cmd := a.handleCommand("/agent create !!!")
	if cmd != nil {
		t.Fatalf("invalid name should return no command, got %T", cmd)
	}
	if a.phase != phaseIdle {
		t.Fatal("invalid name must not start a working phase")
	}
	for _, act := range a.activity.List() {
		if strings.HasPrefix(act.Label, "agent design:") {
			t.Fatalf("invalid name must not register a design activity: %+v", act)
		}
	}
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "agent create:") || !strings.Contains(last.Text(), "invalid profile name") {
		t.Fatalf("expected invalid-name system line, got %q", last.Text())
	}
}

func TestAgentCreateNilClassifierShortCircuits(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.SetClassifier(nil)

	cmd := a.handleCommand("/agent create valid-bot")
	if cmd != nil {
		t.Fatalf("nil classifier should return no command, got %T", cmd)
	}
	if a.phase != phaseIdle {
		t.Fatal("nil classifier must not start a working phase")
	}
	for _, act := range a.activity.List() {
		if strings.HasPrefix(act.Label, "agent design:") {
			t.Fatalf("nil classifier must not register a design activity: %+v", act)
		}
	}
	last := a.messages[len(a.messages)-1]
	if !strings.Contains(last.Text(), "no classifier configured") {
		t.Fatalf("expected nil-classifier system line, got %q", last.Text())
	}
}

func TestAgentCreateBuilderFailureSavesStubAndOpensEditor(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{err: errBoom})

	cmd := a.handleCommand("/agent create fallback-bot")
	if cmd == nil {
		t.Fatal("/agent create returned no command")
	}

	var done agentBuilderDoneMsg
	batch := cmd().(tea.BatchMsg)
	for _, c := range batch {
		if msg := c(); msg != nil {
			if dm, ok := msg.(agentBuilderDoneMsg); ok {
				done = dm
			}
		}
	}
	if done.name != "fallback-bot" || done.err == nil {
		t.Fatalf("builder message = %+v, want fallback-bot error", done)
	}

	a.handleAgentBuilderDone(done)
	if a.view != viewAgent {
		t.Fatalf("view = %v, want viewAgent", a.view)
	}
	if !a.agentState.editMode {
		t.Fatal("expected editor to open on the fallback stub")
	}
	loaded, err := agentprofile.Load("fallback-bot")
	if err != nil {
		t.Fatalf("fallback stub not saved: %v", err)
	}
	if loaded.Name != "fallback-bot" || loaded.Mode != agentprofile.ModeSingle {
		t.Fatalf("fallback stub = %+v", loaded)
	}
}

func TestAgentCreateSuccessForcesRequestedName(t *testing.T) {
	agentTestHome(t)
	a := New(Options{})
	a.SetClassifier(&fakeClassifier{raw: validAgentBuilderJSON})

	cmd := a.handleCommand("/agent create requested-bot")
	if cmd == nil {
		t.Fatal("/agent create returned no command")
	}

	var done agentBuilderDoneMsg
	batch := cmd().(tea.BatchMsg)
	for _, c := range batch {
		if msg := c(); msg != nil {
			if dm, ok := msg.(agentBuilderDoneMsg); ok {
				done = dm
			}
		}
	}
	if done.err != nil {
		t.Fatalf("builder error: %v", done.err)
	}
	if done.profile.Name != "requested-bot" {
		t.Fatalf("profile name = %q, want requested-bot", done.profile.Name)
	}
	if done.name != "requested-bot" {
		t.Fatalf("message name = %q", done.name)
	}
}
