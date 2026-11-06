package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// finishReviewCmd is /finish-review: with no argument, or status, it reports the
// finish review gate (docs/finish-review.md); with on or off it switches the
// gate for this session. The switch is the user's own, typed here, so it may
// turn the gate off whatever a settings layer says; it is held in memory,
// re-applied over every settings reload and never written to a settings file. A
// web session cannot send it.
func (a *App) finishReviewCmd(arg string) tea.Cmd {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "status":
		a.addSystem(a.finishReviewStatus())
	case "on", "off":
		on := strings.EqualFold(strings.TrimSpace(arg), "on")
		a.finishReviewOverride = &on
		a.applyFinishReviewOverride()
		a.invalidateAgentSession()
		a.addSystem("finish review: " + onOff(on) + " for this session")
	default:
		a.addSystem("finish review: use /finish-review, /finish-review status, /finish-review on or /finish-review off")
	}
	return nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// applyFinishReviewOverride lays the session's /finish-review choice over the
// resolved settings.
func (a *App) applyFinishReviewOverride() {
	if a.finishReviewOverride == nil {
		return
	}
	f := config.FinishReviewSettings{}
	if a.settings.FinishReview != nil {
		f = *a.settings.FinishReview
	}
	on := *a.finishReviewOverride
	f.Enabled = &on
	a.settings.FinishReview = &f
}

func (a *App) finishReviewStatus() string {
	s := a.settings
	b := &strings.Builder{}
	b.WriteString("finish review: " + onOff(s.FinishReviewEnabled()))
	b.WriteString("; up to " + strconv.Itoa(s.FinishReviewMaxRounds()) + " round(s) sent back")
	b.WriteString("; " + strconv.Itoa(len(s.FinishReviewScratchPatterns())) + " extra scratch pattern(s)")
	if a.finishReviewOverride != nil {
		b.WriteString("; set by /finish-review for this session")
	} else if src, ok := a.eff.Origin["finish_review"]; ok {
		b.WriteString("; set in " + string(src))
	}
	return b.String()
}
