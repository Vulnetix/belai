package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// setup mirrors the rendering environment main() builds: TrueColor lipgloss,
// dark-background adaptive colours, and stdout on a pty so Banner/ExitCard take
// their colour path. It returns a func that restores stdout and the environment.
func setup() func() {
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	return forceTTY()
}

func frames() map[string]func() string {
	return map[string]func() string{
		"banner-composer": bannerComposer,
		"footer":          footer,
		"footer-intel":    footerIntel,
		"intel":           intel,
		"agent-turn":      agentTurn,
		"plan-review":     planReview,
		"approval-diff":   approvalDiff,
		"settings":        settings,
		"permissions":     permissions,
		"agents-roster":   agentsRoster,
		"model-picker":    modelPicker,
		"classifier":      classifier,
		"local-model":     localModel,
		"exit-card":       exitCard,
		"kanban-pane":     kanbanPane,
		"kanban-board":    kanbanBoard,
		"role-manager":    roleManager,
		"budgets":         budgets,
	}
}

func TestFramesRenderNonEmpty(t *testing.T) {
	restore := setup()
	defer restore()

	for name, fn := range frames() {
		got := fn()
		if got == "" {
			t.Errorf("%s rendered empty", name)
		}
		if strings.ContainsRune(got, '\x00') {
			t.Errorf("%s contains a NUL byte", name)
		}
	}
}

func TestFramesAreDeterministic(t *testing.T) {
	restore := setup()
	defer restore()

	for name, fn := range frames() {
		first, second := fn(), fn()
		if first != second {
			t.Errorf("%s is not deterministic across renders", name)
		}
	}
}

func TestFramesCarryTrueColorSGR(t *testing.T) {
	restore := setup()
	defer restore()

	for name, fn := range frames() {
		got := fn()
		if !strings.Contains(got, "\x1b[38;2;") && !strings.Contains(got, "\x1b[48;2;") {
			t.Errorf("%s has no truecolour SGR sequences", name)
		}
	}
}

func TestFrameContent(t *testing.T) {
	restore := setup()
	defer restore()

	t.Run("banner shows the wordmark", func(t *testing.T) {
		if !strings.Contains(bannerComposer(), "belai") {
			t.Error("banner wordmark missing")
		}
	})

	t.Run("footer shows guardrails and the mode chip", func(t *testing.T) {
		got := footer()
		for _, want := range []string{"agent", "guardrails", "firewall"} {
			if !strings.Contains(got, want) {
				t.Errorf("footer missing %q", want)
			}
		}
	})

	t.Run("intel pane shows the limits, pace, trend and runway", func(t *testing.T) {
		got := intel()
		for _, want := range []string{"[ intel ]", "f12 close", "5-hour limit", "weekly limit", "resets in", "pace", "trend", "runway", "today", "this week", "last 30 days", "╹"} {
			if !strings.Contains(got, want) {
				t.Errorf("intel pane missing %q", want)
			}
		}
	})

	t.Run("footer intel shows the slot", func(t *testing.T) {
		got := footerIntel()
		for _, want := range []string{"intel", "today 34.4M", "5h 23%"} {
			if !strings.Contains(got, want) {
				t.Errorf("footer intel missing %q", want)
			}
		}
	})

	t.Run("agent turn shows the Bash tool and its preview", func(t *testing.T) {
		got := agentTurn()
		if !strings.Contains(got, "Bash") {
			t.Error("agent turn missing the Bash tool row")
		}
	})

	t.Run("plan review shows the three actions, the model and the estimates", func(t *testing.T) {
		got := planReview()
		for _, want := range []string{"approve", "refine", "cancel", "model:", "m change", "new session ≈"} {
			if !strings.Contains(got, want) {
				t.Errorf("plan review missing %q", want)
			}
		}
	})

	t.Run("role manager lists each described decision with its model", func(t *testing.T) {
		got := roleManager()
		for _, want := range []string{"ran Grep instead of Bash", "found 7", "jev-1.13 412ms", "decider-4b 1.8s", "claude-haiku-4.5 2.1s", "reused the earlier result"} {
			if !strings.Contains(got, want) {
				t.Errorf("role manager feed missing %q", want)
			}
		}
	})

	t.Run("budgets lists each scope with its remaining share", func(t *testing.T) {
		got := budgets()
		for _, want := range []string{"Token budgets", "esc back", "global", "(selected)", "session", "day", "month", "79% left", "38% left", "  0% left", "88% left", "deepseek-v4-flash"} {
			if !strings.Contains(got, want) {
				t.Errorf("budgets screen missing %q", want)
			}
		}
	})

	t.Run("exit card carries the resume command", func(t *testing.T) {
		if !strings.Contains(exitCard(), "--resume") {
			t.Error("exit card missing the resume command")
		}
	})
}
