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

type trustedViewState struct {
	rows     []trustedRow
	selected int
	// mode is "" when browsing, "add" while the editor holds a path, or
	// "confirm-add" / "confirm-revoke" / "confirm-prune" awaiting y/n.
	mode    string
	pending []trustedRow // what a revoke or prune confirmation applies to
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
	if st.selected >= len(st.rows) {
		st.selected = max(0, len(st.rows)-1)
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
	case "confirm-add", "confirm-revoke", "confirm-prune":
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

	switch key {
	case "esc":
		a.pop()
	case "up", "k":
		if st.selected > 0 {
			st.selected--
		}
	case "down", "j":
		if st.selected < len(st.rows)-1 {
			st.selected++
		}
	case "x", "delete", "backspace":
		if st.selected < len(st.rows) {
			st.pending = []trustedRow{st.rows[st.selected]}
			st.mode, st.note = "confirm-revoke", ""
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
	}
	return a, nil
}

func (a *App) trustedView() string {
	w := a.contentWidth()
	st := &a.trustedState
	muted := components.MutedStyle
	var b strings.Builder
	line := func(s string) { b.WriteString(ansi.Truncate(s, w, "…") + "\n") }

	b.WriteString(components.SectionHeader("Trusted directories", fmt.Sprintf("%d", len(st.rows)), w))
	line(muted.Render("Belai reads, runs hooks and starts tools only in these. Remote control offers them to the website."))
	line("")

	if len(st.rows) == 0 {
		line(muted.Render("No trusted directories. Press a to add one."))
	}
	for i, r := range st.rows {
		selected := i == st.selected
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
			tags = append(tags, components.WarnStyle.Render("temporary · rc hides it"))
		}
		row := components.Cursor(selected) + path
		if len(tags) > 0 {
			row += "  " + strings.Join(tags, " ")
		}
		line(row)
		var facts []string
		if !r.entry.TrustedAt.IsZero() {
			facts = append(facts, "trusted "+r.entry.TrustedAt.Format("2006-01-02"))
		}
		if !r.entry.LastSeen.IsZero() {
			facts = append(facts, "last opened "+r.entry.LastSeen.Format("2006-01-02"))
		}
		if n := len(r.entry.WorkspaceDirs); n > 0 {
			facts = append(facts, fmt.Sprintf("%d workspace dir%s", n, map[bool]string{true: "", false: "s"}[n == 1]))
		}
		if len(facts) > 0 {
			line(muted.Render("    " + strings.Join(facts, " · ")))
		}
	}

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
		b.WriteString(components.HelpBar("y", "trust", "n/esc", "cancel") + "\n")
	case "confirm-revoke", "confirm-prune":
		n := len(st.pending)
		if st.mode == "confirm-revoke" && n == 1 {
			line(components.EmphStyle.Render("Revoke trust for " + st.pending[0].path + "?"))
		} else {
			line(components.EmphStyle.Render(fmt.Sprintf("Revoke trust for %d missing or temporary director%s?", n, plural(n))))
		}
		line(muted.Render("The next launch there asks again. Files, sessions and settings are left alone."))
		b.WriteString(components.HelpBar("y", "revoke", "n/esc", "cancel") + "\n")
	default:
		b.WriteString(components.HelpBar("↑↓", "move", "x", "revoke", "p", "revoke missing/temp", "a", "add", "r", "refresh", "esc", "back") + "\n")
	}
	return lipgloss.NewStyle().Padding(1).Render(b.String())
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
