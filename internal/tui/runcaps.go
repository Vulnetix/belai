package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/budget"
)

// runCapsMeter is the session's run-cap meter (docs/run-caps.md), or nil when no
// spend, token or wall cap is set. It outlives a rebuild of the agent session,
// so changing a setting does not reset what was spent; it is made again for a
// new or resumed session id, which restores the counters saved under that id.
func (a *App) runCapsMeter() *budget.Meter {
	if !a.settings.RunCapsActive() {
		a.runCaps, a.runCapsID = nil, ""
		return nil
	}
	caps := budget.CapsFromSettings(a.settings)
	if a.runCaps == nil || a.runCapsID != a.sessionID {
		a.runCaps = budget.NewMeter(caps)
		a.runCaps.Persist(a.sessionID)
		a.runCapsID, a.runCapsBase = a.sessionID, caps
		return a.runCaps
	}
	if caps != a.runCapsBase {
		a.runCaps.SetCaps(caps)
		a.runCapsBase = caps
	}
	return a.runCaps
}

// capFooterLabel is the footer chip: the largest used share of a cap, empty
// while none is set. warn is true once the model has been told to finalise.
func (a *App) capFooterLabel() (label string, warn bool) {
	m := a.runCapsMeter()
	if m == nil {
		return "", false
	}
	reason, pct := m.Share()
	if reason == "" {
		return "", false
	}
	return fmt.Sprintf("cap %s %d%%", reason, pct), float64(pct)/100 >= a.settings.RunCapFinalizeAt()
}

// capsCommand is /caps: with no argument or "status" it says what the caps are
// and how much is used; "extend" raises them by half of their original values,
// while extensions remain. It is typed by the user and is never available to the
// website, a model or a tool.
func (a *App) capsCommand(arg string) tea.Cmd {
	m := a.runCapsMeter()
	if m == nil {
		a.addSystem("run caps: off (set run_caps.max_cost_usd, run_caps.max_tokens or run_caps.max_wall_minutes in settings, or start with -max-cost, -max-tokens or -max-wall)")
		return nil
	}
	switch strings.TrimSpace(arg) {
	case "", "status":
		a.addSystem(fmt.Sprintf("run caps: %s; finalise at %d%%; %d of %d extensions left (/caps extend)",
			m.Describe(), int(a.settings.RunCapFinalizeAt()*100+0.5), m.ExtendsLeft(), m.Caps().MaxExtends))
	case "extend":
		if !m.Extend() {
			a.addSystem("run caps: no extensions left (run_caps.max_extends)")
			return nil
		}
		_ = m.Save()
		a.addSystem("run caps: extended by half of each cap; now " + m.Describe())
	default:
		a.addSystem("usage: /caps [status|extend]")
	}
	return nil
}
