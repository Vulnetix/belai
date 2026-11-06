package tui

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
)

// lintLoopCmd is /lint-loop: with no argument, or status, it reports the
// per-edit lint loop (docs/lint.md); with on or off it switches the loop for
// this session. The switch is the user's own, typed here, so it may turn the
// loop on whatever a project file says; it is held in memory, re-applied over
// every settings reload and never written to a settings file. A web session
// cannot send it.
func (a *App) lintLoopCmd(arg string) tea.Cmd {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "status":
		a.addSystem(a.lintLoopStatus())
	case "on", "off":
		on := strings.EqualFold(strings.TrimSpace(arg), "on")
		a.lintLoopOverride = &on
		a.applyLintLoopOverride()
		a.invalidateAgentSession()
		a.addSystem("lint loop: " + onOff(on) + " for this session")
	default:
		a.addSystem("lint loop: use /lint-loop, /lint-loop status, /lint-loop on or /lint-loop off")
	}
	return nil
}

// applyLintLoopOverride lays the session's /lint-loop choice over the resolved
// settings.
func (a *App) applyLintLoopOverride() {
	if a.lintLoopOverride == nil {
		return
	}
	l := config.LintLoopSettings{}
	if a.settings.LintLoop != nil {
		l = *a.settings.LintLoop
	}
	on := *a.lintLoopOverride
	l.Enabled = &on
	a.settings.LintLoop = &l
}

func (a *App) lintLoopStatus() string {
	s := a.settings
	b := &strings.Builder{}
	b.WriteString("lint loop: " + onOff(s.LintLoopEnabled()))
	b.WriteString("; timeout " + strconv.Itoa(s.LintLoopTimeoutMS()) + " ms")
	b.WriteString("; up to " + strconv.Itoa(s.LintLoopMaxRows()) + " row(s) per file")
	b.WriteString("; " + strconv.Itoa(s.LintLoopMaxAttempts()) + " pass(es) on the same findings")
	if a.lintLoopOverride != nil {
		b.WriteString("; set by /lint-loop for this session")
	} else if src, ok := a.eff.Origin["lint_loop"]; ok {
		b.WriteString("; set in " + string(src))
	}
	return b.String()
}
