package tui

import (
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/tui/components"
)

// agentLook is how an agent presents itself: the display name and colours its
// profile carries (internal/agentprofile, identity.go). Both are cosmetic. The
// TUI wears them while the agent is engaged, and nothing about what the agent
// may do depends on them.
type agentLook struct {
	Display string
	Palette []string
}

// lookupAgentLook reads the persona of a profile by name. The persona lives in
// the agent-profile tree, so it is looked up there even when a flat profile of
// the same name carries the prompt (belai:debug is both). A profile with no
// persona, or no such definition, gives the zero look.
func lookupAgentLook(name string) agentLook {
	if name == "" {
		return agentLook{}
	}
	p, err := agentprofile.Load(name)
	if err != nil {
		return agentLook{}
	}

	return agentLook{Display: p.DisplayName, Palette: p.Palette}
}

// agentRole is the profile name without the built-in prefix: "patcher".
func agentRole(name string) string { return strings.TrimPrefix(name, "belai:") }

// title is the short form for the footer chip: the display name, else the
// profile name.
func (l agentLook) title(name string) string {
	if l.Display != "" {
		return l.Display
	}

	return name
}

// label is the form for a picker chip, the same the website uses: the display
// name with the role beside it ("Kremvax (patcher)"), else the profile name.
func (l agentLook) label(name string) string {
	if l.Display != "" {
		return l.Display + " (" + agentRole(name) + ")"
	}

	return name
}

// notice is the form for the line that says an agent was engaged: the display
// name with the exact profile name beside it, so the name to type is never lost.
func (l agentLook) notice(name string) string {
	if l.Display != "" {
		return l.Display + " (" + name + ")"
	}

	return name
}

// syncPersona dresses the TUI in the engaged agent's colours, and takes them
// off again when none is engaged (or the agent is dormant outside agent mode).
// Every route that changes the engaged agent ends in refreshFooter, which calls
// this, so ctrl+p, the picker, /agent, a new or resumed session and startup all
// retheme from one place. It only does work when the agent changed.
func (a *App) syncPersona() {
	name := a.engagedAgent()
	if name == a.personaFor {
		return
	}
	a.personaFor = name
	components.ApplyPersona(lookupAgentLook(name).Palette)
}
