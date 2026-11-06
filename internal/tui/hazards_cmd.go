package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/shellsafe"
	"github.com/vulnetix/belai/internal/tools"
)

// hazardsCommand prints the hazard floor as the session holds it: whether it is
// on, whether ask off waives it, every identifier, the fixed protected paths and
// the user's own additions. It is a read-only view of harness tables and the
// user's settings; it sends nothing to a model and changes nothing.
func (a *App) hazardsCommand(arg string) tea.Cmd {
	if strings.TrimSpace(arg) != "" {
		a.addSystem("usage: /hazards  (lists the hazard rails: the calls that always ask; it changes nothing)")
		return nil
	}
	a.addSystem(hazardsReport(a.settings.HazardAskOn(), a.settings.HazardYoloWaived(), a.settings.ProtectedPathList()))
	return nil
}

// hazardsReport composes the /hazards text from the closed tables and the
// user's settings, every user string reduced to one cleaned line.
func hazardsReport(on, waived bool, userPaths []string) string {
	var b strings.Builder
	state := "on"
	if !on {
		state = "off (permissions.hazard_ask is false in your settings)"
	}
	b.WriteString("hazard rails: " + state + "\n")
	if waived {
		b.WriteString("ask off waives them (permissions.hazard_yolo_waive is true in your settings)\n")
	} else {
		b.WriteString("ask off, /yolo and an allow rule do not waive them; only a person approves\n")
	}
	b.WriteString("shell hazards: " + strings.Join(shellsafe.HazardIDs, ", ") + "\n")
	b.WriteString("file classes: " + strings.Join(tools.HazardPathClasses, ", ") + "\n")
	byClass := map[string][]string{}
	for _, r := range tools.ProtectedRules {
		byClass[r.Class] = append(byClass[r.Class], r.Match)
	}
	for _, c := range tools.HazardPathClasses {
		if m := byClass[c]; len(m) > 0 {
			b.WriteString(fmt.Sprintf("  %s: %s\n", c, strings.Join(m, ", ")))
		}
	}
	if len(userPaths) == 0 {
		b.WriteString("your protected paths: none (permissions.protected_paths)")
	} else {
		clean := make([]string, len(userPaths))
		for i, p := range userPaths {
			clean[i] = sanitize.Line(p, 200)
		}
		b.WriteString("your protected paths: " + strings.Join(clean, ", "))
	}
	return b.String()
}
