package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/tui/components"
)

// resumeViewState tracks the /resume session browser.
type resumeViewState struct {
	groups []session.ProjectSessions
	rows   []resumeRow // flattened: group headers + sessions

	cursor, scroll int
	filtering      bool
	filter         string
	loading        bool
	errorMsg       string
}

// resumeRow is one flattened list row: a group header or a session.
type resumeRow struct {
	header  bool
	label   string
	info    session.SessionInfo
	key     session.Key
	current bool
}

// sessionsScannedMsg carries the result of an async session scan.
type sessionsScannedMsg struct {
	groups []session.ProjectSessions
	err    error
}

func (a *App) enterResume() tea.Cmd {
	a.resumeState = resumeViewState{loading: true}
	return a.scanSessionsCmd()
}

func (a *App) scanSessionsCmd() tea.Cmd {
	store := a.store
	cur, _ := session.KeyFor(a.workdir)
	return func() tea.Msg {
		groups, err := store.AllSessions(cur)
		return sessionsScannedMsg{groups: groups, err: err}
	}
}

func (a *App) handleSessionsScanned(m sessionsScannedMsg) tea.Cmd {
	a.resumeState.loading = false
	if m.err != nil {
		a.resumeState.errorMsg = m.err.Error()
		return nil
	}
	a.resumeState.groups = m.groups
	a.resumeState.rows = flattenGroups(m.groups)
	a.resumeState.cursor = firstSessionRow(a.resumeState.rows)
	return nil
}

func flattenGroups(groups []session.ProjectSessions) []resumeRow {
	var rows []resumeRow
	for _, g := range groups {
		rows = append(rows, resumeRow{header: true, label: g.Label, current: g.Current})
		for _, s := range g.Sessions {
			rows = append(rows, resumeRow{info: s, key: g.Key, current: g.Current})
		}
	}
	return rows
}

func filterResumeRows(rows []resumeRow, q string) []resumeRow {
	if q == "" {
		return rows
	}
	lower := strings.ToLower(q)
	out := make([]resumeRow, 0, len(rows))
	for _, r := range rows {
		if r.header {
			out = append(out, r)
			continue
		}
		if strings.Contains(strings.ToLower(r.info.DisplayName), lower) ||
			strings.Contains(strings.ToLower(r.info.ID), lower) {
			out = append(out, r)
		}
	}
	return out
}

func firstSessionRow(rows []resumeRow) int {
	for i, r := range rows {
		if !r.header {
			return i
		}
	}
	return 0
}

func nextSessionRow(rows []resumeRow, cursor int) int {
	if len(rows) == 0 {
		return 0
	}
	for i := 1; i <= len(rows); i++ {
		idx := (cursor + i) % len(rows)
		if !rows[idx].header {
			return idx
		}
	}
	return cursor
}

func prevSessionRow(rows []resumeRow, cursor int) int {
	if len(rows) == 0 {
		return 0
	}
	for i := 1; i <= len(rows); i++ {
		idx := (cursor - i + len(rows)) % len(rows)
		if !rows[idx].header {
			return idx
		}
	}
	return cursor
}

func (a *App) resumeSearchLine() string {
	const label = "search  "
	switch {
	case a.resumeState.filtering:
		return components.AccentStyle.Render(label + a.resumeState.filter + "▌")
	case a.resumeState.filter != "":
		return components.MutedStyle.Render(label) +
			components.EmphStyle.Render(a.resumeState.filter) +
			components.MutedStyle.Render("  esc clears")
	default:
		return components.MutedStyle.Render(label + "/ to filter")
	}
}

func (a *App) resumeHelpBar() string {
	if a.resumeState.filtering {
		return components.HelpBar("type", "filter", "↑↓", "session", "enter", "resume", "esc", "clear")
	}
	return components.HelpBar("↑↓", "session", "/", "filter", "g", "current", "enter", "resume", "esc", "cancel")
}

func (a *App) resumeView() string {
	w := a.contentWidth()
	rows := filterResumeRows(a.resumeState.rows, a.resumeState.filter)

	var head strings.Builder
	head.WriteString(components.SectionHeader("Resume session", "esc cancel", w))
	head.WriteString("\n" + a.resumeSearchLine() + "\n")

	var tail strings.Builder
	if a.resumeState.errorMsg != "" {
		tail.WriteString("\n" + components.DangerStyle.Render("✗ "+a.resumeState.errorMsg) + "\n")
	}
	tail.WriteString("\n" + a.resumeHelpBar() + "\n")

	const fallbackRows = 10
	nrows := fallbackRows
	if a.height > 0 {
		nrows = a.height - lipgloss.Height(head.String()) - lipgloss.Height(tail.String()) - 2
	}
	if nrows < 3 {
		nrows = 3
	}

	var body strings.Builder
	switch {
	case a.resumeState.loading:
		body.WriteString(components.AccentStyle.Render("  ○ Scanning sessions…") + "\n")
	case len(rows) == 0:
		body.WriteString(components.MutedStyle.Render("  no sessions on disk") + "\n")
	default:
		a.resumeState.scroll = windowStart(a.resumeState.scroll, a.resumeState.cursor, len(rows), nrows)
		start := a.resumeState.scroll
		end := start + nrows
		if end > len(rows) {
			end = len(rows)
		}
		for i := start; i < end; i++ {
			r := rows[i]
			if r.header {
				line := components.MutedStyle.Render(r.label)
				if r.current {
					line += components.AccentStyle.Render("  ● current")
				}
				body.WriteString(line + "\n")
				continue
			}
			name := r.info.DisplayName
			if i == a.resumeState.cursor {
				name = components.EmphStyle.Render(name)
			}
			line := components.Cursor(i == a.resumeState.cursor) + name
			meta := fmt.Sprintf("%d turns · %s", r.info.Turns, ageLabel(r.info.ModTime))
			line += "  " + components.MutedStyle.Render(meta)
			if r.current {
				line += components.AccentStyle.Render("  ● current")
			}
			body.WriteString(line + "\n")
		}
	}

	return lipgloss.NewStyle().Padding(1).Render(head.String() + body.String() + tail.String())
}

// ageLabel renders a compact relative age ("2h ago", "40d ago", "now").
func ageLabel(modTime int64) string {
	if modTime == 0 {
		return ""
	}
	d := time.Since(time.UnixMilli(modTime))
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func (a *App) handleResumeKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.resumeState.filtering {
		return a.handleResumeFilterKey(m)
	}
	rows := filterResumeRows(a.resumeState.rows, a.resumeState.filter)
	switch m.String() {
	case "esc":
		if a.resumeState.filter != "" {
			a.resumeState.filter = ""
			a.resumeState.scroll = 0
			return a, nil
		}
		a.pop()
		return a, nil
	case "up", "k":
		a.resumeState.cursor = prevSessionRow(rows, a.resumeState.cursor)
		return a, nil
	case "down", "j":
		a.resumeState.cursor = nextSessionRow(rows, a.resumeState.cursor)
		return a, nil
	case "/":
		a.resumeState.filtering = true
		return a, nil
	case "g":
		for i, r := range rows {
			if !r.header && r.current {
				a.resumeState.cursor = i
				break
			}
		}
		return a, nil
	case "enter":
		if len(rows) == 0 || a.resumeState.cursor >= len(rows) {
			return a, nil
		}
		r := rows[a.resumeState.cursor]
		if r.header {
			return a, nil
		}
		a.pop()
		return a, a.resumeSession(r.key, r.info.ID)
	}
	return a, nil
}

func (a *App) handleResumeFilterKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.Type {
	case tea.KeyEsc:
		a.resumeState.filtering = false
		a.resumeState.filter = ""
		a.resumeState.scroll = 0
		return a, nil
	case tea.KeyEnter:
		a.resumeState.filtering = false
		return a, nil
	case tea.KeyRunes:
		a.resumeState.filter += string(m.Runes)
		a.resumeState.scroll = 0
		return a, nil
	case tea.KeySpace:
		a.resumeState.filter += " "
		a.resumeState.scroll = 0
		return a, nil
	case tea.KeyBackspace:
		a.resumeState.filter = trimLastRune(a.resumeState.filter)
		a.resumeState.scroll = 0
		return a, nil
	}
	return a, nil
}
