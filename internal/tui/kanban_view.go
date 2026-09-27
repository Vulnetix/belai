package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/tui/components"
)

// /kanban: the full-screen board. Rows are read from the store on every
// render, so a model's write or a sync pull shows without a refresh.

// kanbanViewState is the /kanban screen's cursor and mode.
type kanbanViewState struct {
	tab   int // index into kanban.Lists
	sel   int
	all   bool
	query string
	// mode is "" | "filter" | "new-title" | "new-body" | "edit-title" |
	// "edit-body" | "note" | "move" | "delete".
	mode     string
	newTitle string
	editID   string
	status   string
	errMsg   string
}

// kanbanViewLists is the tab order: the working lists, then done.
var kanbanViewLists = kanban.Lists

func (a *App) enterKanban() tea.Cmd {
	if a.kb == nil {
		return nil
	}
	v := &a.kb.view
	v.mode, v.status, v.errMsg = "", "", ""
	if a.kb.syncer != nil {
		a.kb.syncer.PullNow()
	}
	return nil
}

// kanbanViewRows returns the selected tab's items.
func (a *App) kanbanViewRows() ([]kanban.Item, error) {
	v := a.kb.view
	q := kanban.Query{Text: v.query, Lists: []kanban.List{kanbanViewLists[v.tab]}, Limit: kanban.MaxItems}
	if !v.all {
		q.Project = a.kanbanProject()
	}
	return a.kb.store.Search(q)
}

func (a *App) kanbanSelected() (kanban.Item, bool) {
	rows, err := a.kanbanViewRows()
	if err != nil || len(rows) == 0 {
		return kanban.Item{}, false
	}
	sel := min(a.kb.view.sel, len(rows)-1)
	return rows[sel], true
}

func (a *App) kanbanView() string {
	w := a.contentWidth()
	var b strings.Builder
	b.WriteString(components.SectionHeader("Kanban", "esc back", w))
	if a.kb == nil {
		b.WriteString(components.MutedStyle.Render("  the kanban board is off (setting: kanban)") + "\n")
		return lipgloss.NewStyle().Padding(1).Render(b.String())
	}
	v := &a.kb.view
	project := a.kanbanProject()
	if v.all {
		project = ""
	}
	counts, cerr := a.kb.store.Counts(project)
	var tabs []string
	for i, l := range kanbanViewLists {
		label := fmt.Sprintf("%s %d", strings.ReplaceAll(string(l), "_", " "), counts[l])
		if i == v.tab {
			tabs = append(tabs, components.Chip(label, kanbanListColor(l)))
		} else {
			tabs = append(tabs, lipgloss.NewStyle().Foreground(kanbanListColor(l)).Render(label))
		}
	}
	b.WriteString(strings.Join(tabs, "  ") + "\n")
	scope := "this project (" + a.kb.src.Get().Project + ")"
	if v.all {
		scope = "all projects"
	}
	meta := scope + " · " + a.kanbanSyncLabel()
	if v.query != "" {
		meta += " · filter: " + v.query
	}
	b.WriteString(components.MutedStyle.Render(ansi.Truncate(meta, w, "…")) + "\n\n")

	rows, err := a.kanbanViewRows()
	if err == nil {
		err = cerr
	}
	if v.sel >= len(rows) {
		v.sel = max(0, len(rows)-1)
	}
	switch {
	case err != nil:
		b.WriteString(components.DangerStyle.Render("✗ "+err.Error()) + "\n")
	case len(rows) == 0:
		b.WriteString(components.MutedStyle.Render("  nothing here — n adds an item") + "\n")
	default:
		const window = 12
		start := 0
		if v.sel >= window {
			start = v.sel - window + 1
		}
		end := min(len(rows), start+window)
		for i := start; i < end; i++ {
			it := rows[i]
			selected := i == v.sel
			title := it.Title
			if selected {
				title = components.EmphStyle.Render(title)
			}
			line := components.Cursor(selected) + components.MutedStyle.Render(it.Short()+" ")
			if v.all {
				line += components.MutedStyle.Render(it.Project + " · ")
			}
			if it.Dirty && a.kb.syncer != nil {
				line += components.MutedStyle.Render("↑ ")
			}
			if r := kanbanRouting(it, time.Now().UnixMilli()); r != "" {
				title += "  " + r
			}
			b.WriteString(ansi.Truncate(line+title, w, "…") + "\n")
		}
		if more := len(rows) - end; more > 0 {
			b.WriteString(components.MutedStyle.Render(fmt.Sprintf("  +%d more", more)) + "\n")
		}
		if it, ok := a.kanbanSelected(); ok {
			b.WriteString("\n" + a.kanbanDetail(it, w))
		}
	}

	switch v.mode {
	case "filter":
		b.WriteString("\n" + a.renderFieldEditor("filter", w) + "\n")
	case "new-title":
		b.WriteString("\n" + a.renderFieldEditor("new "+string(kanbanViewLists[v.tab])+" item — title", w) + "\n")
	case "new-body":
		b.WriteString("\n" + a.renderFieldEditor("details (optional)", w) + "\n")
	case "edit-title":
		b.WriteString("\n" + a.renderFieldEditor("title", w) + "\n")
	case "edit-body":
		b.WriteString("\n" + a.renderFieldEditor("details", w) + "\n")
	case "note":
		b.WriteString("\n" + a.renderFieldEditor("add a note", w) + "\n")
	case "assign":
		b.WriteString("\n" + a.renderFieldEditor("assign to agent profile (empty: any matching agent)", w) + "\n")
	case "labels":
		b.WriteString("\n" + a.renderFieldEditor("labels, comma-separated (they route the item to agents)", w) + "\n")
	case "move":
		var opts []string
		for i, l := range kanbanViewLists {
			opts = append(opts, fmt.Sprintf("%d %s", i+1, l))
		}
		b.WriteString("\n" + components.WarnStyle.Render("move to: ") + strings.Join(opts, "  ") + components.MutedStyle.Render("  · esc cancel") + "\n")
	case "delete":
		b.WriteString("\n" + components.DangerStyle.Render("delete this item?") + components.MutedStyle.Render("  y/N") + "\n")
	}
	if v.errMsg != "" {
		b.WriteString("\n" + components.DangerStyle.Render("✗ "+v.errMsg) + "\n")
	} else if v.status != "" {
		b.WriteString("\n" + components.MutedStyle.Render(v.status) + "\n")
	}
	if v.mode == "" {
		b.WriteString("\n" + components.HelpBar(
			"←→", "list", "↑↓", "item", "⏎", "work on it", "n", "new", "e", "title", "b", "details",
			"o", "note", "m", "move", "a", "assign", "L", "labels", "+/-", "priority", "u", "unclaim", "d", "delete", "/", "filter", "p", "scope", "r", "sync", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
}

// kanbanDetail renders the selected item's body, provenance and history.
func (a *App) kanbanDetail(it kanban.Item, w int) string {
	var lines []string
	lines = append(lines, kanbanChip(it.List)+" "+components.EmphStyle.Render(it.Title))
	prov := fmt.Sprintf("%s · %s · session %s · added %s", it.Project, it.Dir, kanbanShortSession(it.SessionID), time.UnixMilli(it.Created).Format("2006-01-02 15:04"))
	lines = append(lines, components.MutedStyle.Render(ansi.Truncate(prov, w, "…")))
	if r := kanbanRouting(it, time.Now().UnixMilli()); r != "" {
		lines = append(lines, "  "+r)
	}
	if it.Branch != "" || it.PR != "" {
		lines = append(lines, components.MutedStyle.Render(ansi.Truncate("  branch "+it.Branch+"  "+it.PR, w, "…")))
	}
	if it.Body != "" {
		body := strings.Split(it.Body, "\n")
		if len(body) > 8 {
			body = append(body[:8], "…")
		}
		for _, l := range body {
			lines = append(lines, "  "+ansi.Truncate(l, w-2, "…"))
		}
	}
	hist := it.History
	if len(hist) > 5 {
		hist = hist[len(hist)-5:]
	}
	for _, m := range hist {
		when := time.UnixMilli(m.At).Format("01-02 15:04")
		var s string
		switch {
		case m.From == m.To && m.Note != "":
			s = when + " note: " + m.Note
		case m.From == "":
			s = when + " added to " + string(m.To)
		default:
			s = fmt.Sprintf("%s %s → %s", when, m.From, m.To)
			if m.Note != "" {
				s += ": " + m.Note
			}
		}
		lines = append(lines, components.MutedStyle.Render("  "+ansi.Truncate(strings.ReplaceAll(s, "\n", " "), w-2, "…")))
	}
	return strings.Join(lines, "\n") + "\n"
}

func (a *App) handleKanbanKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.kb == nil {
		if m.String() == "esc" {
			a.pop()
		}
		return a, nil
	}
	v := &a.kb.view
	key := m.String()
	switch v.mode {
	case "filter", "new-title", "new-body", "edit-title", "edit-body", "note", "assign", "labels":
		switch key {
		case "esc":
			v.mode, v.errMsg = "", ""
			a.editor.Reset()
			return a, nil
		case "enter":
			text := strings.TrimSpace(a.editor.Value())
			a.editor.Reset()
			a.kanbanCommit(text)
			return a, nil
		}
		return a, a.editor.Update(m)
	case "move":
		if key == "esc" {
			v.mode = ""
			return a, nil
		}
		if len(key) == 1 && key[0] >= '1' && int(key[0]-'0') <= len(kanbanViewLists) {
			to := kanbanViewLists[key[0]-'1']
			if it, ok := a.kanbanSelected(); ok {
				if _, err := a.kb.store.Move(it.ID, to, "moved by the user", a.sessionID); err != nil {
					v.errMsg = err.Error()
				} else {
					v.status = fmt.Sprintf("%s moved %s → %s", it.Short(), it.List, to)
				}
			}
			v.mode = ""
		}
		return a, nil
	case "delete":
		if key == "y" {
			if it, ok := a.kanbanSelected(); ok {
				if _, err := a.kb.store.Delete(it.ID); err != nil {
					v.errMsg = err.Error()
				} else {
					v.status = it.Short() + " deleted"
				}
			}
		}
		v.mode = ""
		return a, nil
	}

	v.errMsg, v.status = "", ""
	switch key {
	case "esc":
		if v.query != "" {
			v.query = ""
			return a, nil
		}
		a.pop()
	case "left", "h":
		v.tab = (v.tab + len(kanbanViewLists) - 1) % len(kanbanViewLists)
		v.sel = 0
	case "right", "l", "tab":
		v.tab = (v.tab + 1) % len(kanbanViewLists)
		v.sel = 0
	case "up", "k":
		if v.sel > 0 {
			v.sel--
		}
	case "down", "j":
		if rows, err := a.kanbanViewRows(); err == nil && v.sel < len(rows)-1 {
			v.sel++
		}
	case "p":
		v.all = !v.all
		v.sel = 0
	case "r":
		if a.kb.syncer != nil {
			a.kb.syncer.PullNow()
			v.status = "syncing…"
		} else {
			v.status = a.kanbanSyncLabel()
		}
	case "/":
		a.kanbanEdit("filter", v.query)
	case "n":
		a.kanbanEdit("new-title", "")
	case "e", "b", "o":
		if it, ok := a.kanbanSelected(); ok {
			v.editID = it.ID
			switch key {
			case "e":
				a.kanbanEdit("edit-title", it.Title)
			case "b":
				a.kanbanEdit("edit-body", it.Body)
			default:
				a.kanbanEdit("note", "")
			}
		}
	case "a", "L":
		if it, ok := a.kanbanSelected(); ok {
			v.editID = it.ID
			if key == "a" {
				a.kanbanEdit("assign", it.Assignee)
			} else {
				a.kanbanEdit("labels", strings.Join(it.Labels, ", "))
			}
		}
	case "+", "-", "=":
		if it, ok := a.kanbanSelected(); ok {
			p := it.Priority + 1
			if key == "-" {
				p = it.Priority - 1
			}
			if got, err := a.kb.store.Route(it.ID, kanban.RoutePatch{Priority: &p}, a.sessionID); err != nil {
				v.errMsg = err.Error()
			} else {
				v.status = fmt.Sprintf("%s priority %d", got.Short(), got.Priority)
			}
		}
	case "u":
		if it, ok := a.kanbanSelected(); ok {
			if it.ClaimedBy == "" {
				v.status = it.Short() + " is not claimed"
			} else if got, err := a.kb.store.Unclaim(it.ID, "claim released by the user", a.sessionID, false); err != nil {
				v.errMsg = err.Error()
			} else {
				v.status = fmt.Sprintf("%s released to %s", got.Short(), got.List)
			}
		}
	case "m":
		if _, ok := a.kanbanSelected(); ok {
			v.mode = "move"
		}
	case "d":
		if _, ok := a.kanbanSelected(); ok {
			v.mode = "delete"
		}
	case "enter":
		if it, ok := a.kanbanSelected(); ok {
			a.popToChat()
			a.prefillKanbanPrompt(it)
		}
	}
	return a, nil
}

// kanbanEdit opens the shared editor for a field.
func (a *App) kanbanEdit(mode, value string) {
	a.kb.view.mode = mode
	a.editor.Reset()
	a.editor.SetValue(value)
	a.editor.CursorEnd()
	_ = a.editor.Focus()
}

// kanbanCommit applies the field the editor just closed on.
func (a *App) kanbanCommit(text string) {
	v := &a.kb.view
	mode := v.mode
	v.mode = ""
	prov := a.kb.src.Get()
	switch mode {
	case "filter":
		v.query, v.sel = text, 0
	case "new-title":
		if text == "" {
			v.errMsg = "a title is required"
			return
		}
		v.newTitle = text
		a.kanbanEdit("new-body", "")
	case "new-body":
		it, dup, err := a.kb.store.Add(kanban.ItemInput{Title: v.newTitle, Body: text, List: kanbanViewLists[v.tab]}, prov)
		v.newTitle = ""
		switch {
		case err != nil:
			v.errMsg = err.Error()
		case dup:
			v.status = "already on the board as " + it.Short()
		default:
			v.status = it.Short() + " added"
		}
	case "assign", "labels":
		var rp kanban.RoutePatch
		if mode == "assign" {
			rp.Assignee = &text
		} else {
			labels := strings.Split(text, ",")
			rp.Labels = &labels
		}
		if it, err := a.kb.store.Route(v.editID, rp, prov.SessionID); err != nil {
			v.errMsg = err.Error()
		} else {
			v.status = it.Short() + " routed"
		}
	case "edit-title", "edit-body", "note":
		var p kanban.Patch
		switch mode {
		case "edit-title":
			p.Title = &text
		case "edit-body":
			p.Body = &text
		default:
			if text == "" {
				return
			}
			p.Note = text
		}
		if it, err := a.kb.store.Update(v.editID, p, prov.SessionID); err != nil {
			v.errMsg = err.Error()
		} else {
			v.status = it.Short() + " updated"
		}
	}
}

// kanbanStatus is the screen switcher's status for the board.
func (a *App) kanbanStatus() string {
	if a.kb == nil {
		return "off"
	}
	counts, err := a.kb.store.Counts(a.kanbanProject())
	if err != nil {
		return "unreadable"
	}
	var parts []string
	for _, l := range []kanban.List{kanban.Review, kanban.InProgress, kanban.Blocked, kanban.Backlog} {
		if n := counts[l]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, strings.ReplaceAll(string(l), "_", " ")))
		}
	}
	return strings.Join(parts, " · ")
}

// kanbanRouting renders an item's labels, priority, assignee and claim:
// "#build ▲2 @belai:builder ⚙ belai-builder-3f9a2c 12m". A lapsed claim
// shows ⚠ instead of the lease.
func kanbanRouting(it kanban.Item, now int64) string {
	var parts []string
	for _, l := range it.Labels {
		parts = append(parts, components.MutedStyle.Render("#"+l))
	}
	switch {
	case it.Priority > 0:
		parts = append(parts, components.WarnStyle.Render(fmt.Sprintf("▲%d", it.Priority)))
	case it.Priority < 0:
		parts = append(parts, components.MutedStyle.Render(fmt.Sprintf("▼%d", -it.Priority)))
	}
	if it.Assignee != "" {
		parts = append(parts, components.AccentStyle.Render("@"+it.Assignee))
	}
	if it.ClaimedBy != "" {
		if it.LeaseUntil > now {
			lease := time.Duration(it.LeaseUntil - now).Round(time.Minute)
			parts = append(parts, components.AccentStyle.Render("⚙ "+it.ClaimedBy+" "+compactDuration(lease)))
		} else {
			parts = append(parts, components.DangerStyle.Render("⚠ "+it.ClaimedBy+" lapsed"))
		}
	}
	return strings.Join(parts, " ")
}
