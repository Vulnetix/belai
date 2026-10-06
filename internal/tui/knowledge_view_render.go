package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/tui/components"
)

// knowledgeLayout splits the screen's height between the list and the lower
// pane (detail, document or search result). The pane takes a third of the
// height, at least five lines; on a short terminal it gives lines back so the
// list keeps three and the screen still fits.
func (a *App) knowledgeLayout() (list, pane int) {
	if a.height <= 0 {
		return 10, 8
	}
	w := a.contentWidth()
	head, tail := a.knowledgeHead(w), a.knowledgeTail(w)
	// Padding(1) adds a line top and bottom; the pane has a title line and a
	// blank line above it.
	room := a.height - lipgloss.Height(head) - lipgloss.Height(tail) - 5
	pane = max(5, min(14, a.height/3))
	if room-pane < 3 {
		pane = max(2, room-3)
	}
	return max(3, room-pane), pane
}

func (a *App) knowledgePaneRows() int {
	_, pane := a.knowledgeLayout()
	return pane
}

func (a *App) knowledgeListRows() int {
	list, _ := a.knowledgeLayout()
	return list
}

func (a *App) knowledgeTabStrip(w int) string {
	st := &a.knowledgeState
	counts := [knowTabCount]string{"", "", ""}
	if ix := a.knowStore; ix != nil {
		n := 0
		for _, docs := range ix.Docs() {
			n += len(docs)
		}
		counts[knowTabProject] = fmt.Sprintf(" %d", n)
	}
	if !st.loading {
		counts[knowTabGlobal] = fmt.Sprintf(" %d", len(st.projects))
		counts[knowTabAgents] = fmt.Sprintf(" %d", len(st.agents))
	}
	var parts, plain []string
	for i, name := range knowledgeTabNames {
		label := name + counts[i]
		if i == st.tab {
			label = "[ " + label + " ]"
			parts = append(parts, components.EmphStyle.Render(label))
		} else {
			parts = append(parts, components.MutedStyle.Render(label))
		}
		plain = append(plain, label)
	}
	left := strings.Join(parts, "   ")
	meta := "1 2 3 · tab"
	pad := w - lipgloss.Width(strings.Join(plain, "   ")) - lipgloss.Width(meta)
	if pad < 2 {
		return ansi.Truncate(left, w, "")
	}
	return left + strings.Repeat(" ", pad) + components.MutedStyle.Render(meta)
}

func (a *App) knowledgeScopeNote() string {
	st := &a.knowledgeState
	switch st.tab {
	case knowTabProject:
		return "what a model in this directory can search: the engaged agent's documents, this project's .vulnetix output and your @ files"
	case knowTabGlobal:
		if p := a.knowledgePick(); p != "" {
			for _, e := range st.projects {
				if e.info.Key == p {
					return "project " + mcpClean(e.title, 60) + " · esc shows every project"
				}
			}
		}
		return "every project indexed on this host · enter picks one"
	}
	if p := a.knowledgePick(); p != "" {
		for _, e := range st.agents {
			if e.info.Key == p {
				return "agent " + mcpClean(e.title, 60) + " · esc shows every agent"
			}
		}
	}
	return "the documents each agent profile lists · enter picks one"
}

func (a *App) knowledgeHead(w int) string {
	st := &a.knowledgeState
	var b strings.Builder
	subtitle := ""
	switch {
	case st.loading:
		subtitle = "loading…"
	case st.filter != "":
		subtitle = fmt.Sprintf("%d shown", len(a.knowledgeShown()))
	}
	b.WriteString(components.SectionHeader("Knowledge", subtitle, w))
	b.WriteString(a.knowledgeTabStrip(w) + "\n")
	b.WriteString(ansi.Truncate(components.MutedStyle.Render(a.knowledgeScopeNote()), w, "…") + "\n")
	line := func(s string) { b.WriteString(ansi.Truncate(s, w, "…") + "\n") }
	const flabel = "filter  "
	switch {
	case st.filtering:
		line(components.AccentStyle.Render(flabel + st.filter + "▌"))
	case st.filter != "":
		line(components.MutedStyle.Render(flabel) + components.EmphStyle.Render(st.filter) + components.MutedStyle.Render("  esc clears"))
	default:
		line(components.MutedStyle.Render(flabel + "/ filters by words, type:, lang:, topic:, kind:, tool:"))
	}
	modes := make([]string, knowModeCount)
	for i, n := range knowledgeModeNames {
		if i == st.mode {
			modes[i] = components.EmphStyle.Render("[" + n + "]")
		} else {
			modes[i] = components.MutedStyle.Render(n)
		}
	}
	const slabel = "search  "
	switch {
	case st.searching:
		line(components.AccentStyle.Render(slabel+st.query+"▌") + "  " + strings.Join(modes, " "))
	case st.query != "":
		line(components.MutedStyle.Render(slabel) + components.EmphStyle.Render(st.query) + "  " + strings.Join(modes, " "))
	default:
		line(components.MutedStyle.Render(slabel+"s searches like a model's ") + strings.Join(modes, " "))
	}
	return b.String()
}

func (a *App) knowledgeTail(w int) string {
	st := &a.knowledgeState
	var b strings.Builder
	if st.err != "" {
		b.WriteString(lipgloss.NewStyle().Width(w).Render(components.DangerStyle.Render(st.err)) + "\n")
	}
	switch {
	case st.searching:
		b.WriteString(components.HelpBar("type", "query", "tab", "grep/glob/read", "enter", "run", "esc", "close") + "\n")
	case st.filtering:
		b.WriteString(components.HelpBar("type", "filter", "↑↓", "move", "enter", "done", "esc", "clear") + "\n")
	case st.showDoc:
		b.WriteString(components.HelpBar("↑↓ pgup pgdn", "scroll", "esc", "close") + "\n")
	case st.ran && len(st.result) > 0:
		b.WriteString(lipgloss.NewStyle().Width(w).Render(components.HelpBar("↑↓ pgup pgdn", "move", "enter", "open document", "s", "new search", "m", "mode", "esc", "clear")) + "\n")
	default:
		b.WriteString(lipgloss.NewStyle().Width(w).Render(components.HelpBar("1 2 3", "scope", "↑↓", "move", "enter", "open", "/", "filter", "s", "search", "m", "mode", "r", "reload", "esc", "back")) + "\n")
	}
	return b.String()
}

// knowledgeLine renders one list row on one line.
func (a *App) knowledgeLine(r knowledgeRow, selected bool, w int) string {
	muted := components.MutedStyle
	if r.kind == knowRowIndex {
		e := a.knowledgeEntries()[r.entry]
		title := mcpClean(e.title, 60)
		if selected {
			title = components.AccentStyle.Bold(true).Render(title)
		}
		s := components.Cursor(selected) + title
		if e.current {
			s += "  " + components.Chip("this project", components.ColorTealSoft)
		}
		switch {
		case e.info.Err != nil:
			s += "  " + components.DangerStyle.Render("unreadable")
		case e.info.Index != nil && e.info.Exists():
			s += "  " + muted.Render(fmt.Sprintf("%d documents · %d tokens", e.info.Docs(), e.info.Tokens()))
		}
		if e.note != "" {
			s += "  " + muted.Render(mcpClean(e.note, 40))
		}
		if e.path != "" {
			s += "  " + muted.Render(mcpClean(e.path, 120))
		}
		return ansi.Truncate(s, w, "…")
	}
	addr := strings.TrimPrefix(mcpClean(r.doc.Address, 200), "kb+")
	if selected {
		addr = components.AccentStyle.Bold(true).Render(addr)
	}
	s := components.Cursor(selected) + addr
	if k := r.doc.Tags.Kind; k != "" {
		s += "  " + muted.Render(k)
	}
	if l := r.doc.Tags.Lang; l != "" {
		s += muted.Render(" · " + l)
	}
	var topics []string
	for i, t := range r.doc.Tags.Topics {
		if i == 3 {
			break
		}
		topics = append(topics, t.ID)
	}
	if len(topics) > 0 {
		s += "  " + components.AccentStyle.Render(strings.Join(topics, " "))
	}
	return ansi.Truncate(s, w, "…")
}

// knowledgeDetail is the lines describing the selected row.
func (a *App) knowledgeDetail(shown []knowledgeRow) []string {
	st := &a.knowledgeState
	muted := components.MutedStyle
	if st.selected >= len(shown) {
		return []string{muted.Render("nothing selected")}
	}
	r := shown[st.selected]
	if r.kind == knowRowIndex {
		e := a.knowledgeEntries()[r.entry]
		lines := []string{components.EmphStyle.Render(mcpClean(e.title, 80))}
		if e.path != "" {
			lines = append(lines, muted.Render(mcpClean(e.path, 200)))
		}
		switch {
		case e.info.Err != nil:
			lines = append(lines, components.DangerStyle.Render("the index file failed its integrity check or could not be read; it is left as it is"))
		case !e.info.Exists():
			lines = append(lines, muted.Render("no index yet: "+firstNonEmpty(e.note, "it is built on the next refresh")))
		default:
			docs := e.info.Index.Docs()
			lines = append(lines, fmt.Sprintf("%d documents · %d tokens · %s on disk · updated %s",
				len(docs), e.info.Tokens(), formatBytesShort(e.info.Bytes), e.info.Updated.Format("2006-01-02 15:04")))
			if t := topTags(docs, "type:", 6); len(t) > 0 {
				lines = append(lines, muted.Render("types   ")+strings.Join(t, " · "))
			}
			if t := topTags(docs, "lang:", 6); len(t) > 0 {
				lines = append(lines, muted.Render("langs   ")+strings.Join(t, " · "))
			}
			if t := topTags(docs, "topic:", 8); len(t) > 0 {
				var named []string
				for _, x := range t {
					id, n, _ := strings.Cut(x, " ")
					named = append(named, topicLabel(id)+" "+n)
				}
				lines = append(lines, muted.Render("topics  ")+strings.Join(named, " · "))
			}
			lines = append(lines, muted.Render("enter lists its documents"))
		}
		return lines
	}
	d := r.doc
	lines := []string{components.EmphStyle.Render(mcpClean(d.Address, 200))}
	meta := []string{}
	if d.Tags.Kind != "" {
		meta = append(meta, "type "+d.Tags.Kind)
	}
	if d.Tags.Lang != "" {
		meta = append(meta, d.Tags.Lang)
	}
	meta = append(meta, formatBytesShort(d.Size), fmt.Sprintf("%d chunks", d.Chunks), fmt.Sprintf("%d tokens", d.Tokens))
	if d.Dropped > 0 {
		meta = append(meta, fmt.Sprintf("%d dropped by the classifier", d.Dropped))
	}
	if d.Truncated {
		meta = append(meta, "truncated by the cap")
	}
	lines = append(lines, strings.Join(meta, " · "))
	switch d.Tags.Src {
	case tags.SrcJev:
		lines = append(lines, muted.Render("topics scored by the decision backend"))
	case tags.SrcRegex:
		lines = append(lines, muted.Render("topics found by the pattern detector"))
	default:
		lines = append(lines, muted.Render("not tagged yet: the next refresh tags it"))
	}
	if len(d.Tags.Topics) > 0 {
		var ts []string
		for _, t := range d.Tags.Topics {
			ts = append(ts, fmt.Sprintf("%s %d%%", topicLabel(t.ID), int(t.Score*100+0.5)))
		}
		lines = append(lines, muted.Render("topics  ")+strings.Join(ts, " · "))
	}
	if len(d.Tags.Labels) > 0 {
		lines = append(lines, muted.Render("labels  ")+strings.Join(d.Tags.Labels, " "))
	}
	lines = append(lines, muted.Render("enter shows the indexed text"))
	return lines
}

// knowledgePane draws the lower pane: a document, a search result or the
// selected row's detail.
func (a *App) knowledgePane(w int, shown []knowledgeRow) string {
	st := &a.knowledgeState
	rows := a.knowledgePaneRows()
	muted := components.MutedStyle
	var title string
	var lines []string
	cursor := -1
	top := 0
	switch {
	case st.showDoc:
		title = "indexed text · " + mcpClean(st.docTitle, 120)
		lines = st.docLines
		top = st.docTop
	case st.ran:
		title = "as a model's tool: " + mcpClean(st.resultCmd, 160)
		lines = st.result
		cursor = st.resultSel
		top = windowStart(st.resultTop, st.resultSel, len(lines), rows)
		st.resultTop = top
	default:
		title = "detail"
		lines = a.knowledgeDetail(shown)
	}
	var b strings.Builder
	b.WriteString(muted.Render("── "+title) + "\n")
	end := min(top+rows, len(lines))
	for i := top; i < end; i++ {
		l := lines[i]
		if i == cursor {
			l = components.AccentStyle.Render("› ") + l
		} else if cursor >= 0 {
			l = "  " + l
		}
		b.WriteString(ansi.Truncate(l, w, "…") + "\n")
	}
	for i := end - top; i < rows; i++ {
		b.WriteString("\n")
	}
	return b.String()
}

func (a *App) knowledgeView() string {
	w := a.contentWidth()
	st := &a.knowledgeState
	shown := a.knowledgeShown()
	head, tail := a.knowledgeHead(w), a.knowledgeTail(w)
	nrows := a.knowledgeListRows()

	var body strings.Builder
	switch {
	case st.loading && st.tab != knowTabProject:
		body.WriteString(components.MutedStyle.Render("  Reading the indexes…") + "\n")
	case len(shown) == 0 && st.filter != "":
		body.WriteString(components.MutedStyle.Render("  Nothing matches the filter.") + "\n")
	case len(shown) == 0:
		body.WriteString(components.MutedStyle.Render(a.knowledgeEmpty()) + "\n")
	default:
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
			body.WriteString(a.knowledgeLine(shown[i], i == st.selected, w) + "\n")
		}
		if overflow {
			marker(len(shown)-end, "↓")
		}
	}
	return lipgloss.NewStyle().Padding(1).Render(head + "\n" + body.String() + "\n" + a.knowledgePane(w, shown) + tail)
}

func (a *App) knowledgeEmpty() string {
	switch a.knowledgeState.tab {
	case knowTabProject:
		return "  Nothing is indexed for this project yet. An agent with knowledge.paths, or .vulnetix output, fills it."
	case knowTabGlobal:
		return "  No project on this host has an index yet."
	}
	return "  No agent profile lists documents yet (knowledge.paths in a profile)."
}

// knowledgeStatus summarises the screen for the switcher row.
func (a *App) knowledgeStatus() string {
	if a.knowStore == nil {
		return ""
	}
	n := 0
	for _, docs := range a.knowStore.Docs() {
		n += len(docs)
	}
	return fmt.Sprintf("%d documents here", n)
}
