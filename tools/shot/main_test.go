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
		"agent-turn":      agentTurn,
		"plan-review":     planReview,
		"approval-diff":   approvalDiff,
		"settings":        settings,
		"permissions":     permissions,
		"agents-roster":   agentsRoster,
		"model-picker":    modelPicker,
		"local-model":     localModel,
		"exit-card":       exitCard,
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
		if !strings.Contains(bannerComposer(), "S I G N E T") {
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

	t.Run("agent turn shows the Bash tool and its preview", func(t *testing.T) {
		got := agentTurn()
		if !strings.Contains(got, "Bash") {
			t.Error("agent turn missing the Bash tool row")
		}
	})

	t.Run("plan review shows the three actions", func(t *testing.T) {
		got := planReview()
		for _, want := range []string{"approve", "refine", "cancel"} {
			if !strings.Contains(got, want) {
				t.Errorf("plan review missing %q", want)
			}
		}
	})

	t.Run("exit card carries the resume command", func(t *testing.T) {
		if !strings.Contains(exitCard(), "--resume") {
			t.Error("exit card missing the resume command")
		}
	})
}
