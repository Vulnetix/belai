package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/bgproc"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/fuzzy"
	"github.com/vulnetix/signet/internal/processlib"
	"github.com/vulnetix/signet/internal/promptlib"
)

// The slash popup offers the saved libraries next to the commands: a prompt
// from the prompt library, an agent profile, or a supervised process from the
// process library, each under its own prefix. Picking one acts on the item
// directly — a library chip names a thing, not a command line to edit.
const (
	slashPromptPrefix  = "prompt:"
	slashAgentPrefix   = "agent:"
	slashProcessPrefix = "process:"
)

// slashLibCache holds the prompt and process libraries for the current slash
// session. Completion runs on every keystroke, so the directories are read
// once when the composer starts a slash line, not per key.
type slashLibCache struct {
	active    bool
	prompts   []promptlib.Entry
	processes []processlib.Entry
}

// slashCandidate is one popup entry: the line it completes to and the extra
// key a fuzzy query may match (the bare library name), so "/deploy" finds
// "/prompt:deploy".
type slashCandidate struct {
	line string
	name string
}

func (c slashCandidate) keys() []string {
	if c.name == "" {
		return []string{strings.TrimPrefix(c.line, "/")}
	}
	return []string{strings.TrimPrefix(c.line, "/"), c.name}
}

// loadSlashLib re-reads both libraries. A read failure drops that library
// from the popup rather than failing the keystroke: this is chrome.
func (a *App) loadSlashLib() {
	global, _ := promptlib.Load(config.ScopeGlobal, a.workdir)
	project, _ := promptlib.Load(config.ScopeProject, a.workdir)
	a.slashLib.prompts = promptlib.Enabled(promptlib.Merge(global.Entries, project.Entries))
	if promptlib.ExtraEntries != nil {
		a.slashLib.prompts = append(a.slashLib.prompts, promptlib.ExtraEntries()...)
	}

	// Every saved process is offered, disabled ones too: disabled only turns
	// off auto-start, and starting one by hand is the point of the chip.
	pg, _ := processlib.Load(config.ScopeGlobal, a.workdir)
	pp, _ := processlib.Load(config.ScopeProject, a.workdir)
	a.slashLib.processes = processlib.Merge(pg.Entries, pp.Entries)
}

// slashCompletions returns the popup candidates for the composer line: the
// commands, then the prompt, agent and process libraries, ranked together by
// fuzzy match. Equal scores keep that grouping, so a command sits ahead of a
// library entry that matches as well. A line with a space is completing a
// command argument, which only the registry knows about.
func (a *App) slashCompletions(input string) []string {
	if !strings.HasPrefix(input, "/") {
		a.slashLib.active = false
		return nil
	}
	if !a.slashLib.active {
		a.slashLib.active = true
		a.loadSlashLib()
	}
	body := strings.TrimPrefix(input, "/")
	if strings.Contains(body, " ") {
		return a.registry.Complete(input)
	}

	names := a.registry.Names()
	cands := make([]slashCandidate, 0, len(names)+len(a.slashLib.prompts)+len(a.agents)+len(a.slashLib.processes))
	for _, n := range names {
		cands = append(cands, slashCandidate{line: "/" + n})
	}
	for _, e := range a.slashLib.prompts {
		cands = append(cands, slashCandidate{line: "/" + slashPromptPrefix + e.Name, name: e.Name})
	}
	for _, c := range a.agents {
		cands = append(cands, slashCandidate{line: "/" + slashAgentPrefix + c.Name, name: c.Name})
	}
	for _, e := range a.slashLib.processes {
		cands = append(cands, slashCandidate{line: "/" + slashProcessPrefix + e.Name, name: e.Name})
	}

	ranked := fuzzy.Rank(body, cands, slashCandidate.keys)
	if len(ranked) == 0 {
		return nil
	}
	out := make([]string, len(ranked))
	for i, c := range ranked {
		out[i] = c.line
	}
	return out
}

// isLibraryLine reports whether a slash line names a library entry rather
// than a command.
func isLibraryLine(line string) bool {
	body := strings.TrimPrefix(strings.TrimSpace(line), "/")
	for _, p := range []string{slashPromptPrefix, slashAgentPrefix, slashProcessPrefix} {
		if strings.HasPrefix(body, p) {
			return true
		}
	}
	return false
}

// handleLibraryCommand runs a /prompt:, /agent: or /process: line. It reports
// false for any other line so the caller falls through to the registry. Only
// the first prefix is cut: agent names may themselves hold a colon
// (signet:debug).
func (a *App) handleLibraryCommand(line string) (tea.Cmd, bool) {
	body := strings.TrimPrefix(strings.TrimSpace(line), "/")
	if name, ok := strings.CutPrefix(body, slashPromptPrefix); ok {
		return a.runPromptEntry(strings.TrimSpace(name)), true
	}
	if name, ok := strings.CutPrefix(body, slashAgentPrefix); ok {
		return a.engageAgent(strings.TrimSpace(name)), true
	}
	if name, ok := strings.CutPrefix(body, slashProcessPrefix); ok {
		return a.runProcessEntry(strings.TrimSpace(name)), true
	}
	return nil, false
}

// runPromptEntry loads a saved prompt into the composer, exactly as browsing
// the library with up does: the text is the user's to edit and send, and
// loadedPrompt records the file so ctrl+s overwrites the right entry.
func (a *App) runPromptEntry(name string) tea.Cmd {
	entry, ok := findPrompt(a.slashLib.prompts, name)
	if !ok {
		a.loadSlashLib()
		entry, ok = findPrompt(a.slashLib.prompts, name)
	}
	if !ok {
		a.addSystem("prompt: no saved prompt named " + name)
		return nil
	}
	a.editor.SetValue(entry.Prompt)
	a.editor.CursorEnd()
	a.loadedPrompt = &entry
	a.relayout()
	return nil
}

func findPrompt(entries []promptlib.Entry, name string) (promptlib.Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return promptlib.Entry{}, false
}

// engageAgent switches to agent mode carried by the named profile. The name
// has to be one the agent picker offers, so the popup and the picker can never
// engage different things for the same name.
func (a *App) engageAgent(name string) tea.Cmd {
	known := false
	for _, c := range a.agents {
		if c.Name == name {
			known = true
			break
		}
	}
	if !known {
		a.addSystem("agent: no profile named " + name)
		return nil
	}
	a.agentExplicit = true
	a.setNamedAgent(name)
	a.mode = "agent"
	a.modeExplicit = true
	a.modeSticky = true
	// Re-resolve the carrier and reseal the system prompt on the next send.
	a.syncPlanMode()
	a.saveMode()
	a.addSystem("agent: " + name)
	a.refreshFooter()
	a.relayout()
	return nil
}

// runProcessEntry starts a saved process unless it is already live, then
// reports its status either way. The manager does not refuse a second copy
// from the same Signet, so the live check has to happen here.
func (a *App) runProcessEntry(name string) tea.Cmd {
	entry, ok := findProcess(a.slashLib.processes, name)
	if !ok {
		a.loadSlashLib()
		entry, ok = findProcess(a.slashLib.processes, name)
	}
	if !ok {
		a.addSystem("process: no saved process named " + name)
		return nil
	}
	if a.procManager == nil {
		a.addSystem("process: process manager not available")
		return nil
	}
	if p, ok := a.procManager.ProcessByName(entry.Name); ok && processLive(p.State) {
		a.addSystem(processStatusLine(p, time.Now()))
		return nil
	}
	p, cmd, ok := a.startSupervised(entry.Name, entry.Command)
	if !ok {
		return nil
	}
	a.addSystem(processStatusLine(p, time.Now()))
	return cmd
}

func findProcess(entries []processlib.Entry, name string) (processlib.Entry, bool) {
	for _, e := range entries {
		if e.Name == name {
			return e, true
		}
	}
	return processlib.Entry{}, false
}

// processLive reports whether a process is running or on its way back to
// running, i.e. starting it again would make a second copy.
func processLive(s bgproc.State) bool {
	switch s {
	case bgproc.StateRunning, bgproc.StateRecovering, bgproc.StateRestarted:
		return true
	}
	return false
}

// processStatusLine is the one-line status the /process: chip reports. Every
// field is a harness observation of the process record.
func processStatusLine(p bgproc.Process, now time.Time) string {
	parts := []string{fmt.Sprintf("process %s (%s) %s", p.Name, p.ID, p.State.Label())}
	if processLive(p.State) {
		if p.PID > 0 {
			parts = append(parts, fmt.Sprintf("pid %d", p.PID))
		}
		if !p.Started.IsZero() {
			parts = append(parts, "up "+now.Sub(p.Started).Round(time.Second).String())
		}
	} else {
		parts = append(parts, fmt.Sprintf("exit %d", p.ExitCode))
		if !p.Ended.IsZero() {
			parts = append(parts, "ended "+now.Sub(p.Ended).Round(time.Second).String()+" ago")
		}
	}
	if p.Command != "" {
		parts = append(parts, p.Command)
	}
	if p.LogPath != "" {
		parts = append(parts, "log "+p.LogPath)
	}
	return strings.Join(parts, " · ")
}
