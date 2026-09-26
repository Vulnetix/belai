package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/tui/components"
)

func TestResumeRestoresModelProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	// Simulate no explicit CLI flags so the session record can apply.
	a.flags = config.Settings{}
	entries := basicSessionEntries()
	entries[1].Meta["model"] = "gpt-4o"
	seedEntries(t, a, key, "sess-1", entries)

	a.resumeSession(key, "sess-1")

	if a.cfg.Model != "gpt-4o" || a.cfg.Provider != "openai" {
		t.Fatalf("cfg = %q/%q, want gpt-4o/openai", a.cfg.Model, a.cfg.Provider)
	}
}

func TestResumeCLIFlagBeatsSessionRecord(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test")
	workdir := t.TempDir()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Provider: "openai", Model: "cli-model", Workdir: workdir})
	key, _ := session.KeyFor(workdir)
	entries := basicSessionEntries()
	entries[1].Meta["model"] = "session-model"
	seedEntries(t, a, key, "sess-1", entries)

	a.resumeSession(key, "sess-1")

	if a.cfg.Model != "cli-model" {
		t.Fatalf("cfg.Model = %q, want cli-model (CLI flag wins)", a.cfg.Model)
	}
}

func TestResumeUnconfiguredProviderWarnsAndKeepsCurrent(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir) // openai/gpt-5, no key
	key, _ := session.KeyFor(workdir)
	a.flags = config.Settings{}
	entries := basicSessionEntries()
	entries[1].Meta["provider"] = "anthropic"
	entries[1].Meta["model"] = "claude-sonnet-4-5"
	seedEntries(t, a, key, "sess-1", entries)

	a.resumeSession(key, "sess-1")

	if a.cfg.Provider != "openai" {
		t.Fatalf("cfg.Provider = %q, want openai (kept current)", a.cfg.Provider)
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Text(), "not configured") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected an unconfigured-provider warning")
	}
}

func TestResumeStickyPlanModeRestored(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	entries := basicSessionEntries()
	entries = append(entries, modes.PlanState{Enabled: true, Todos: []modes.Todo{{N: 1, Text: "step one"}}}.ToEntry("a1"))
	seedEntries(t, a, key, "sess-plan", entries)

	a.resumeSession(key, "sess-plan")

	if a.mode != "plan" || !a.modeSticky {
		t.Fatalf("mode/modeSticky = %q/%v, want plan/true", a.mode, a.modeSticky)
	}
	if !strings.Contains(a.lastPlanText, "step one") {
		t.Fatalf("lastPlanText = %q", a.lastPlanText)
	}
	if a.pendingPlanExecute {
		t.Fatal("pendingPlanExecute must stay false after resume")
	}
}

func TestResumeCarrierNamesRestored(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	key, _ := session.KeyFor(workdir)
	entries := basicSessionEntries()
	entries = append(entries, session.Meta{Schema: 2, Cwd: workdir, ActivePlan: "p1", ActiveGoal: "g1", ActiveProfile: "prof"}.ToEntry("a1"))
	seedEntries(t, a, key, "sess-1", entries)

	a.resumeSession(key, "sess-1")

	if a.state.ActivePlan != "p1" || a.state.ActiveGoal != "g1" || a.state.ActiveProfile != "prof" {
		t.Fatalf("state = plan:%q goal:%q profile:%q", a.state.ActivePlan, a.state.ActiveGoal, a.state.ActiveProfile)
	}
	if a.namedAgent != "prof" {
		t.Fatalf("namedAgent = %q, want prof", a.namedAgent)
	}
}

func TestResumeCrossProjectClearsCarrierNames(t *testing.T) {
	workdirA := t.TempDir()
	workdirB := t.TempDir()
	a := newResumeApp(t, workdirA)
	keyB, _ := session.KeyFor(workdirB)
	entries := basicSessionEntries()
	entries = append(entries, session.Meta{Schema: 2, Cwd: workdirB, ActivePlan: "p1", ActiveGoal: "g1"}.ToEntry("a1"))
	seedEntries(t, a, keyB, "sess-origin", entries)

	a.resumeSession(keyB, "sess-origin")

	if a.state.ActivePlan != "" || a.state.ActiveGoal != "" {
		t.Fatalf("carrier names not cleared on fork: %q/%q", a.state.ActivePlan, a.state.ActiveGoal)
	}
	found := false
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Text(), "origin project") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a cross-project carrier warning")
	}
}

func TestOfferCompactionShownAboveThreshold(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	a.classifier = &fakeClassifier{raw: "SAFE"}
	a.settings.ContextWindows = map[string]int{"gpt-5": 200}
	a.messages = []components.Message{
		{Role: "user", Content: strings.Repeat("x", 500)},
		{Role: "assistant", Content: strings.Repeat("y", 500)},
	}

	a.offerCompactionCmd()

	if a.view != viewResumeCompact {
		t.Fatalf("view = %v, want resume-compact offer", a.view)
	}
}

func TestOfferCompactionSkippedBelowThreshold(t *testing.T) {
	workdir := t.TempDir()
	a := newResumeApp(t, workdir)
	a.classifier = &fakeClassifier{raw: "SAFE"}
	a.settings.ContextWindows = map[string]int{"gpt-5": 1_000_000}
	a.messages = []components.Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok"},
	}

	a.offerCompactionCmd()

	if a.view != viewChat {
		t.Fatalf("view = %v, want no offer below threshold", a.view)
	}
}

func TestResumeCompactEnterCallsCompact(t *testing.T) {
	a := New(Options{Provider: "openai", Model: "gpt-5"})
	a.view = viewResumeCompact
	a.viewStack = []viewState{viewResumeCompact}

	_, cmd := a.handleResumeCompactKey(tea.KeyMsg{Type: tea.KeyEnter})

	if a.view != viewChat {
		t.Fatalf("view = %v, want chat after enter", a.view)
	}
	if cmd == nil {
		t.Fatal("enter should return the compaction command")
	}
}

func TestRehydrateRestoresUsageAnchor(t *testing.T) {
	entries := []session.Entry{
		{ID: "u1", Type: "user", Role: "user", Content: "hello"},
		{ID: "a1", ParentID: "u1", Type: "assistant", Role: "assistant", Content: "hi", Meta: map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12}},
	}
	r := rehydrateSession(entries)
	msgs := make([]transcript.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		msgs = append(msgs, transcript.Message{Role: m.Role, Content: m.Text(), Usage: m.Usage})
	}
	if est := transcript.EstimateContext(msgs); est.LastUsageIndex < 0 {
		t.Fatal("rehydrated usage should anchor the context estimate")
	}
}
