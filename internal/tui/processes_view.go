// /processes manager for the supervised-process library. The view mirrors
// the prompt-library manager because both are file-backed libraries, but rows
// also show the live running state from the supervised-process manager.
package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/bgproc"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/processlib"
	"github.com/vulnetix/belai/internal/tui/components"
)

// processesViewState tracks the /processes manager UI.
type processesViewState struct {
	selected    int
	scope       config.Scope
	mode        string // "" | "name" | "confirm-new" | "delete" | "edit"
	pendingName string
	editPath    string
	errorMsg    string
}

// processEditedMsg carries the result of an external $EDITOR run.
type processEditedMsg struct {
	path string
	err  error
}

type processRow struct {
	entry    processlib.Entry
	override bool
}

func (a *App) enterProcesses() tea.Cmd {
	if a.processesState.scope == "" {
		a.processesState.scope = config.ScopeProject
	}
	a.processesState.selected = 0
	a.processesState.mode = ""
	a.processesState.errorMsg = ""
	return nil
}

func (a *App) processRows() ([]processRow, int, error) {
	scope := a.processesState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := processlib.Load(scope, a.workdir)
	if err != nil {
		return nil, 0, err
	}
	var globalNames map[string]bool
	if scope == config.ScopeProject {
		if gl, err := processlib.Load(config.ScopeGlobal, a.workdir); err == nil {
			globalNames = make(map[string]bool, len(gl.Entries))
			for _, e := range gl.Entries {
				globalNames[e.Name] = true
			}
		}
	}
	rows := make([]processRow, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		rows = append(rows, processRow{entry: e, override: globalNames[e.Name]})
	}
	return rows, len(listing.Strays), nil
}

func (a *App) processesView() string {
	rows, strays, err := a.processRows()
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Process Library", "esc back", w))
	scope := string(a.processesState.scope)
	if scope == "" {
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) + "\n")

	// Build a map of running processes by library slug so we can render a
	// status chip next to each row.
	running := make(map[string]bgproc.Process)
	if a.procManager != nil {
		for _, p := range a.procManager.List() {
			running[p.Name] = p
		}
	}

	if a.processesState.mode == "name" {
		b.WriteString("\n" + a.renderFieldEditor("new process command", w) + "\n")
	} else if a.processesState.mode == "confirm-new" {
		b.WriteString("\n" + components.WarnStyle.Render("replace process '"+a.processesState.pendingName+"'?") + components.MutedStyle.Render("  y/N") + "\n")
	} else if a.processesState.mode == "delete" {
		name := ""
		if a.processesState.selected >= 0 && a.processesState.selected < len(rows) {
			name = rows[a.processesState.selected].entry.Name
		}
		b.WriteString("\n" + components.DangerStyle.Render("delete process '"+name+"'?") + components.MutedStyle.Render("  y/N") + "\n")
	} else if a.processesState.mode == "edit" {
		b.WriteString("\n" + a.renderProcessInlineEditor(w) + "\n")
	} else if err != nil {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+err.Error()) + "\n")
	} else if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("  no processes in this scope — press a to add one") + "\n")
	} else {
		for i, row := range rows {
			selected := i == a.processesState.selected
			status := "● "
			if !row.entry.Enabled {
				status = "○ "
			}
			name := fmt.Sprintf("%03d %s", row.entry.Order, row.entry.Name)
			switch {
			case selected:
				name = components.EmphStyle.Render(name)
			case !row.entry.Enabled:
				name = components.MutedStyle.Render(name)
			}

			var markers []string
			if row.override {
				markers = append(markers, components.MutedStyle.Render("override"))
			}
			if !row.entry.Enabled {
				markers = append(markers, components.MutedStyle.Render("disabled"))
			}
			if proc, ok := running[row.entry.Name]; ok {
				markers = append(markers, components.Chip(proc.State.Label(), components.ColorTealSoft))
				if proc.PID != 0 {
					markers = append(markers, components.MutedStyle.Render(fmt.Sprintf("pid %d", proc.PID)))
				}
			}

			line := components.Cursor(selected) + status + name
			if len(markers) > 0 {
				line += " " + strings.Join(markers, components.MutedStyle.Render(" · "))
			}
			b.WriteString(line + "\n")
		}
	}

	if strays > 0 {
		b.WriteString("\n" + components.MutedStyle.Render(fmt.Sprintf("  %d stray files ignored", strays)) + "\n")
	}
	if a.processesState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.processesState.errorMsg) + "\n")
	}

	if a.processesState.mode != "" {
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "toggle", "J/K", "reorder", "e", "edit",
			"a", "new", "d", "delete", "s", "scope", "r", "run", "x", "stop",
			"enter", "tail", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

func (a *App) renderProcessInlineEditor(width int) string {
	accent := lipgloss.TerminalColor(components.ColorTeal)
	meta := "⏎ save · ctrl+j newline · esc cancel"
	return components.Panel{
		Title:  "edit process command",
		Meta:   meta,
		Body:   a.editor.View(),
		Width:  width,
		Accent: accent,
		Raw:    true,
	}.View()
}

func (a *App) handleProcessesKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.processesState.mode {
	case "name":
		switch m.String() {
		case "esc":
			a.processesState.mode = ""
			a.processesState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			cmd := strings.TrimSpace(a.editor.Value())
			a.editor.Reset()
			if cmd == "" {
				a.processesState.errorMsg = "command required"
				return a, nil
			}
			return a.createAndEditProcess(cmd)
		default:
			return a, a.editor.Update(m)
		}
	case "confirm-new":
		switch m.String() {
		case "y":
			return a.openExistingProcess(a.processesState.pendingName)
		case "n", "esc":
			a.processesState.mode = ""
			a.processesState.pendingName = ""
			a.processesState.errorMsg = ""
			return a, nil
		}
		return a, nil
	case "delete":
		switch m.String() {
		case "y":
			return a.deleteSelectedProcess()
		case "n", "esc":
			a.processesState.mode = ""
			return a, nil
		}
		return a, nil
	case "edit":
		switch m.String() {
		case "esc":
			a.processesState.mode = ""
			a.processesState.errorMsg = ""
			a.processesState.editPath = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			return a.saveInlineProcess()
		default:
			return a, a.editor.Update(m)
		}
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.processesState.selected > 0 {
			a.processesState.selected--
		}
		return a, nil
	case "down", "j":
		if rows, _, err := a.processRows(); err == nil && a.processesState.selected < len(rows)-1 {
			a.processesState.selected++
		}
		return a, nil
	case " ":
		return a.toggleSelectedProcess()
	case "J":
		return a.reorderSelectedProcess(1)
	case "K":
		return a.reorderSelectedProcess(-1)
	case "e":
		return a.editSelectedProcess()
	case "a":
		a.processesState.mode = "name"
		a.processesState.errorMsg = ""
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	case "d":
		if rows, _, err := a.processRows(); err == nil && a.processesState.selected < len(rows) {
			a.processesState.mode = "delete"
			a.processesState.errorMsg = ""
		}
		return a, nil
	case "s":
		a.toggleProcessScope()
		return a, nil
	case "r":
		return a.runSelectedProcess()
	case "x":
		return a.stopSelectedProcess()
	case "enter":
		return a.showSelectedProcessTail()
	}
	return a, a.editor.Update(m)
}

func (a *App) toggleProcessScope() {
	if a.processesState.scope == config.ScopeGlobal {
		a.processesState.scope = config.ScopeProject
	} else {
		a.processesState.scope = config.ScopeGlobal
	}
	a.processesState.selected = 0
	a.processesState.errorMsg = ""
}

func (a *App) toggleSelectedProcess() (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if _, err := processlib.SetEnabled(rows[sel].entry, !rows[sel].entry.Enabled); err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.processesState.errorMsg = ""
	return a, nil
}

func (a *App) reorderSelectedProcess(delta int) (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	to := sel + delta
	if to < 0 || to >= len(rows) {
		return a, nil
	}
	entries := make([]processlib.Entry, len(rows))
	for i := range rows {
		entries[i] = rows[i].entry
	}
	if _, err := processlib.Reorder(a.processesState.scope, a.workdir, entries, sel, to); err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.processesState.selected = to
	a.processesState.errorMsg = ""
	return a, nil
}

func (a *App) deleteSelectedProcess() (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		a.processesState.mode = ""
		return a, nil
	}
	sel := a.processesState.selected
	a.processesState.mode = ""
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if err := processlib.Delete(rows[sel].entry); err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	if a.processesState.selected >= len(rows)-1 && a.processesState.selected > 0 {
		a.processesState.selected--
	}
	a.processesState.errorMsg = ""
	return a, nil
}

func (a *App) editSelectedProcess() (tea.Model, tea.Cmd) {
	if a.working() {
		a.processesState.errorMsg = "refusing to open an editor while a turn is running"
		return a, nil
	}
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	return a, a.openProcessEditor(rows[sel].entry)
}

func (a *App) createAndEditProcess(cmd string) (tea.Model, tea.Cmd) {
	if a.working() {
		a.processesState.errorMsg = "refusing to open an editor while a turn is running"
		return a, nil
	}
	scope := a.processesState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	entry, err := processlib.CreateUnique(scope, a.workdir, cmd)
	if errors.Is(err, processlib.ErrNameExists) {
		a.processesState.mode = "confirm-new"
		a.processesState.pendingName = entry.Name
		a.processesState.errorMsg = ""
		return a, nil
	}
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.processesState.mode = ""
	a.processesState.errorMsg = ""
	return a, a.openProcessEditor(entry)
}

func (a *App) openExistingProcess(slug string) (tea.Model, tea.Cmd) {
	scope := a.processesState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := processlib.Load(scope, a.workdir)
	if err != nil {
		a.processesState.errorMsg = err.Error()
		a.processesState.mode = ""
		return a, nil
	}
	for i := range listing.Entries {
		if listing.Entries[i].Name == slug {
			a.processesState.mode = ""
			a.processesState.pendingName = ""
			a.processesState.errorMsg = ""
			return a, a.openProcessEditor(listing.Entries[i])
		}
	}
	a.processesState.mode = ""
	a.processesState.pendingName = ""
	a.processesState.errorMsg = "process no longer exists"
	return a, nil
}

func (a *App) openProcessEditor(e processlib.Entry) tea.Cmd {
	bin, args, ok := resolveEditorCommand()
	if !ok {
		a.processesState.mode = "edit"
		a.processesState.editPath = e.Path
		a.processesState.errorMsg = ""
		a.editor.SetValue(e.Command)
		a.editor.CursorEnd()
		_ = a.editor.Focus()
		a.addSystem("no $VISUAL or $EDITOR found; edit the command inline")
		return nil
	}
	cmd := exec.Command(bin, append(args, e.Path)...)
	return a.execEditor(cmd, func(err error) tea.Msg {
		return processEditedMsg{path: e.Path, err: err}
	})
}

func (a *App) handleProcessEdited(m processEditedMsg) tea.Cmd {
	cmds := []tea.Cmd{}
	if mouseEnabled(&a.settings) {
		cmds = append(cmds, func() tea.Msg { return tea.EnableMouseCellMotion() })
	}
	if m.err != nil {
		a.addSystem("editor failed: " + m.err.Error())
	}
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return tea.Batch(cmds...)
	}
	a.processesState.selected = 0
	for i, row := range rows {
		if row.entry.Path == m.path {
			a.processesState.selected = i
			break
		}
	}
	a.processesState.mode = ""
	a.processesState.errorMsg = ""
	return tea.Batch(cmds...)
}

func (a *App) saveInlineProcess() (tea.Model, tea.Cmd) {
	scope := a.processesState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := processlib.Load(scope, a.workdir)
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	for i := range listing.Entries {
		if listing.Entries[i].Path == a.processesState.editPath {
			if _, err := processlib.Update(listing.Entries[i], a.editor.Value()); err != nil {
				a.processesState.errorMsg = err.Error()
				return a, nil
			}
			a.processesState.mode = ""
			a.processesState.editPath = ""
			a.processesState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		}
	}
	a.processesState.mode = ""
	a.processesState.editPath = ""
	a.processesState.errorMsg = "process no longer exists"
	return a, nil
}

func (a *App) runSelectedProcess() (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if a.procManager == nil {
		a.processesState.errorMsg = "process manager not available"
		return a, nil
	}
	if _, err := a.procManager.Start(rows[sel].entry.Name, rows[sel].entry.Command); err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.processesState.errorMsg = ""
	return a, nil
}

func (a *App) stopSelectedProcess() (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if a.procManager == nil {
		a.processesState.errorMsg = "process manager not available"
		return a, nil
	}
	// Find the runtime handle for this library entry.
	name := rows[sel].entry.Name
	var targetID string
	for _, p := range a.procManager.List() {
		if p.Name == name {
			targetID = p.ID
			break
		}
	}
	if targetID == "" {
		a.processesState.errorMsg = "process not running"
		return a, nil
	}
	if err := a.procManager.Stop(targetID); err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.processesState.errorMsg = ""
	return a, nil
}

func (a *App) showSelectedProcessTail() (tea.Model, tea.Cmd) {
	rows, _, err := a.processRows()
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.processesState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	name := rows[sel].entry.Name
	if a.procManager == nil {
		a.processesState.errorMsg = "process manager not available"
		return a, nil
	}

	// Prefer a running process so the F9-style activity output view stays live.
	var targetID string
	for _, p := range a.procManager.List() {
		if p.Name == name && p.State == bgproc.StateRunning {
			targetID = p.ID
			break
		}
	}
	if targetID != "" {
		a.runsOutput = runsOutputState{id: "proc-" + targetID}
		return a, a.push(viewRunsOutput)
	}

	// Fall back to the newest log file for this process, whether it is
	// historical in this session or from a previous Belai run.
	tail, err := a.procManager.Tail(name, 256)
	if err != nil {
		a.processesState.errorMsg = err.Error()
		return a, nil
	}
	a.runsOutput = runsOutputState{id: name, content: tail}
	return a, a.push(viewRunsOutput)
}
