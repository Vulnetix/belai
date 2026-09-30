package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/tui/components"
)

// The grouped list is the layout for a screen with more rows than a terminal
// holds: a rail of groups, one group's rows at a time, and a detail pane for
// the selected row. It fits any height (rows scroll inside their own box) and
// any width (a tab strip replaces the rail below glSideBySide columns).
//
// It is layout and navigation only. A caller owns its rows, its values and every
// write, and hands the widget plain strings, so the widget never sees a
// setting, a rule or a credential.

const (
	// glSideBySide is the content width from which the rail sits beside the
	// list; narrower terminals get a tab strip above it. It is the /model
	// screen's breakpoint, so both screens switch layout together.
	glSideBySide = modelSideBySide
	// glRailWide is the rail's width beside the list.
	glRailWide = 26
	// glSrcWidth caps the source column.
	glSrcWidth = 13
	// glDetailRows is the detail box's content rows when the body has room.
	glDetailRows = 4
)

// glRow is one line of a group. A header row labels a run of rows under it and
// never takes the cursor.
type glRow struct {
	idx    int // the caller's own index for the row; -1 on a header
	key    string
	label  string
	value  string // plain text
	src    string // where the value comes from; "default" and "" draw nothing
	dim    bool   // reads as not in use, though it stays editable
	header bool
	detail []string // the detail pane's lines, already styled
	// match is the text the filter searches (label, key and help).
	match string
}

// glGroup is one rail entry.
type glGroup struct {
	key   string
	title string
	rows  []glRow
}

// changed reports whether any row's value differs from its default.
func (g glGroup) changed() bool {
	for _, r := range g.rows {
		if !r.header && glOverridden(r.src) {
			return true
		}
	}
	return false
}

func (g glGroup) selectable() int {
	n := 0
	for _, r := range g.rows {
		if !r.header {
			n++
		}
	}
	return n
}

func glOverridden(src string) bool { return src != "" && src != "default" }

// glState is the navigation state of one grouped list.
type glState struct {
	scroll    int // first visible row of the list, kept across frames
	filter    string
	filtering bool // keys edit the filter
	changed   bool // only rows whose value is not the default
}

// active reports whether the list shows results instead of groups.
func (st *glState) active() bool { return st.filter != "" || st.changed }

// glKey is one key in the footer.
type glKey struct {
	key, label string
	prio       int // lower is kept longer
}

// glHelpLine renders keys as one line, dropping the least important first
// until it fits w, so movement, edit and back always stay on screen.
func glHelpLine(keys []glKey, w int) string {
	render := func(ks []glKey) string {
		pairs := make([]string, 0, 2*len(ks))
		for _, k := range ks {
			pairs = append(pairs, k.key, k.label)
		}
		return components.HelpBar(pairs...)
	}
	keys = append([]glKey(nil), keys...)
	for ansi.StringWidth(render(keys)) > w && len(keys) > 1 {
		drop, worst := -1, 0
		for i, k := range keys {
			if k.prio >= worst {
				drop, worst = i, k.prio
			}
		}
		keys = append(keys[:drop], keys[drop+1:]...)
	}
	return render(keys)
}

// glVisible is what the list shows: the groups as given, or, while a filter or
// the changed-only view is on, one "Results" group of the matching rows drawn
// across every group.
func (st *glState) visible(groups []glGroup) []glGroup {
	if !st.active() {
		return groups
	}
	q := strings.ToLower(strings.TrimSpace(st.filter))
	res := glGroup{key: "results", title: "Results"}
	for _, g := range groups {
		for _, r := range g.rows {
			if r.header {
				continue
			}
			if st.changed && !glOverridden(r.src) {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(r.match+" "+r.label+" "+r.key), q) {
				continue
			}
			r.detail = append([]string{components.MutedStyle.Render("in " + g.title)}, r.detail...)
			res.rows = append(res.rows, r)
		}
	}
	return []glGroup{res}
}

// glGroupIndex is the position of the group holding row idx, or 0.
func glGroupIndex(groups []glGroup, idx int) int {
	for gi, g := range groups {
		for _, r := range g.rows {
			if !r.header && r.idx == idx {
				return gi
			}
		}
	}
	return 0
}

// glFirst is the first selectable row of a group, or -1.
func glFirst(g glGroup) int {
	for _, r := range g.rows {
		if !r.header {
			return r.idx
		}
	}
	return -1
}

// glSnap returns cur if it is a visible row, else the first visible one, so a
// filter that hides the cursor's row moves the cursor rather than losing it.
func glSnap(groups []glGroup, cur int) int {
	for _, g := range groups {
		for _, r := range g.rows {
			if !r.header && r.idx == cur {
				return cur
			}
		}
	}
	for _, g := range groups {
		if i := glFirst(g); i >= 0 {
			return i
		}
	}
	return cur
}

// glStep moves the cursor delta selectable rows inside its group, clamped at
// both ends.
func glStep(groups []glGroup, cur, delta int) int {
	if len(groups) == 0 {
		return cur
	}
	g := groups[glGroupIndex(groups, cur)]
	var idxs []int
	pos := 0
	for _, r := range g.rows {
		if r.header {
			continue
		}
		if r.idx == cur {
			pos = len(idxs)
		}
		idxs = append(idxs, r.idx)
	}
	if len(idxs) == 0 {
		return cur
	}
	pos = max(0, min(len(idxs)-1, pos+delta))
	return idxs[pos]
}

// glStepGroup moves to the first row of the adjacent group, wrapping, and skips
// a group with nothing selectable.
func glStepGroup(groups []glGroup, cur, delta int) int {
	n := len(groups)
	if n == 0 {
		return cur
	}
	gi := glGroupIndex(groups, cur)
	for i := 1; i <= n; i++ {
		next := ((gi+delta*i)%n + n) % n
		if first := glFirst(groups[next]); first >= 0 {
			return first
		}
	}
	return cur
}

// key applies a filter-editing key. It returns true when the key was consumed.
func (st *glState) key(m tea.KeyMsg) bool {
	if !st.filtering {
		return false
	}
	switch m.String() {
	case "esc":
		st.filter, st.filtering, st.scroll = "", false, 0
	case "enter":
		st.filtering = false
	case "backspace":
		if r := []rune(st.filter); len(r) > 0 {
			st.filter = string(r[:len(r)-1])
		}
		st.scroll = 0
	case "ctrl+u":
		st.filter, st.scroll = "", 0
	default:
		if m.Type == tea.KeyRunes && !m.Alt {
			st.filter += string(m.Runes)
			st.scroll = 0
		} else if m.Type == tea.KeySpace {
			st.filter += " "
			st.scroll = 0
		}
	}
	return true
}

// glSpec is what one frame of the body draws.
type glSpec struct {
	groups []glGroup // the visible groups, from glState.visible
	cursor int       // the selected row's idx
	// empty is the list's text when nothing matches.
	empty string
}

// glBody draws the rail (or tab strip), the list and the detail pane in bodyH
// rows. bodyH <= 0 means unclipped. It writes st.scroll.
func glBody(spec glSpec, st *glState, w, bodyH int) string {
	groups := spec.groups
	gi := glGroupIndex(groups, spec.cursor)
	side := w >= glSideBySide && !st.active()

	var cur glRow
	var grp glGroup
	if len(groups) > 0 {
		grp = groups[gi]
		for _, r := range grp.rows {
			if !r.header && r.idx == spec.cursor {
				cur = r
			}
		}
	}

	listW := w
	if side {
		listW = w - glRailWide - 1
	}
	stripH := 0
	if !side && !st.active() {
		stripH = 1
	}
	// The detail box takes what the row's text needs, up to a cap that grows
	// with the terminal, so a short screen keeps its list.
	detailCap := glDetailRows
	switch {
	case bodyH > 0 && bodyH < 12:
		detailCap = 2
	case bodyH >= 30:
		detailCap = 7
	}
	detailRows := max(min(len(glDetailLines(cur, listW-2, 0)), detailCap), 2)
	detailH := detailRows + 2
	listH := 0
	if bodyH > 0 {
		listH = max(bodyH-detailH-stripH, 5)
	}

	list := glListBox(grp, cur, spec, st, listW, listH)
	detail := box(glDetailLines(cur, listW-2, detailRows), listW, detailH)
	right := list + "\n" + detail

	var b strings.Builder
	switch {
	case side:
		rail := box(glRailLines(groups, gi, glRailWide-2, bodyH-2), glRailWide, bodyH)
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, rail, " ", right))
	case stripH > 0:
		b.WriteString(glTabStrip(groups, gi, w) + "\n" + right)
	default:
		b.WriteString(right)
	}
	return b.String()
}

// glRailLines lists the groups, windowed around the current one when the box
// is shorter than the list.
func glRailLines(groups []glGroup, cur, iw, ih int) []string {
	lines := make([]string, 0, len(groups)+1)
	for i, g := range groups {
		dot := " "
		if g.changed() {
			dot = components.AccentStyle.Render("●")
		}
		count := components.MutedStyle.Render(fmt.Sprintf("%d", g.selectable()))
		title := g.title
		if i == cur {
			title = components.AccentStyle.Bold(true).Render(title)
		}
		gap := max(iw-2-1-1-ansi.StringWidth(g.title)-ansi.StringWidth(fmt.Sprintf("%d", g.selectable())), 1)
		lines = append(lines, components.Cursor(i == cur)+dot+" "+title+strings.Repeat(" ", gap)+count)
	}
	if ih > 0 && len(lines) > ih {
		start := windowStart(0, cur, len(lines), ih)
		lines = lines[start : start+ih]
	}
	return lines
}

// glTabStrip is the rail for narrow terminals: one line, the current group
// highlighted, scrolled so it is always on screen.
func glTabStrip(groups []glGroup, cur, w int) string {
	if len(groups) == 0 {
		return ""
	}
	tab := func(i int) string {
		g := groups[i]
		name := g.title
		if g.changed() {
			name += " ●"
		}
		if i == cur {
			return components.Chip(name, components.ColorTeal)
		}
		return components.MutedStyle.Render(" " + name + " ")
	}
	count := components.MutedStyle.Render(fmt.Sprintf("%d/%d", cur+1, len(groups)))
	room := w - ansi.StringWidth(count) - 1
	start, end := cur, cur+1
	width := ansi.StringWidth(tab(cur))
	for grew := true; grew; {
		grew = false
		if end < len(groups) {
			if x := ansi.StringWidth(tab(end)) + 1; width+x <= room-2 {
				width += x
				end++
				grew = true
			}
		}
		if start > 0 {
			if x := ansi.StringWidth(tab(start-1)) + 1; width+x <= room-2 {
				width += x
				start--
				grew = true
			}
		}
	}
	parts := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		parts = append(parts, tab(i))
	}
	left, right := " ", " "
	if start > 0 {
		left = components.MutedStyle.Render("‹")
	}
	if end < len(groups) {
		right = components.MutedStyle.Render("›")
	}
	line := left + strings.Join(parts, " ") + right
	if gap := w - ansi.StringWidth(line) - ansi.StringWidth(count); gap > 0 {
		return line + strings.Repeat(" ", gap) + count
	}
	return ansi.Truncate(line, w, "…")
}

// glListBox draws the group's rows in a box listH tall, scrolled to keep the
// cursor visible with a marker line for what is above and below.
func glListBox(g glGroup, cur glRow, spec glSpec, st *glState, w, h int) string {
	iw := w - 2
	var lines []string
	if st.active() || st.filtering {
		q := st.filter
		if st.filtering {
			q += "▏"
		}
		tag := components.AccentStyle.Render("/") + " " + q
		if st.changed {
			tag += components.MutedStyle.Render("  changed only")
		}
		tag += components.MutedStyle.Render(fmt.Sprintf("  %d match%s", g.selectable(), map[bool]string{true: "", false: "es"}[g.selectable() == 1]))
		lines = append(lines, tag)
	} else {
		lines = append(lines, components.MutedStyle.Render(strings.ToUpper(g.title)))
	}
	head := len(lines)

	labelW, srcW := 8, 0
	for _, r := range g.rows {
		if r.header {
			continue
		}
		labelW = max(labelW, ansi.StringWidth(r.label))
		if glOverridden(r.src) {
			srcW = max(srcW, min(ansi.StringWidth(r.src), glSrcWidth))
		}
	}
	labelW = min(labelW+2, max(iw/3, 12))
	valW := max(iw-2-labelW-srcW-1, 8)

	var rowLines []string
	pos := 0
	for i, r := range g.rows {
		if r.header {
			rowLines = append(rowLines, components.MutedStyle.Render(" "+r.label))
			continue
		}
		selected := r.idx == spec.cursor
		if selected {
			pos = i
			if i > 0 && g.rows[i-1].header {
				pos = i - 1 // keep a run's heading with its first row
			}
		}
		label := padRight(ansi.Truncate(r.label, labelW-1, "…"), labelW)
		value := padRight(ansi.Truncate(r.value, valW, "…"), valW)
		switch {
		case selected:
			label = components.AccentStyle.Bold(true).Render(label)
			value = components.EmphStyle.Render(value)
		case r.dim:
			label = components.MutedStyle.Render(label)
			value = components.MutedStyle.Render(value)
		default:
			label = components.MutedStyle.Render(label)
		}
		line := components.Cursor(selected) + label + value
		if srcW > 0 && glOverridden(r.src) {
			line += " " + components.AccentStyle.Render(ansi.Truncate(r.src, glSrcWidth, "…"))
		}
		rowLines = append(rowLines, line)
	}
	if len(g.rows) == 0 {
		msg := spec.empty
		if msg == "" {
			msg = "nothing here"
		}
		rowLines = append(rowLines, components.MutedStyle.Render("  "+msg))
	}

	avail := len(rowLines)
	if h > 0 {
		avail = max(h-2-head, 1)
	}
	if len(rowLines) <= avail {
		st.scroll = 0
		return box(append(lines, rowLines...), w, h)
	}
	win := max(avail-1, 1)
	st.scroll = windowStart(st.scroll, pos, len(rowLines), win)
	lines = append(lines, rowLines[st.scroll:st.scroll+win]...)
	above, below := st.scroll, len(rowLines)-st.scroll-win
	var marks []string
	if above > 0 {
		marks = append(marks, fmt.Sprintf("↑ %d above", above))
	}
	if below > 0 {
		marks = append(marks, fmt.Sprintf("↓ %d below", below))
	}
	return box(append(lines, components.MutedStyle.Render("  "+strings.Join(marks, " · "))), w, h)
}

// glDetailLines is the detail pane: the row's own lines, cut to rows.
func glDetailLines(r glRow, iw, rows int) []string {
	if len(r.detail) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.detail))
	for _, l := range r.detail {
		out = append(out, wrapCells(l, iw)...)
	}
	if rows > 0 && len(out) > rows {
		// The last line says where the value comes from and is saved, so it
		// outlasts the prose above it.
		last := out[len(out)-1]
		out = append(out[:rows-1], last)
	}
	return out
}

// wrapCells breaks one styled line at spaces into lines of at most w cells.
func wrapCells(s string, w int) []string {
	if w < 8 || ansi.StringWidth(s) <= w {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, w, ""), "\n")
}
