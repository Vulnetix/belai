package tui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/promptlib"
	"github.com/vulnetix/belai/internal/tui/components"
)

// promptsViewState tracks the /prompts manager UI. Rows are derived from
// promptlib.Load on entry and after every mutation, so no cached slice drifts
// from the filesystem.
type promptsViewState struct {
	selected    int
	scope       config.Scope
	mode        string // "" | "name" | "confirm-new" | "delete" | "edit"
	pendingName string // slugged name being confirmed for a duplicate "a"
	editPath    string // entry path being edited in the in-TUI fallback
	errorMsg    string
}

// promptEditedMsg carries the result of an external $EDITOR run.
type promptEditedMsg struct {
	path string
	err  error
}

// promptRow is one rendered prompt entry.
type promptRow struct {
	entry    promptlib.Entry
	override bool // project entry shadows a global name
}

func (a *App) enterPrompts() tea.Cmd {
	if a.promptsState.scope == "" {
		a.promptsState.scope = config.ScopeProject
	}
	a.promptsState.selected = 0
	a.promptsState.mode = ""
	a.promptsState.errorMsg = ""
	return nil
}

// promptRows loads the current scope and returns its entries plus the stray
// count. In the project scope, a project entry shadowing a global name gets
// an override marker so the shadowing is visible.
func (a *App) promptRows() ([]promptRow, int, error) {
	scope := a.promptsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := promptlib.Load(scope, a.workdir)
	if err != nil {
		return nil, 0, err
	}
	var globalNames map[string]bool
	if scope == config.ScopeProject {
		if gl, err := promptlib.Load(config.ScopeGlobal, a.workdir); err == nil {
			globalNames = make(map[string]bool, len(gl.Entries))
			for _, e := range gl.Entries {
				globalNames[e.Name] = true
			}
		}
	}
	rows := make([]promptRow, 0, len(listing.Entries))
	for _, e := range listing.Entries {
		rows = append(rows, promptRow{entry: e, override: globalNames[e.Name]})
	}
	return rows, len(listing.Strays), nil
}

func (a *App) promptsView() string {
	rows, strays, err := a.promptRows()
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Prompt Library", "esc back", w))
	scope := string(a.promptsState.scope)
	if scope == "" {
		scope = string(config.ScopeProject)
	}
	b.WriteString(components.Chip(scope, components.ColorTealSoft) + "\n")

	if a.promptsState.mode == "name" {
		b.WriteString("\n" + a.renderFieldEditor("new prompt name", w) + "\n")
	} else if a.promptsState.mode == "confirm-new" {
		b.WriteString("\n" + components.WarnStyle.Render("replace prompt '"+a.promptsState.pendingName+"'?") + components.MutedStyle.Render("  y/N") + "\n")
	} else if a.promptsState.mode == "delete" {
		name := ""
		if a.promptsState.selected >= 0 && a.promptsState.selected < len(rows) {
			name = rows[a.promptsState.selected].entry.Name
		}
		b.WriteString("\n" + components.DangerStyle.Render("delete prompt '"+name+"'?") + components.MutedStyle.Render("  y/N") + "\n")
	} else if a.promptsState.mode == "edit" {
		b.WriteString("\n" + a.renderPromptInlineEditor(w) + "\n")
	} else if err != nil {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+err.Error()) + "\n")
	} else if len(rows) == 0 {
		b.WriteString(components.MutedStyle.Render("  no prompts in this scope — press a to add one") + "\n")
	} else {
		for i, row := range rows {
			selected := i == a.promptsState.selected
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

			line := components.Cursor(selected) + status + name
			if len(markers) > 0 {
				line += strings.Join(markers, components.MutedStyle.Render(" · "))
			}
			b.WriteString(line + "\n")
		}
	}

	if strays > 0 {
		b.WriteString("\n" + components.MutedStyle.Render(fmt.Sprintf("  %d stray files ignored", strays)) + "\n")
	}
	if a.promptsState.errorMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+a.promptsState.errorMsg) + "\n")
	}

	if a.promptsState.mode != "" {
		b.WriteString("\n" + components.HelpBar("enter", "save", "esc", "cancel") + "\n")
	} else {
		b.WriteString("\n" + components.HelpBar(
			"↑↓", "move", "space", "toggle", "J/K", "reorder", "e", "edit",
			"a", "new", "d", "delete", "s", "scope", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// renderPromptInlineEditor frames the in-TUI fallback editor for a prompt
// body. Unlike the generic field editor, enter is commit and ctrl+j inserts a
// newline, because multi-line prompts are the normal case.
func (a *App) renderPromptInlineEditor(width int) string {
	accent := lipgloss.TerminalColor(components.ColorTeal)
	meta := "⏎ save · ctrl+j newline · esc cancel"
	return components.Panel{
		Title:  "edit prompt",
		Meta:   meta,
		Body:   a.editor.View(),
		Width:  width,
		Accent: accent,
		Raw:    true,
	}.View()
}

func (a *App) handlePromptsKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.promptsState.mode {
	case "name":
		switch m.String() {
		case "esc":
			a.promptsState.mode = ""
			a.promptsState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			name := strings.TrimSpace(a.editor.Value())
			a.editor.Reset()
			if name == "" {
				a.promptsState.errorMsg = "name required"
				return a, nil
			}
			return a.createAndEditPrompt(name)
		default:
			return a, a.editor.Update(m)
		}
	case "confirm-new":
		switch m.String() {
		case "y":
			return a.openExistingPrompt(a.promptsState.pendingName)
		case "n", "esc":
			a.promptsState.mode = ""
			a.promptsState.pendingName = ""
			a.promptsState.errorMsg = ""
			return a, nil
		}
		return a, nil
	case "delete":
		switch m.String() {
		case "y":
			return a.deleteSelectedPrompt()
		case "n", "esc":
			a.promptsState.mode = ""
			return a, nil
		}
		return a, nil
	case "edit":
		switch m.String() {
		case "esc":
			a.promptsState.mode = ""
			a.promptsState.errorMsg = ""
			a.promptsState.editPath = ""
			a.editor.Reset()
			return a, nil
		case "enter":
			return a.saveInlinePrompt()
		default:
			return a, a.editor.Update(m)
		}
	}

	switch m.String() {
	case "esc":
		a.pop()
		return a, nil
	case "up", "k":
		if a.promptsState.selected > 0 {
			a.promptsState.selected--
		}
		return a, nil
	case "down", "j":
		if rows, _, err := a.promptRows(); err == nil && a.promptsState.selected < len(rows)-1 {
			a.promptsState.selected++
		}
		return a, nil
	case " ":
		return a.toggleSelectedPrompt()
	case "J":
		return a.reorderSelectedPrompt(1)
	case "K":
		return a.reorderSelectedPrompt(-1)
	case "e":
		return a.editSelectedPrompt()
	case "a":
		a.promptsState.mode = "name"
		a.promptsState.errorMsg = ""
		a.editor.Reset()
		_ = a.editor.Focus()
		return a, nil
	case "d":
		if rows, _, err := a.promptRows(); err == nil && a.promptsState.selected < len(rows) {
			a.promptsState.mode = "delete"
			a.promptsState.errorMsg = ""
		}
		return a, nil
	case "s":
		a.togglePromptScope()
		return a, nil
	}
	return a, a.editor.Update(m)
}

func (a *App) togglePromptScope() {
	if a.promptsState.scope == config.ScopeGlobal {
		a.promptsState.scope = config.ScopeProject
	} else {
		a.promptsState.scope = config.ScopeGlobal
	}
	a.promptsState.selected = 0
	a.promptsState.errorMsg = ""
}

func (a *App) toggleSelectedPrompt() (tea.Model, tea.Cmd) {
	rows, _, err := a.promptRows()
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.promptsState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if _, err := promptlib.SetEnabled(rows[sel].entry, !rows[sel].entry.Enabled); err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	a.promptsState.errorMsg = ""
	return a, nil
}

func (a *App) reorderSelectedPrompt(delta int) (tea.Model, tea.Cmd) {
	rows, _, err := a.promptRows()
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.promptsState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	to := sel + delta
	if to < 0 || to >= len(rows) {
		return a, nil
	}
	entries := make([]promptlib.Entry, len(rows))
	for i := range rows {
		entries[i] = rows[i].entry
	}
	if _, err := promptlib.Reorder(a.promptsState.scope, a.workdir, entries, sel, to); err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	a.promptsState.selected = to
	a.promptsState.errorMsg = ""
	return a, nil
}

func (a *App) deleteSelectedPrompt() (tea.Model, tea.Cmd) {
	rows, _, err := a.promptRows()
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		a.promptsState.mode = ""
		return a, nil
	}
	sel := a.promptsState.selected
	a.promptsState.mode = ""
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	if err := promptlib.Delete(rows[sel].entry); err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	if a.promptsState.selected >= len(rows)-1 && a.promptsState.selected > 0 {
		a.promptsState.selected--
	}
	a.promptsState.errorMsg = ""
	return a, nil
}

func (a *App) editSelectedPrompt() (tea.Model, tea.Cmd) {
	if a.working() {
		a.promptsState.errorMsg = "refusing to open an editor while a turn is running"
		return a, nil
	}
	rows, _, err := a.promptRows()
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	sel := a.promptsState.selected
	if sel < 0 || sel >= len(rows) {
		return a, nil
	}
	return a, a.openPromptEditor(rows[sel].entry)
}

func (a *App) createAndEditPrompt(name string) (tea.Model, tea.Cmd) {
	if a.working() {
		a.promptsState.errorMsg = "refusing to open an editor while a turn is running"
		return a, nil
	}
	scope := a.promptsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	entry, err := promptlib.Create(scope, a.workdir, name, "")
	if errors.Is(err, promptlib.ErrNameExists) {
		slug, serr := promptlib.Slug(name)
		if serr != nil {
			a.promptsState.errorMsg = serr.Error()
			return a, nil
		}
		a.promptsState.mode = "confirm-new"
		a.promptsState.pendingName = slug
		a.promptsState.errorMsg = ""
		return a, nil
	}
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	a.promptsState.mode = ""
	a.promptsState.errorMsg = ""
	return a, a.openPromptEditor(entry)
}

// openExistingPrompt opens the editor on the entry the duplicate name would
// have replaced, after a y confirm.
func (a *App) openExistingPrompt(slug string) (tea.Model, tea.Cmd) {
	scope := a.promptsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := promptlib.Load(scope, a.workdir)
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		a.promptsState.mode = ""
		return a, nil
	}
	for i := range listing.Entries {
		if listing.Entries[i].Name == slug {
			a.promptsState.mode = ""
			a.promptsState.pendingName = ""
			a.promptsState.errorMsg = ""
			return a, a.openPromptEditor(listing.Entries[i])
		}
	}
	a.promptsState.mode = ""
	a.promptsState.pendingName = ""
	a.promptsState.errorMsg = "prompt no longer exists"
	return a, nil
}

// openPromptEditor hands an entry to $VISUAL/$EDITOR, falling back to the
// in-TUI field editor when neither resolves. It runs through the execEditor
// seam so tests exercise the reload path without spawning an editor.
func (a *App) openPromptEditor(e promptlib.Entry) tea.Cmd {
	bin, args, ok := resolveEditorCommand()
	if !ok {
		a.promptsState.mode = "edit"
		a.promptsState.editPath = e.Path
		a.promptsState.errorMsg = ""
		a.editor.SetValue(e.Prompt)
		a.editor.CursorEnd()
		_ = a.editor.Focus()
		a.addSystem("no $VISUAL or $EDITOR found; edit the prompt inline")
		return nil
	}
	cmd := exec.Command(bin, append(args, e.Path)...)
	return a.execEditor(cmd, func(err error) tea.Msg {
		return promptEditedMsg{path: e.Path, err: err}
	})
}

// resolveEditorCommand resolves $VISUAL then $EDITOR, splits on spaces so
// `code -w` and `emacsclient -nw` work, and resolves the binary with
// exec.LookPath — never through `sh -c`.
func resolveEditorCommand() (string, []string, bool) {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		v := os.Getenv(env)
		if v == "" {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		bin, err := exec.LookPath(fields[0])
		if err != nil {
			continue
		}
		return bin, fields[1:], true
	}
	return "", nil, false
}

// handlePromptEdited reloads the manager after an external editor exits and
// re-finds the selection by path, the way applyAgentFieldChange does. It also
// restores the mouse, which tea's terminal release disabled and RestoreTerminal
// does not re-enable.
func (a *App) handlePromptEdited(m promptEditedMsg) tea.Cmd {
	cmds := []tea.Cmd{}
	if mouseEnabled(&a.settings) {
		cmds = append(cmds, func() tea.Msg { return tea.EnableMouseCellMotion() })
	}
	if m.err != nil {
		a.addSystem("editor failed: " + m.err.Error())
	}
	rows, _, err := a.promptRows()
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return tea.Batch(cmds...)
	}
	a.promptsState.selected = 0
	for i, row := range rows {
		if row.entry.Path == m.path {
			a.promptsState.selected = i
			break
		}
	}
	a.promptsState.mode = ""
	a.promptsState.errorMsg = ""
	return tea.Batch(cmds...)
}

// saveInlinePrompt commits the in-TUI fallback editor's body back to disk.
func (a *App) saveInlinePrompt() (tea.Model, tea.Cmd) {
	scope := a.promptsState.scope
	if scope == "" {
		scope = config.ScopeProject
	}
	listing, err := promptlib.Load(scope, a.workdir)
	if err != nil {
		a.promptsState.errorMsg = err.Error()
		return a, nil
	}
	for i := range listing.Entries {
		if listing.Entries[i].Path == a.promptsState.editPath {
			if _, err := promptlib.Update(listing.Entries[i], a.editor.Value()); err != nil {
				a.promptsState.errorMsg = err.Error()
				return a, nil
			}
			a.promptsState.mode = ""
			a.promptsState.editPath = ""
			a.promptsState.errorMsg = ""
			a.editor.Reset()
			return a, nil
		}
	}
	a.promptsState.mode = ""
	a.promptsState.editPath = ""
	a.promptsState.errorMsg = "prompt no longer exists"
	return a, nil
}
