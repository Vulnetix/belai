package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestScratchEnterSequence(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	for _, kv := range [][2]string{
		{"OPENAI_API_KEY", "sk-openai"},
		{"ANTHROPIC_API_KEY", "sk-ant"},
		{"OPENROUTER_API_KEY", "or"},
		{"GROQ_API_KEY", "gr"},
		{"DEEPSEEK_API_KEY", "ds"},
		{"MISTRAL_API_KEY", "ms"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir, Resolver: newTestResolver(t, workdir)})
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	_ = a.enterModel()

	// Let the availability probe land (enterModel started it).
	if cmd := a.availabilityCmdIfStale(); cmd != nil {
		a.handleAvailability(cmd().(availabilityMsg))
	}
	a.cfg.Provider = "openai"

	// Render once so modelState.rows is populated, like the real view does.
	_ = a.modelView()

	a.modelState.agentScope = "session"
	a.modelState.selected = 0
	seen := []string{a.cfg.Provider}
	for i := 0; i < 30; i++ {
		_, _ = a.handleModelKey(tea.KeyMsg{Type: tea.KeyEnter})
		_ = a.modelView() // re-render refreshes cached rows
		seen = append(seen, a.cfg.Provider)
	}
	t.Logf("sequence (%d steps): %v", len(seen)-1, seen)
	t.Logf("row opts: %v", a.modelState.rows[0].opts)
}
