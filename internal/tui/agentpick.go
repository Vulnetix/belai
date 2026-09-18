package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/tui/components"
)

// The agent picker is the slash-completion popup's sibling: a strip above
// the composer listing the agent profiles that can carry this turn's system
// prompt. It shows in agent mode, where a profile is the thing that changes
// what the turn does, and is driven by the same three keys — tab highlights,
// right accepts, enter selects.
//
// The picker is opened explicitly by the /agent command or by pressing enter
// in agent mode while no agent is engaged. It is no longer driven by typing @
// or @agent:, which now belongs to the file chooser.
//
// The names come from internal/profiles (the flat Name/Content profiles that
// reach the system prompt as a carrier), not from internal/agentprofile, which
// defines background agents and never touches the system prompt.

// noAgentSelection is the agentIndex value meaning "no candidate is
// highlighted", mirroring noAutocompleteSelection.
const noAgentSelection = -1

// agentNoneLabel is the pseudo-candidate that clears the selection, so the
// picker can undo itself without a command.
const agentNoneLabel = "(none)"

// agentChoice is one pickable agent. Built-ins and background-agent
// definitions are drawn differently so a harness profile, a file the user
// wrote, and a definition that can also run on its own are never confused.
type agentChoice struct {
	Name    string
	Builtin bool
	// Background marks an internal/agentprofile definition. Engaging one
	// carries its system_prompt here; ctrl+g starts it as a background agent
	// instead, which is the only thing it can do that a flat profile cannot.
	Background bool
	// Tools is the definition's tool allowlist, honoured when it carries a
	// foreground turn. Empty means every registered tool.
	Tools []string
}

// loadAgents refreshes the pickable list from both trees: the flat profiles
// that carry a system prompt, then the background-agent definitions. A read
// failure drops that source rather than failing the frame: this is chrome,
// not a turn.
func (a *App) loadAgents() {
	a.agentIndex = noAgentSelection
	var builtins, users []agentChoice
	if list, err := profiles.List(); err == nil {
		for _, p := range list {
			c := agentChoice{Name: p.Name, Builtin: p.Builtin}
			if p.Builtin {
				builtins = append(builtins, c)
			} else {
				users = append(users, c)
			}
		}
	}

	// Built-ins first: signet:debug is the default agent and always sits at
	// index 0. User profiles follow, then background-agent definitions.
	sort.Slice(builtins, func(i, j int) bool { return builtins[i].Name < builtins[j].Name })
	for i, c := range builtins {
		if c.Name == profiles.DebugProfile {
			builtins[0], builtins[i] = builtins[i], builtins[0]
			break
		}
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Name < users[j].Name })

	choices := make([]agentChoice, 0, len(builtins)+len(users))
	choices = append(choices, builtins...)
	choices = append(choices, users...)

	names := make(map[string]struct{}, len(choices))
	for _, c := range choices {
		names[c.Name] = struct{}{}
	}
	if list, err := agentprofile.List(); err == nil {
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		for _, p := range list {
			// A flat profile owns the name: it is the one CarrierOptions
			// resolves first, so offering a shadowed definition would engage
			// something other than the row the user picked.
			if _, ok := names[p.Name]; ok {
				continue
			}
			choices = append(choices, agentChoice{Name: p.Name, Background: true, Tools: p.Tools})
		}
	}

	a.agents = choices
}

// openAgentPicker shows the agent strip above the composer and selects the
// default signet:debug profile when it is available.
func (a *App) openAgentPicker() {
	a.agentPickerOpen = true
	a.agentIndex = 0
	for i, c := range a.agents {
		if c.Name == profiles.DebugProfile {
			a.agentIndex = i
			return
		}
	}
}

// startAgentChoice runs a background-agent definition as a background agent,
// the one thing the flat profiles cannot do. It is deliberately a separate key
// from engaging: starting a loop does not answer the prompt in the composer.
func (a *App) startAgentChoice(c agentChoice) tea.Cmd {
	if !c.Background {
		a.addSystem(c.Name + " is a profile, not a background agent — enter engages it here")
		return nil
	}
	if a.bgManager == nil {
		a.addSystem("background agents need configured credentials")
		return nil
	}
	p, err := agentprofile.Load(c.Name)
	if err != nil {
		a.addSystem("agent: " + err.Error())
		return nil
	}
	if err := a.bgManager.Start(c.Name, p); err != nil {
		a.addSystem("agent start failed: " + err.Error())
		return nil
	}
	a.agentIndex = noAgentSelection
	a.addSystem("agent started in the background: " + c.Name)
	return nil
}

// agentCandidates returns the profiles the strip is currently offering.
func (a *App) agentCandidates() []agentChoice {
	return a.agents
}

// agentPickerVisible reports whether the strip has anything to draw. The slash
// popup wins when both could show, and the file chooser wins when an @-prefix
// is being typed, because @ is now reserved for file references.
func (a *App) agentPickerVisible() bool {
	if a.view != viewChat || a.mode != "agent" || len(a.autocomplete) > 0 {
		return false
	}
	if a.filePickerVisible() {
		return false
	}
	return a.agentPickerOpen && len(a.agents) > 0
}

// engagedAgent returns the agent carrying turns right now, which is nothing
// outside agent mode. Plan and goal mode carry the active plan or goal instead
// — that is Signet's own logic, and the system prompt holds exactly one
// carrier — so an engaged agent is dormant there rather than cleared: cycling
// agent → plan → goal → agent gets the selection back, and nothing in between
// shows it or sends it.
func (a *App) engagedAgent() string {
	if a.mode != "agent" {
		return ""
	}
	return a.namedAgent
}

// engagedAgentTools is the engaged agent's tool allow-list, and likewise empty
// outside agent mode: plan mode has its own read-only registry and goal mode
// the full one, neither of which an agent profile may narrow.
func (a *App) engagedAgentTools() []string {
	if a.mode != "agent" {
		return nil
	}
	return a.namedAgentTools
}

// agentSelection returns the highlighted candidate, if there is one.
func (a *App) agentSelection() (agentChoice, bool) {
	cands := a.agentCandidates()
	if a.agentIndex < 0 || a.agentIndex >= len(cands)+1 {
		return agentChoice{}, false
	}
	if a.agentIndex == len(cands) {
		return agentChoice{Name: agentNoneLabel}, true
	}
	return cands[a.agentIndex], true
}

// cycleAgent moves the highlight to the next candidate, wrapping through the
// (none) entry so the picker can also clear a selection.
func (a *App) cycleAgent() tea.Cmd {
	cands := a.agentCandidates()
	if len(cands) == 0 {
		return nil
	}
	if a.agentIndex < 0 {
		a.agentIndex = 0
	} else {
		a.agentIndex++
	}
	// len(cands) is the (none) slot; one past it wraps to the first name.
	if a.agentIndex > len(cands) {
		a.agentIndex = 0
	}
	return nil
}

// acceptAgent engages the highlighted profile for every following turn — or
// clears the engaged one on (none) — and closes the picker.
func (a *App) acceptAgent() tea.Cmd {
	choice, ok := a.agentSelection()
	if !ok {
		return nil
	}
	a.agentPickerOpen = false
	a.agentIndex = noAgentSelection
	if choice.Name == agentNoneLabel {
		if a.namedAgent == "" {
			return nil
		}
		a.namedAgent = ""
		a.namedAgentTools = nil
		a.invalidateAgentSession()
		a.addSystem("agent cleared")
		a.refreshFooter()
		return nil
	}
	a.namedAgent = choice.Name
	// A background definition's tool allowlist follows it into the foreground:
	// a read-only definition must stay read-only wherever it runs. The tools
	// are fixed when the session is built, so the cached one has to go.
	a.namedAgentTools = choice.Tools
	a.invalidateAgentSession()
	a.mode = "agent"
	msg := "agent: " + choice.Name
	if choice.Background {
		msg += " (background definition; ctrl+g starts it in the background instead)"
	}
	a.addSystem(msg)
	a.refreshFooter()
	a.relayout()
	return nil
}

// renderAgentPicker draws the candidates as a chip row, highlighting the
// current one and marking built-ins with ◈ so a harness profile reads
// differently from a file the user wrote.
func (a *App) renderAgentPicker() string {
	cands := a.agentCandidates()
	parts := make([]string, 0, len(cands)+1)
	for i, c := range cands {
		label := c.Name
		switch {
		case c.Builtin:
			label = "◈ " + label
		case c.Background:
			label = "↻ " + label
		}
		switch {
		case i == a.agentIndex:
			parts = append(parts, components.Chip(label, components.ColorTealSoft))
		case c.Name == a.namedAgent:
			parts = append(parts, components.EmphStyle.Render(label))
		case c.Builtin:
			// Built-ins are chrome the user did not write: muted, never keycap
			// bright, so the file-backed profiles read first.
			parts = append(parts, components.MutedStyle.Render(label))
		case c.Background:
			parts = append(parts, components.WarnStyle.Render(label))
		default:
			parts = append(parts, components.KeyStyle.Render(label))
		}
	}
	if a.namedAgent != "" || a.agentIndex == len(cands) {
		none := components.MutedStyle.Render(agentNoneLabel)
		if a.agentIndex == len(cands) {
			none = components.Chip(agentNoneLabel, components.ColorTealSoft)
		}
		parts = append(parts, none)
	}
	line := components.MutedStyle.Render("⌂ ") + strings.Join(parts, components.MutedStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(a.contentWidth()).Render(line)
}
