package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/trustgate"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The trusted directories screen (/trusted) lists every directory the user
// has affirmed at the first-run trust gate, `-trust-dir` or `belai rc --dir`.
// Trust is what remote control offers to the website, so this is where a
// stale or throwaway directory is revoked. Revoking only clears the registry's
// trusted flag: the next launch there asks again, and an rc-session child
// started there fails its own trust check. Adding asks for a path and a
// confirmation, the same consent `-trust-dir` records: the directory only,
// never the workspace directories its settings propose.
//
// The list can run to hundreds of entries, so it is one line per directory in
// a window that follows the cursor, and / filters it by path substring.

type trustedViewState struct {
	rows []trustedRow
	// selected indexes the filtered rows; scroll is the window's first row.
	selected, scroll int
	filtering        bool
	filter           string
	// mode is "" when browsing, "add" while the editor holds a path, or
	// "confirm-add" / "confirm-revoke" / "confirm-prune" / "confirm-shown"
	// awaiting y/n.
	mode    string
	pending []trustedRow // what a revoke confirmation applies to
	addPath string       // the normalised path a confirm-add would trust
	note    string
	noteErr bool
}

// trustedRow is one trusted registry entry with the facts the screen shows.
type trustedRow struct {
	key     string
	path    string
	entry   projectregistry.Entry
	missing bool // the directory no longer exists
	temp    bool // a scratch directory rc never offers
	offered bool // the running rc daemon lists it
	current bool // this session's working directory
}

// stale reports a row the prune action revokes.
func (r trustedRow) stale() bool { return r.missing || r.temp }

func (a *App) openTrusted() tea.Cmd { return a.push(viewTrusted) }

func (a *App) enterTrusted() tea.Cmd {
	a.trustedState = trustedViewState{}
	a.loadTrustedRows()
	return nil
}

// loadTrustedRows reads the registry and the rc record. The registry is a
// small local file, so this runs on the UI goroutine like the other
// settings screens.
func (a *App) loadTrustedRows() {
	st := &a.trustedState
	st.rows = nil
	reg, err := projectregistry.Load()
	if err != nil {
		st.note, st.noteErr = err.Error(), true
		return
	}
	offered := map[string]bool{}
	if r, live := rc.ReadRecord(); live {
		for _, d := range r.Dirs {
			offered[d.Path] = true
		}
	}
	cur, _ := rc.Normalize(a.workdir)
	for _, e := range reg.All() {
		if !e.Trusted {
			continue
		}
		row := trustedRow{key: e.Key, path: e.Path, entry: e}
		if p, err := rc.Normalize(e.Path); err == nil {
			row.path = p
			row.temp = rc.IsTempDir(p)
		} else {
			row.missing = true
		}
		row.offered = offered[row.path]
		row.current = cur != "" && row.path == cur
		st.rows = append(st.rows, row)
	}
	sort.SliceStable(st.rows, func(i, j int) bool {
		if st.rows[i].current != st.rows[j].current {
			return st.rows[i].current
		}
		return st.rows[i].path < st.rows[j].path
	})
	a.clampTrusted()
}

// trustedShown is the rows matching the filter, case-insensitively, by path
// or by tag ("missing", "temporary", "offered").
func (a *App) trustedShown() []trustedRow {
	st := &a.trustedState
	q := strings.ToLower(strings.TrimSpace(st.filter))
	if q == "" {
		return st.rows
	}
	var out []trustedRow
	for _, r := range st.rows {
		hay := strings.ToLower(r.path)
		if r.missing {
			hay += " missing"
		}
		if r.temp {
			hay += " temporary"
		}
		if r.offered {
			hay += " offered"
		}
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	return out
}

func (a *App) clampTrusted() {
	st := &a.trustedState
	n := len(a.trustedShown())
	if st.selected >= n {
		st.selected = n - 1
	}
	if st.selected < 0 {
		st.selected = 0
	}
}

func (a *App) trustedStale() []trustedRow {
	var out []trustedRow
	for _, r := range a.trustedState.rows {
		if r.stale() {
			out = append(out, r)
		}
	}
	return out
}

// revokeTrusted clears trust for rows and says what still needs doing.
func (a *App) revokeTrusted(rows []trustedRow) {
	st := &a.trustedState
	keys := make([]string, len(rows))
	offered, current := false, false
	for i, r := range rows {
		keys[i] = r.key
		offered = offered || r.offered
		current = current || r.current
	}
	n, err := projectregistry.Untrust(keys...)
	if err != nil {
		st.note, st.noteErr = err.Error(), true
		return
	}
	notes := []string{fmt.Sprintf("Revoked trust for %d director%s.", n, plural(n))}
	if current {
		notes = append(notes, "This session keeps running; the next launch here asks again.")
	}
	if offered {
		notes = append(notes, "Remote control still lists it until restarted (/rc stop, then /rc start); sessions it starts there are refused.")
	}
	st.note, st.noteErr = strings.Join(notes, " "), false
	a.loadTrustedRows()
}

// stageTrustedAdd normalises the typed path and asks to confirm it.
func (a *App) stageTrustedAdd(raw string) {
	st := &a.trustedState
	raw = strings.TrimSpace(raw)
	if raw == "" {
		st.note, st.noteErr = "enter a directory path", true
		return
	}
	p := expandHomePath(raw)
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.workdir, p)
	}
	p, err := rc.Normalize(p)
	if err != nil {
		st.note, st.noteErr = err.Error(), true
		return
	}
	if s, err := trustgate.Check(p); err == nil && s.Trusted {
		st.note, st.noteErr = p+" is already trusted", false
		st.mode = ""
		a.editor.Reset()
		return
	}
	st.addPath, st.mode, st.note = p, "confirm-add", ""
	a.editor.Reset()
}

func (a *App) handleTrustedKey(m tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := &a.trustedState
	key := m.String()

	switch st.mode {
	case "add":
		switch key {
		case "esc":
			st.mode, st.note = "", ""
			a.editor.Reset()
			return a, nil
		case "enter":
			a.stageTrustedAdd(a.editor.Value())
			return a, nil
		}
		return a, a.editor.Update(m)
	case "confirm-add", "confirm-revoke", "confirm-prune", "confirm-shown":
		if key == "y" {
			if st.mode == "confirm-add" {
				if err := trustgate.Grant(st.addPath, nil); err != nil {
					st.note, st.noteErr = err.Error(), true
				} else {
					st.note, st.noteErr = "Trusted "+st.addPath+". Remote control offers it from its next start.", false
				}
				a.loadTrustedRows()
			} else {
				a.revokeTrusted(st.pending)
			}
		}
		if key == "y" || key == "n" || key == "esc" {
			st.mode, st.pending, st.addPath = "", nil, ""
		}
		return a, nil
	}

	if st.filtering {
		switch m.Type {
		case tea.KeyEsc:
			st.filtering, st.filter = false, ""
		case tea.KeyEnter:
			st.filtering = false
		case tea.KeyRunes:
			st.filter += string(m.Runes)
		case tea.KeySpace:
			st.filter += " "
		case tea.KeyBackspace:
			st.filter = trimLastRune(st.filter)
		case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown:
			a.moveTrusted(key)
			return a, nil
		default:
			return a, nil
		}
		st.selected, st.scroll = 0, 0
		return a, nil
	}

	shown := a.trustedShown()
	switch key {
	case "esc":
		if st.filter != "" {
			st.filter, st.selected, st.scroll = "", 0, 0
			return a, nil
		}
		a.pop()
	case "/":
		st.filtering, st.note = true, ""
	case "x", "delete":
		if st.selected < len(shown) {
			st.pending = []trustedRow{shown[st.selected]}
			st.mode, st.note = "confirm-revoke", ""
		}
	case "X":
		if st.filter == "" {
			st.note, st.noteErr = "Filter first (/), then X revokes every directory shown.", false
		} else if len(shown) > 0 {
			st.pending = append([]trustedRow(nil), shown...)
			st.mode, st.note = "confirm-shown", ""
		}
	case "p":
		if stale := a.trustedStale(); len(stale) > 0 {
			st.pending = stale
			st.mode, st.note = "confirm-prune", ""
		} else {
			st.note, st.noteErr = "No missing or temporary directories to revoke.", false
		}
	case "a":
		st.mode, st.note = "add", ""
		a.editor.Masked = false
		a.editor.Reset()
		_ = a.editor.Focus()
	case "r":
		st.note = ""
		a.loadTrustedRows()
	default:
		a.moveTrusted(key)
	}
	return a, nil
}

// moveTrusted moves the cursor for a navigation key; others are ignored.
func (a *App) moveTrusted(key string) {
	st := &a.trustedState
	n := len(a.trustedShown())
	page := max(1, a.trustedListRows()-1)
	switch key {
	case "up", "k":
		st.selected--
	case "down", "j":
		st.selected++
	case "pgup", "ctrl+u":
		st.selected -= page
	case "pgdown", "ctrl+d", " ":
		st.selected += page
	case "home", "g":
		st.selected = 0
	case "end", "G":
		st.selected = n - 1
	default:
		return
	}
	a.clampTrusted()
}

// trustedListRows is how many directory lines fit, given the chrome the
// current mode draws above and below the list.
func (a *App) trustedListRows() int {
	if a.height <= 0 {
		return 12
	}
	head, tail := a.trustedHead(a.contentWidth()), a.trustedTail(a.contentWidth())
	// Padding(1) adds a line top and bottom; one more keeps the footer clear.
	return max(3, a.height-lipgloss.Height(head)-lipgloss.Height(tail)-3)
}

func (a *App) trustedHead(w int) string {
	st := &a.trustedState
	var b strings.Builder
	count := fmt.Sprintf("%d", len(st.rows))
	if st.filter != "" {
		count = fmt.Sprintf("%d of %d", len(a.trustedShown()), len(st.rows))
	}
	b.WriteString(components.SectionHeader("Trusted directories", count, w))
	b.WriteString(ansi.Truncate(components.MutedStyle.Render("Belai reads, runs hooks and starts tools only in these. Remote control offers them to the website."), w, "…") + "\n")
	const label = "filter  "
	switch {
	case st.filtering:
		b.WriteString(components.AccentStyle.Render(label+st.filter+"▌") + "\n")
	case st.filter != "":
		b.WriteString(components.MutedStyle.Render(label) + components.EmphStyle.Render(st.filter) +
			components.MutedStyle.Render("  esc clears") + "\n")
	default:
		b.WriteString(components.MutedStyle.Render(label+"/ to filter by path, or missing, temporary, offered") + "\n")
	}
	return b.String()
}

func (a *App) trustedTail(w int) string {
	st := &a.trustedState
	muted := components.MutedStyle
	var b strings.Builder
	line := func(s string) { b.WriteString(ansi.Truncate(s, w, "…") + "\n") }
	if st.note != "" {
		style := muted
		if st.noteErr {
			style = components.DangerStyle
		}
		b.WriteString("\n" + lipgloss.NewStyle().Width(w).Render(style.Render(mcpClean(st.note, 400))) + "\n")
	}
	b.WriteString("\n")
	switch st.mode {
	case "add":
		b.WriteString(a.renderFieldEditor("directory to trust (absolute, ~/ or relative to this one)", w) + "\n")
	case "confirm-add":
		line(components.EmphStyle.Render("Trust " + st.addPath + "?"))
		line(muted.Render("Belai will read its files and settings there, and remote control will offer it. Its proposed workspace dirs are not accepted."))
		line(components.HelpBar("y", "trust", "n/esc", "cancel"))
	case "confirm-revoke", "confirm-prune", "confirm-shown":
		n := len(st.pending)
		switch {
		case st.mode == "confirm-revoke" && n == 1:
			line(components.EmphStyle.Render("Revoke trust for " + st.pending[0].path + "?"))
		case st.mode == "confirm-prune":
			line(components.EmphStyle.Render(fmt.Sprintf("Revoke trust for %d missing or temporary director%s?", n, plural(n))))
		default:
			line(components.EmphStyle.Render(fmt.Sprintf("Revoke trust for the %d director%s matching %q?", n, plural(n), st.filter)))
		}
		line(muted.Render("The next launch there asks again. Files, sessions and settings are left alone."))
		line(components.HelpBar("y", "revoke", "n/esc", "cancel"))
	default:
		if st.filtering {
			line(components.HelpBar("type", "filter", "↑↓", "move", "enter", "done", "esc", "clear"))
		} else {
			b.WriteString(lipgloss.NewStyle().Width(w).Render(components.HelpBar("↑↓ pgup pgdn", "move", "/", "filter", "x", "revoke", "X", "revoke shown", "p", "revoke missing+temp", "a", "add", "r", "refresh", "esc", "back")) + "\n")
		}
	}
	return b.String()
}

// trustedLine renders one directory on one line: the path, then its tags,
// then muted facts, truncated to the width.
func trustedLine(r trustedRow, selected bool, w int) string {
	path := r.path
	if selected {
		path = components.AccentStyle.Bold(true).Render(path)
	}
	var tags []string
	if r.current {
		tags = append(tags, components.Chip("this session", components.ColorTealSoft))
	}
	if r.offered {
		tags = append(tags, components.AccentStyle.Render("offered by rc"))
	}
	if r.missing {
		tags = append(tags, components.DangerStyle.Render("missing"))
	}
	if r.temp {
		tags = append(tags, components.WarnStyle.Render("temporary"))
	}
	var facts []string
	if !r.entry.TrustedAt.IsZero() {
		facts = append(facts, "trusted "+r.entry.TrustedAt.Format("2006-01-02"))
	}
	if !r.entry.LastSeen.IsZero() {
		facts = append(facts, "opened "+r.entry.LastSeen.Format("2006-01-02"))
	}
	if n := len(r.entry.WorkspaceDirs); n > 0 {
		facts = append(facts, fmt.Sprintf("+%d workspace", n))
	}
	s := components.Cursor(selected) + path
	if len(tags) > 0 {
		s += "  " + strings.Join(tags, " ")
	}
	if len(facts) > 0 {
		s += "  " + components.MutedStyle.Render(strings.Join(facts, " · "))
	}
	return ansi.Truncate(s, w, "…")
}

func (a *App) trustedView() string {
	w := a.contentWidth()
	st := &a.trustedState
	shown := a.trustedShown()
	head, tail := a.trustedHead(w), a.trustedTail(w)
	nrows := a.trustedListRows()

	var body strings.Builder
	switch {
	case len(st.rows) == 0:
		body.WriteString(components.MutedStyle.Render("  No trusted directories. Press a to add one.") + "\n")
	case len(shown) == 0:
		body.WriteString(components.MutedStyle.Render("  Nothing matches the filter.") + "\n")
	default:
		// An overflowing list keeps a line above and below for the
		// "more" markers, so the window never jumps as they appear.
		overflow := len(shown) > nrows
		if overflow {
			nrows = max(1, nrows-2)
		}
		st.scroll = windowStart(st.scroll, st.selected, len(shown), nrows)
		end := min(st.scroll+nrows, len(shown))
		marker := func(n int, arrow string) {
			if n > 0 {
				body.WriteString(components.MutedStyle.Render(fmt.Sprintf("  %s %d more", arrow, n)))
			}
			body.WriteString("\n")
		}
		if overflow {
			marker(st.scroll, "↑")
		}
		for i := st.scroll; i < end; i++ {
			body.WriteString(trustedLine(shown[i], i == st.selected, w) + "\n")
		}
		if overflow {
			marker(len(shown)-end, "↓")
		}
	}
	return lipgloss.NewStyle().Padding(1).Render(head + "\n" + body.String() + tail)
}

// trustedStatus summarises the screen for the switcher row.
func (a *App) trustedStatus() string {
	reg, err := projectregistry.Load()
	if err != nil {
		return ""
	}
	n := 0
	for _, e := range reg.All() {
		if e.Trusted {
			n++
		}
	}
	return fmt.Sprintf("%d trusted", n)
}
