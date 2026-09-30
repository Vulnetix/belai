package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
)

// treeViewState tracks the /tree session browser: the session's entries as a
// tree, with the branch the conversation is on marked.
type treeViewState struct {
	entries []session.Entry
	rows    []session.TreeRow
	all     bool // show tool, reasoning and system rows too

	cursor, scroll int
	errorMsg       string
}

// enterTree loads the running session's own file. Rows still in memory are
// written first so the tree shows the whole conversation.
func (a *App) enterTree() tea.Cmd {
	a.treeState = treeViewState{}
	entries, err := a.treeEntries()
	if err != nil {
		a.treeState.errorMsg = err.Error()
		return nil
	}
	a.treeState.entries = entries
	a.treeRefresh()
	a.treeState.cursor = a.treeCurrentRow()
	return nil
}

// treeEntries reads the current session's file after flushing what is pending.
func (a *App) treeEntries() ([]session.Entry, error) {
	if a.store == nil || a.storeDisabled {
		return nil, errors.New("session storage is off, so there is no tree to show")
	}
	a.persistTailMode(true)
	entries, err := a.store.ReadFrom(a.sessionKey, a.sessionID)
	if err != nil {
		return nil, errors.New("nothing has been saved in this session yet")
	}
	return entries, nil
}

// treeCurrentRow is the newest shown row on the active branch: the leaf
// itself, or the last reply before it when the leaf is a row that is hidden.
func (a *App) treeCurrentRow() int {
	cur := 0
	for i, r := range a.treeState.rows {
		if r.OnPath {
			cur = i
		}
	}
	return cur
}

func (a *App) treeRefresh() {
	a.treeState.rows = session.TreeRows(a.treeState.entries, a.treeState.all)
	if a.treeState.cursor >= len(a.treeState.rows) {
		a.treeState.cursor = max(0, len(a.treeState.rows)-1)
	}
}

// treeCommand is /tree: no argument opens the browser, an id (or unique
// prefix) continues from that entry, and "fork <id>" copies that branch into a
// new session.
func (a *App) treeCommand(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return a.push(viewTree)
	}
	fork := false
	if rest, ok := strings.CutPrefix(arg, "fork "); ok {
		fork, arg = true, strings.TrimSpace(rest)
	}
	entries, err := a.treeEntries()
	if err != nil {
		a.addSystem("tree: " + err.Error())
		return nil
	}
	e, err := session.FindEntry(entries, arg)
	if err != nil {
		a.addSystem("tree: " + err.Error())
		return nil
	}
	return a.treeGo(e, fork)
}

// treeGo moves the conversation to an entry. Continuing in place appends a
// branch marker and reloads the session, so the file only ever grows and the
// mirror on the website sees further lines. Forking copies the chain into a
// new session and opens that. Files are not rolled back either way.
func (a *App) treeGo(e session.Entry, fork bool) tea.Cmd {
	if a.working() {
		a.addSystem("tree: stop the running turn first")
		return nil
	}
	leaf, prefill, err := session.Target(e)
	if err == nil && leaf == "" {
		err = errors.New("nothing comes before the first message")
	}
	if err != nil {
		a.addSystem("tree: " + err.Error())
		return nil
	}
	a.persistTailMode(true)
	a.flushTurnEnd()

	var cmd tea.Cmd
	if fork {
		newID := session.MustID()
		if err := a.store.ForkPath(a.sessionKey, a.sessionID, a.sessionKey, newID, leaf); err != nil {
			a.addSystem("tree: " + err.Error())
			return nil
		}
		cmd = a.resumeSession(a.sessionKey, newID)
		a.addSystem("forked into " + shortID(newID) + " from " + shortID(e.ID))
	} else {
		a.appendEntry(session.BranchMarker(a.lastEntryID, leaf))
		cmd = a.resumeSession(a.sessionKey, a.sessionID)
		a.addSystem("continuing from " + shortID(e.ID))
	}
	a.addSystem("conversation only: files on disk are not rolled back")
	if prefill != "" {
		a.editor.SetValue(prefill)
		a.editor.CursorEnd()
	}
	return cmd
}

func (a *App) treeHelpBar() string {
	return components.HelpBar("↑↓", "row", "enter", "continue here", "f", "fork", "g", "current", "tab", "all rows", "esc", "close")
}

func (a *App) treeView() string {
	w := a.contentWidth()
	var head strings.Builder
	head.WriteString(components.SectionHeader("Session tree", "esc close", w))
	head.WriteString("\n")

	var tail strings.Builder
	if a.treeState.errorMsg != "" {
		tail.WriteString("\n" + components.DangerStyle.Render("✗ "+a.treeState.errorMsg) + "\n")
	}
	tail.WriteString("\n" + components.MutedStyle.Render("conversation only; files are not rolled back") + "\n")
	tail.WriteString(a.treeHelpBar() + "\n")

	nrows := 10
	if a.height > 0 {
		nrows = a.height - lipgloss.Height(head.String()) - lipgloss.Height(tail.String()) - 2
	}
	if nrows < 3 {
		nrows = 3
	}

	rows := a.treeState.rows
	var body strings.Builder
	if len(rows) == 0 {
		if a.treeState.errorMsg == "" {
			body.WriteString(components.MutedStyle.Render("  no messages yet") + "\n")
		}
	} else {
		a.treeState.scroll = windowStart(a.treeState.scroll, a.treeState.cursor, len(rows), nrows)
		start := a.treeState.scroll
		end := min(start+nrows, len(rows))
		for i := start; i < end; i++ {
			body.WriteString(a.treeLine(rows[i], i == a.treeState.cursor, w) + "\n")
		}
	}
	return lipgloss.NewStyle().Padding(1).Render(head.String() + body.String() + tail.String())
}

// treeLine renders one row: the cursor, the branch indent, a glyph for where
// the row sits (the active leaf, the active path, another branch), the label
// and the short id a /tree argument takes.
func (a *App) treeLine(r session.TreeRow, selected bool, width int) string {
	glyph := "○"
	switch {
	case r.Leaf:
		glyph = "●"
	case r.OnPath:
		glyph = "•"
	}
	if r.Fork {
		glyph = "┬"
	}
	prefix := components.Cursor(selected) + strings.Repeat("  ", min(r.Depth, 12)) + glyph + " "
	id := "  " + shortID(r.Entry.ID)
	room := width - lipgloss.Width(prefix) - lipgloss.Width(id) - 2
	label := r.Label
	if room > 10 && lipgloss.Width(label) > room {
		label = string([]rune(label)[:max(room-1, 1)]) + "…"
	}
	switch {
	case selected:
		label = components.EmphStyle.Render(label)
	case !r.OnPath:
		label = components.MutedStyle.Render(label)
	}
	line := prefix + label + components.MutedStyle.Render(id)
	if r.Leaf {
		line += components.AccentStyle.Render("  ● current")
	}
	return line
}

func (a *App) handleTreeKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := a.treeState.rows
	switch m.String() {
	case "esc", "q":
		a.pop()
	case "up", "k":
		if a.treeState.cursor > 0 {
			a.treeState.cursor--
		}
	case "down", "j":
		if a.treeState.cursor < len(rows)-1 {
			a.treeState.cursor++
		}
	case "g":
		a.treeState.cursor = a.treeCurrentRow()
	case "tab":
		cur := ""
		if a.treeState.cursor < len(rows) {
			cur = rows[a.treeState.cursor].Entry.ID
		}
		a.treeState.all = !a.treeState.all
		a.treeRefresh()
		for i, r := range a.treeState.rows {
			if r.Entry.ID == cur {
				a.treeState.cursor = i
			}
		}
	case "enter", "f":
		if a.treeState.cursor >= len(rows) {
			return a, nil
		}
		e := rows[a.treeState.cursor].Entry
		if _, _, err := session.Target(e); err != nil {
			a.treeState.errorMsg = err.Error()
			return a, nil
		}
		a.pop()
		return a, a.treeGo(e, m.String() == "f")
	}
	return a, nil
}
