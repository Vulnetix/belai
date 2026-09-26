package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/profiles"
	"github.com/vulnetix/belai/internal/tui/components"
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

	// Built-ins first: belai:debug is the default agent and always sits at
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
// default belai:debug profile when it is available.
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
	a.agentIndex = noAgentSelection
	return a.startAgentProfile(c.Name)
}

// agentCandidates returns the profiles the strip is currently offering: the
// names valid for a /agent argument while one is being typed, otherwise every
// carrier the session can engage.
func (a *App) agentCandidates() []agentChoice {
	if a.agentArgSub != "" {
		return a.agentArgCands
	}
	return a.agents
}

// agentArgVerbs are the /agent subcommands that take a <name>, mapped to the
// tree that name has to come from: true means an agent the background manager
// is already running, false a profile on disk.
var agentArgVerbs = map[string]bool{
	"start":  false,
	"edit":   false,
	"stop":   true,
	"pause":  true,
	"resume": true,
	"log":    true,
}

// parseAgentArg splits a composer line into the /agent subcommand and the
// partial name typed after it. The space following the verb is required: until
// it is there the line is still completing the subcommand itself, which the
// slash chips handle.
func parseAgentArg(line string) (sub, prefix string, ok bool) {
	body, found := strings.CutPrefix(line, "/agent ")
	if !found {
		return "", "", false
	}
	sub, prefix, found = strings.Cut(body, " ")
	if !found {
		return "", "", false
	}
	if _, known := agentArgVerbs[sub]; !known {
		return "", "", false
	}
	// A profile name holds no whitespace, so a second word means the line has
	// moved past the argument the picker can complete.
	if strings.ContainsAny(prefix, " \t") {
		return "", "", false
	}
	return sub, prefix, true
}

// refreshAgentArgPicker opens, narrows, or closes the argument picker for the
// current composer line. Nothing is highlighted until tab is pressed: enter on
// a fully typed line must run the name the user wrote, never a prefix sibling
// the strip happened to list first.
func (a *App) refreshAgentArgPicker() {
	sub, prefix, ok := parseAgentArg(a.editor.Value())
	if !ok {
		a.closeAgentArgPicker()
		return
	}
	cands := a.agentArgCandidates(sub, prefix)
	if len(cands) == 0 {
		a.closeAgentArgPicker()
		return
	}
	if sub != a.agentArgSub || !sameAgentNames(cands, a.agentArgCands) {
		a.agentIndex = noAgentSelection
	}
	a.agentArgSub = sub
	a.agentArgCands = cands
	a.agentPickerOpen = true
	a.agentPickerSubmit = false
}

// agentArgCandidates returns the names valid for one /agent subcommand,
// narrowed to prefix.
func (a *App) agentArgCandidates(sub, prefix string) []agentChoice {
	var out []agentChoice
	if agentArgVerbs[sub] {
		if a.bgManager == nil {
			return nil
		}
		live := a.bgManager.List()
		sort.Slice(live, func(i, j int) bool { return live[i].Name < live[j].Name })
		for _, s := range live {
			if strings.HasPrefix(s.Name, prefix) {
				out = append(out, agentChoice{Name: s.Name, Background: true})
			}
		}
		return out
	}
	for _, c := range a.agents {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// closeAgentArgPicker returns the strip to its carrier role. It is a no-op
// outside argument mode, so it never closes a picker /agent or enter opened.
func (a *App) closeAgentArgPicker() {
	if a.agentArgSub == "" {
		return
	}
	a.agentArgSub = ""
	a.agentArgCands = nil
	a.agentPickerOpen = false
	a.agentIndex = noAgentSelection
}

// sameAgentNames reports whether two candidate lists offer the same names in
// the same order, which is what decides whether a highlight can survive.
func sameAgentNames(a, b []agentChoice) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

// acceptAgentArg completes the composer line with the highlighted name and
// runs it. This is not a carrier choice: nothing is engaged, and the command
// does whatever it does to the agent that was named.
func (a *App) acceptAgentArg() tea.Cmd {
	choice, ok := a.agentSelection()
	if !ok {
		return nil
	}
	line := "/agent " + a.agentArgSub + " " + choice.Name
	a.recordInput(line)
	a.closeAgentArgPicker()
	a.editor.Reset()
	a.clearAutocomplete()
	return a.handleCommand(line)
}

// agentPickerVisible reports whether the strip has anything to draw. The slash
// popup wins when both could show, and the file chooser wins when an @-prefix
// is being typed, because @ is now reserved for file references.
func (a *App) agentPickerVisible() bool {
	if a.view != viewChat || len(a.autocomplete) > 0 || a.dirPickState.open {
		return false
	}
	// Naming an agent for a /agent subcommand is not choosing a carrier, so
	// the argument picker shows in every mode; the carrier picker belongs to
	// agent mode, where a profile is what changes the turn.
	if a.agentArgSub == "" && a.mode != "agent" {
		return false
	}
	if a.filePickerVisible() {
		return false
	}
	return a.agentPickerOpen && len(a.agentCandidates()) > 0
}

// engagedAgent returns the agent carrying turns right now, which is nothing
// outside agent mode. Plan and goal mode carry the active plan or goal instead
// — that is Belai's own logic, and the system prompt holds exactly one
// carrier — so an engaged agent is dormant there rather than cleared: nothing
// in between shows it or sends it. Shift+tab re-entering agent mode clears the
// selection (clearEngagedAgent); a direct mode assignment leaves it dormant.
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
		// (none) clears the engaged carrier. As a command argument it is not
		// a name, so argument mode stops one row short of it.
		if a.agentArgSub != "" {
			return agentChoice{}, false
		}
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
	// Argument mode has no (none) row, so it wraps a row earlier.
	last := len(cands)
	if a.agentArgSub != "" {
		last = len(cands) - 1
	}
	if a.agentIndex > last {
		a.agentIndex = 0
	}
	return nil
}

// cycleAgentFromChat engages the next available agent profile in agent mode.
// It is ctrl+p, the shortcut the mode chip shows after shift+tab clears the
// last-used profile: one key steps through the profiles without opening the
// picker, so a fresh carrier is always one press away.
func (a *App) cycleAgentFromChat() tea.Cmd {
	if a.mode != "agent" {
		return nil
	}
	if len(a.agents) == 0 {
		a.loadAgents()
	}
	if len(a.agents) == 0 {
		return nil
	}
	idx := -1
	for i, c := range a.agents {
		if c.Name == a.namedAgent {
			idx = i
			break
		}
	}
	choice := a.agents[(idx+1)%len(a.agents)]
	a.agentExplicit = true
	a.setNamedAgent(choice.Name)
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

// acceptAgent engages the highlighted profile for every following turn — or
// clears the engaged one on (none) — and closes the picker.
func (a *App) acceptAgent() tea.Cmd {
	if a.agentArgSub != "" {
		return a.acceptAgentArg()
	}
	choice, ok := a.agentSelection()
	if !ok {
		return nil
	}
	a.agentPickerOpen = false
	a.agentIndex = noAgentSelection
	// A picker opened by a submit attempt owes that submit an answer. (none)
	// leaves agent mode without a carrier, so the prompt stays in the composer
	// rather than starting a turn the picker exists to prevent.
	pendingSubmit := a.agentPickerSubmit
	a.agentPickerSubmit = false
	if choice.Name == agentNoneLabel {
		if a.namedAgent == "" {
			return nil
		}
		a.agentExplicit = false
		a.setNamedAgent("")
		a.invalidateAgentSession()
		a.addSystem("agent cleared")
		return nil
	}
	a.agentExplicit = true
	a.setNamedAgent(choice.Name)
	a.invalidateAgentSession()
	a.mode = "agent"
	msg := "agent: " + choice.Name
	if choice.Background {
		msg += " (background definition; ctrl+g starts it in the background instead)"
	}
	a.addSystem(msg)
	a.refreshFooter()
	a.relayout()
	if pendingSubmit {
		if input := strings.TrimSpace(a.editor.Value()); input != "" {
			return a.submitInput(input)
		}
	}
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
	if a.agentArgSub == "" && (a.namedAgent != "" || a.agentIndex == len(cands)) {
		none := components.MutedStyle.Render(agentNoneLabel)
		if a.agentIndex == len(cands) {
			none = components.Chip(agentNoneLabel, components.ColorTealSoft)
		}
		parts = append(parts, none)
	}
	line := components.MutedStyle.Render("⌂ ") + strings.Join(parts, components.MutedStyle.Render("  ·  "))
	return lipgloss.NewStyle().MaxWidth(a.contentWidth()).Render(line)
}
