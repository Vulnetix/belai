package components

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// MD is a rendered markdown block: one Row per screen line, plus the source
// line index each Row came from so truncation can recover the raw remainder.
// Every Row has exactly one Src entry, in order.
type MD struct {
	Rows []Row
	Src  []int
}

// mermaidRenderer lets an external Mermaid renderer be dropped in without
// touching the parser. nil means ```mermaid fences render as a labelled,
// unhighlighted code block.
var mermaidRenderer func(src string, width int) []Row

// RenderMarkdown renders markdown at the given inner width, hand-rolled and
// line-oriented so the result stays on the Seg/Row/renderRows primitives and
// every SourceLine keeps the LineMap invariant. It never wraps a Seg that
// already carries escapes: wrapping happens on the plain concatenation and the
// segments are re-sliced afterwards.
func RenderMarkdown(src string, width int) MD {
	if width < 1 {
		width = 1
	}
	lines := strings.Split(src, "\n")
	var rows []Row
	var srcIdx []int
	i := 0
	for i < len(lines) {
		line := lines[i]
		switch {
		case mdBlank(line):
			i++
		case isFence(line):
			r, s, ni := renderFence(lines, i, width)
			rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
		case isATX(line):
			level, text, _ := mdATX(line)
			r, s := renderHeading(text, level, width, i)
			rows, srcIdx, i = appendMD(rows, srcIdx, r, s, i+1)
		case mdThematic(line):
			rows = append(rows, Row{Segs: []Seg{NewSeg(repeatRune('─', width), ColorLine)}})
			srcIdx = append(srcIdx, i)
			i++
		case isBlockquote(line):
			r, s, ni := renderBlockquote(lines, i, width)
			rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
		case mdTableStart(lines, i):
			r, s, ni := renderTable(lines, i, width)
			rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
		case mdIndent(line) >= 4:
			r, s, ni := renderIndentedCode(lines, i, width)
			rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
		default:
			if _, _, _, ok := mdListItem(line, 0); ok {
				r, s, ni := renderList(lines, i, 0, 0, width)
				rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
			} else {
				r, s, ni := renderParagraph(lines, i, width)
				rows, srcIdx, i = appendMD(rows, srcIdx, r, s, ni)
			}
		}
	}
	return MD{Rows: rows, Src: srcIdx}
}

func appendMD(rows []Row, src []int, r []Row, s []int, ni int) ([]Row, []int, int) {
	return append(rows, r...), append(src, s...), ni
}

// ---------------------------------------------------------------------------
// Block predicate helpers
// ---------------------------------------------------------------------------

func mdBlank(line string) bool { return strings.TrimSpace(line) == "" }

// mdIndent returns the leading-space cell count of a line. Only ASCII spaces
// count: tabs in prose are not indentation in this renderer.
func mdIndent(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// mdStripIndent strips up to n leading spaces.
func mdStripIndent(line string, n int) string {
	i := 0
	for i < len(line) && i < n && line[i] == ' ' {
		i++
	}
	return line[i:]
}

// mdBlockStart reports whether a single line begins a block that ends a
// paragraph. Tables need a second line, so callers check mdTableStart too.
func mdBlockStart(line string) bool {
	if mdBlank(line) || isFence(line) || isATX(line) || mdThematic(line) || isBlockquote(line) {
		return true
	}
	if ind := mdIndent(line); ind >= 4 {
		return true
	}
	_, _, _, ok := mdListItem(line, 0)
	return ok
}

func isFence(line string) bool {
	_, _, _, ok := mdFence(line)
	return ok
}

func isATX(line string) bool {
	_, _, ok := mdATX(line)
	return ok
}

// mdFence recognises a fenced-code opener: up to three spaces, then three or
// more backticks or tildes.
func mdFence(line string) (char byte, length int, info string, ok bool) {
	if mdIndent(line) > 3 {
		return 0, 0, "", false
	}
	t := strings.TrimLeft(line, " ")
	if t == "" || (t[0] != '`' && t[0] != '~') {
		return 0, 0, "", false
	}
	c := t[0]
	n := 0
	for n < len(t) && t[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, "", false
	}
	info = strings.TrimSpace(t[n:])
	if c == '`' && strings.ContainsRune(info, '`') {
		return 0, 0, "", false
	}
	return c, n, info, true
}

// mdATX recognises an ATX heading, returning its level and content.
func mdATX(line string) (level int, text string, ok bool) {
	t := strings.TrimLeft(line, " ")
	if t == "" || t[0] != '#' {
		return 0, "", false
	}
	n := 0
	for n < len(t) && t[n] == '#' {
		n++
	}
	if n > 6 {
		return 0, "", false
	}
	rest := t[n:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, "", false
	}
	rest = strings.TrimLeft(rest, " \t")
	rest = strings.TrimRight(rest, " \t")
	rest = strings.TrimRight(rest, "#")
	return n, strings.TrimRight(rest, " \t"), true
}

// mdThematic recognises a thematic break: three or more of the same marker
// optionally separated by spaces.
func mdThematic(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return false
	}
	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != c && t[i] != ' ' && t[i] != '\t' {
			return false
		}
	}
	return true
}

func isBlockquote(line string) bool {
	if mdIndent(line) > 3 {
		return false
	}
	return strings.HasPrefix(mdStripIndent(line, 3), ">")
}

func stripBlockquote(line string) string {
	t := mdStripIndent(line, 3)
	t = strings.TrimPrefix(t, ">")
	t = strings.TrimPrefix(t, " ")
	return t
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// Word wrapping over Segs
// ---------------------------------------------------------------------------

// mdWord is a whitespace-delimited word in a plain text, addressed by cell
// range. link marks a link URL tail; dropFrom records where the droppable URL
// begins after the link text is glued onto the preceding word.
type mdWord struct {
	from, to int
	link     bool
	dropFrom int
}

// wrapSegs word-wraps inline-parsed segments to width, returning one segment
// slice per screen line. Words break at spaces; a word wider than width is
// hard-broken; a link's URL is dropped when it cannot share its text's line.
func wrapSegs(segs []Seg, width int) [][]Seg {
	if width < 1 {
		width = 1
	}
	plain := segsPlain(segs)
	if strings.TrimSpace(plain) == "" {
		return nil
	}
	words := tokenizeWords(plain)
	markLinkWords(segs, words)
	words = mergeLinks(words)
	ranges := layoutWords(words, width)
	out := make([][]Seg, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, cutSegs(segs, r[0], r[1]))
	}
	return out
}

// wrapRanges word-wraps plain text to width, returning each line's cell range.
func wrapRanges(text string, width int) [][2]int {
	if width < 1 {
		width = 1
	}
	return layoutWords(tokenizeWords(text), width)
}

// wrapTextLines word-wraps plain text to width, returning the wrapped lines.
func wrapTextLines(text string, width int) []string {
	var out []string
	for _, r := range wrapRanges(text, width) {
		out = append(out, ansi.Cut(text, r[0], r[1]))
	}
	return out
}

func segsPlain(segs []Seg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func tokenizeWords(text string) []mdWord {
	var out []mdWord
	at, cell := 0, 0
	for at < len(text) {
		for at < len(text) && text[at] == ' ' {
			cell++
			at++
		}
		if at >= len(text) {
			break
		}
		from := cell
		for at < len(text) && text[at] != ' ' {
			r, size := utf8.DecodeRuneInString(text[at:])
			cell += lipgloss.Width(string(r))
			at += size
		}
		out = append(out, mdWord{from: from, to: cell})
	}
	return out
}

func markLinkWords(segs []Seg, words []mdWord) {
	pos := 0
	for _, s := range segs {
		w := lipgloss.Width(s.Text)
		if s.Link {
			for i := range words {
				if words[i].from >= pos && words[i].to <= pos+w {
					words[i].link = true
				}
			}
		}
		pos += w
	}
}

func mergeLinks(words []mdWord) []mdWord {
	out := make([]mdWord, 0, len(words))
	for _, w := range words {
		if w.link && len(out) > 0 {
			last := &out[len(out)-1]
			// dropFrom is the text's end: the droppable tail runs from there to
			// the url's end, including the space between them.
			last.dropFrom = last.to
			last.to = w.to
			continue
		}
		out = append(out, w)
	}
	return out
}

func layoutWords(words []mdWord, width int) [][2]int {
	var lines [][2]int
	start, end := -1, 0
	flush := func() {
		if start >= 0 && end > start {
			lines = append(lines, [2]int{start, end})
		}
		start, end = -1, 0
	}
	for _, w := range words {
		if start < 0 {
			start, end = w.from, w.from
		}
		if w.dropFrom > 0 {
			// Link word: text [from, dropFrom) then a droppable url tail.
			if w.dropFrom-start > width {
				if end > start {
					flush()
					start, end = w.from, w.from
				}
				if w.dropFrom-w.from > width {
					hardBreak(&lines, w.from, w.dropFrom, width)
					start, end = -1, 0
					continue
				}
			}
			end = w.dropFrom
			if w.to-start <= width {
				end = w.to
			}
			continue
		}
		if w.to-start > width {
			if end > start {
				flush()
				start, end = w.from, w.from
			}
			if w.to-w.from > width {
				hardBreak(&lines, w.from, w.to, width)
				start, end = -1, 0
				continue
			}
		}
		end = w.to
	}
	flush()
	return lines
}

func hardBreak(lines *[][2]int, from, to, width int) {
	for pos := from; pos < to; {
		e := min(pos+width, to)
		*lines = append(*lines, [2]int{pos, e})
		pos = e
	}
}

// cutSegs re-slices segments to the cell range [from, to) of their plain
// concatenation, so a wrapped line keeps exactly the styles that fall inside
// it.
func cutSegs(segs []Seg, from, to int) []Seg {
	if to <= from {
		return nil
	}
	var out []Seg
	pos := 0
	for _, s := range segs {
		w := lipgloss.Width(s.Text)
		if pos+w <= from {
			pos += w
			continue
		}
		if pos >= to {
			break
		}
		start := max(pos, from)
		end := min(pos+w, to)
		out = append(out, s.slice(start-pos, end-pos))
		pos += w
	}
	return out
}

// ---------------------------------------------------------------------------
// Block renderers
// ---------------------------------------------------------------------------

func renderHeading(text string, level, width, src int) ([]Row, []int) {
	fg := lipgloss.TerminalColor(ColorTealSoft)
	if level <= 2 {
		fg = lipgloss.TerminalColor(ColorCream)
	}
	segs := parseInline(text)
	for i := range segs {
		segs[i].FG = fg
		segs[i].Emph = nil
		segs[i].Strike = nil
		segs[i].Link = false
	}
	var rows []Row
	var srcs []int
	for _, lineSegs := range wrapSegs(segs, width) {
		rows = append(rows, Row{Segs: lineSegs})
		srcs = append(srcs, src)
	}
	return rows, srcs
}

// paraSegment is one logical paragraph line (hard-break separated) and the
// source line it starts at.
type paraSegment struct {
	text string
	src  int
}

// paraSegments joins source lines into logical lines, converting soft breaks
// to spaces and hard breaks (two trailing spaces or a trailing backslash) to
// newlines, while remembering each logical line's first source line.
func paraSegments(lines []string, base int) []paraSegment {
	var out []paraSegment
	cur := strings.Builder{}
	curSrc := base
	prevHard := false
	started := false
	flush := func() {
		out = append(out, paraSegment{text: cur.String(), src: curSrc})
		cur.Reset()
	}
	for i, l := range lines {
		hard := false
		if strings.HasSuffix(l, "  ") {
			hard = true
			l = strings.TrimRight(l, " ")
		} else if strings.HasSuffix(l, "\\") && !strings.HasSuffix(l, "\\\\") {
			hard = true
			l = strings.TrimSuffix(l, "\\")
		}
		if started && !prevHard {
			cur.WriteByte(' ')
		}
		cur.WriteString(l)
		started = true
		if hard {
			flush()
			curSrc = base + i + 1
			prevHard = true
		} else {
			prevHard = false
		}
	}
	if cur.Len() > 0 || !started {
		flush()
	}
	return out
}

func renderParagraph(lines []string, start, width int) ([]Row, []int, int) {
	var buf []string
	i := start
	for i < len(lines) && !mdBlank(lines[i]) && !mdBlockStart(lines[i]) && !mdTableStart(lines, i) {
		buf = append(buf, lines[i])
		i++
	}
	var rows []Row
	var src []int
	for _, seg := range paraSegments(buf, start) {
		for _, lineSegs := range wrapSegs(parseInline(seg.text), width) {
			rows = append(rows, Row{Segs: lineSegs})
			src = append(src, seg.src)
		}
	}
	return rows, src, i
}

func renderFence(lines []string, start, width int) ([]Row, []int, int) {
	c, n, info, _ := mdFence(lines[start])
	lang := firstWord(info)
	body, next := collectFenceBody(lines, start, c, n)
	code := expandTabsLines(strings.Join(body, "\n"), 4)

	indent := "  "
	icol := visibleLen(indent)

	if strings.EqualFold(lang, "mermaid") && mermaidRenderer != nil {
		var rows []Row
		var src []int
		for _, r := range mermaidRenderer(code, width) {
			r.Gutter += icol
			r.Segs = append([]Seg{NewSeg(indent, nil)}, r.Segs...)
			rows = append(rows, r)
			src = append(src, start+1)
		}
		return rows, src, next
	}

	var rows []Row
	var src []int
	codeLines := strings.Split(code, "\n")

	if strings.EqualFold(lang, "mermaid") {
		// Labelled, unhighlighted code block behind the renderer hook.
		rows = append(rows, Row{Gutter: icol, Segs: []Seg{NewSeg(indent, nil), NewSeg("mermaid", ColorMuted)}})
		src = append(src, start)
		for k, line := range codeLines {
			rows = append(rows, codeRow(indent, icol, line, nil))
			src = append(src, start+1+k)
		}
		return rows, src, next
	}

	highlighted := HighlightedLang(lang, code)
	for k, line := range codeLines {
		var segs []Seg
		if highlighted != nil && k < len(highlighted) {
			segs = highlighted[k]
		}
		rows = append(rows, codeRow(indent, icol, line, segs))
		src = append(src, start+1+k)
	}
	return rows, src, next
}

func collectFenceBody(lines []string, start int, c byte, n int) (body []string, next int) {
	i := start + 1
	for i < len(lines) {
		if closingFence(lines[i], c, n) {
			return body, i + 1
		}
		body = append(body, lines[i])
		i++
	}
	return body, i // unterminated fence runs to end of input
}

func closingFence(line string, c byte, n int) bool {
	t := strings.TrimLeft(line, " ")
	if len(t) < n {
		return false
	}
	for i := 0; i < n; i++ {
		if t[i] != c {
			return false
		}
	}
	return strings.Trim(t[n:], " \t") == ""
}

func codeRow(indent string, icol int, line string, segs []Seg) Row {
	r := Row{Gutter: icol, Segs: []Seg{NewSeg(indent, nil)}}
	if len(segs) > 0 {
		r.Segs = append(r.Segs, segs...)
	} else {
		r.Segs = append(r.Segs, NewSeg(line, nil))
	}
	return r
}

func renderIndentedCode(lines []string, start, width int) ([]Row, []int, int) {
	indent := "  "
	icol := visibleLen(indent)
	var rows []Row
	var src []int
	i := start
	for i < len(lines) && !mdBlank(lines[i]) && mdIndent(lines[i]) >= 4 {
		rows = append(rows, codeRow(indent, icol, mdStripIndent(lines[i], 4), nil))
		src = append(src, i)
		i++
	}
	return rows, src, i
}

func renderBlockquote(lines []string, start, width int) ([]Row, []int, int) {
	i := start
	var inner []string
	for i < len(lines) && isBlockquote(lines[i]) {
		inner = append(inner, stripBlockquote(lines[i]))
		i++
	}
	gutter := "▏ "
	icol := visibleLen(gutter)
	md := RenderMarkdown(strings.Join(inner, "\n"), max(width-icol, 1))
	var rows []Row
	var src []int
	for k, r := range md.Rows {
		r.Gutter += icol
		r.Segs = append([]Seg{NewSeg(gutter, ColorMuted)}, r.Segs...)
		rows = append(rows, r)
		src = append(src, start+md.Src[k])
	}
	return rows, src, i
}

// ---------------------------------------------------------------------------
// Lists
// ---------------------------------------------------------------------------

const (
	itemBullet = iota
	itemOrdered
	itemTask
)

func bulletFor(depth int) string {
	switch depth % 3 {
	case 0:
		return "•"
	case 1:
		return "◦"
	default:
		return "▪"
	}
}

// mdListItem recognises a list marker starting exactly at indent. It returns
// the kind, the text after the marker, and the task checkbox state.
func mdListItem(line string, indent int) (kind int, textAfter string, taskDone bool, ok bool) {
	if mdIndent(line) != indent {
		return 0, "", false, false
	}
	rest := line[indent:]
	if rest == "" {
		return 0, "", false, false
	}
	switch c := rest[0]; {
	case c == '-' || c == '*' || c == '+':
		after := rest[1:]
		if after != "" && after[0] != ' ' && after[0] != '\t' {
			return 0, "", false, false
		}
		t := strings.TrimLeft(after, " \t")
		if len(t) >= 3 && t[0] == '[' && (t[1] == ' ' || t[1] == 'x' || t[1] == 'X') && t[2] == ']' {
			return itemTask, strings.TrimLeft(t[3:], " \t"), t[1] == 'x' || t[1] == 'X', true
		}
		return itemBullet, strings.TrimLeft(after, " \t"), false, true
	case c >= '0' && c <= '9':
		ni := 0
		for ni < len(rest) && rest[ni] >= '0' && rest[ni] <= '9' {
			ni++
		}
		if ni < len(rest) && (rest[ni] == '.' || rest[ni] == ')') {
			after := rest[ni+1:]
			if after != "" && after[0] != ' ' && after[0] != '\t' {
				return 0, "", false, false
			}
			return itemOrdered, strings.TrimLeft(after, " \t"), false, true
		}
	}
	return 0, "", false, false
}

func renderList(lines []string, start, indent, depth, width int) ([]Row, []int, int) {
	var rows []Row
	var src []int
	i := start
	order := 0
	for i < len(lines) {
		kind, textAfter, taskDone, ok := mdListItem(lines[i], indent)
		if !ok {
			break
		}
		marker := ""
		switch kind {
		case itemOrdered:
			order++
			marker = strconv.Itoa(order) + ". "
		case itemTask:
			if taskDone {
				marker = "☑ "
			} else {
				marker = "☐ "
			}
		default:
			marker = bulletFor(depth) + " "
		}
		ci := visibleLen(marker)
		r, s, ni := renderListItem(lines, i, indent, ci, marker, textAfter, depth, width)
		rows = append(rows, r...)
		src = append(src, s...)
		i = ni
	}
	return rows, src, i
}

func renderListItem(lines []string, start, indent, ci int, marker, textAfter string, depth, width int) ([]Row, []int, int) {
	type block struct {
		para     []int
		listRows []Row
		listSrc  []int
	}
	var blocks []block
	cur := block{para: []int{start}}
	i := start + 1
	for i < len(lines) {
		line := lines[i]
		if mdBlank(line) {
			i++
			continue
		}
		ind := mdIndent(line)
		if ind <= indent {
			break
		}
		if _, _, _, ok := mdListItem(line, ind); ok {
			if len(cur.para) > 0 {
				blocks = append(blocks, cur)
				cur = block{}
			}
			lr, ls, ni := renderList(lines, i, ind, depth+1, width)
			blocks = append(blocks, block{listRows: lr, listSrc: ls})
			i = ni
			continue
		}
		cur.para = append(cur.para, i)
		i++
	}
	if len(cur.para) > 0 {
		blocks = append(blocks, cur)
	}

	var rows []Row
	var src []int
	for bi, b := range blocks {
		if len(b.para) > 0 {
			text := itemParaText(lines, b.para, ci, textAfter, bi == 0)
			r, s := renderItemPara(marker, text, indent, ci, width, start, bi == 0)
			rows = append(rows, r...)
			src = append(src, s...)
		} else {
			rows = append(rows, b.listRows...)
			src = append(src, b.listSrc...)
		}
	}
	return rows, src, i
}

// itemParaText joins an item's paragraph source lines, stripping the hanging
// indent from continuation lines and honouring hard breaks.
func itemParaText(lines []string, para []int, ci int, textAfter string, first bool) string {
	var parts []string
	if first && textAfter != "" {
		parts = append(parts, textAfter)
	}
	skip := 0
	if first {
		skip = 1
	}
	for _, li := range para[skip:] {
		parts = append(parts, mdStripIndent(lines[li], ci))
	}
	return softJoin(parts)
}

// softJoin joins lines into paragraph text: soft breaks become a space, hard
// breaks (two trailing spaces or a trailing backslash) become a newline.
func softJoin(lines []string) string {
	var b strings.Builder
	started := false
	prevHard := false
	for _, l := range lines {
		hard := false
		if strings.HasSuffix(l, "  ") {
			hard = true
			l = strings.TrimRight(l, " ")
		} else if strings.HasSuffix(l, "\\") && !strings.HasSuffix(l, "\\\\") {
			hard = true
			l = strings.TrimSuffix(l, "\\")
		}
		if started && !prevHard {
			b.WriteByte(' ')
		}
		b.WriteString(l)
		started = true
		if hard {
			b.WriteByte('\n')
		}
		prevHard = hard
	}
	return b.String()
}

// renderItemPara wraps one item's paragraph with a hanging indent and the
// marker on the first line only.
func renderItemPara(marker, text string, indent, ci, width, srcLine int, withMarker bool) ([]Row, []int) {
	inner := max(width-ci, 1)
	var rows []Row
	var src []int
	firstRow := withMarker
	for _, logical := range strings.Split(text, "\n") {
		for _, lineSegs := range wrapSegs(parseInline(logical), inner) {
			r := Row{Gutter: indent + ci}
			if firstRow {
				r.Segs = append(r.Segs, NewSeg(spaces(indent), nil), NewSeg(marker, ColorMuted))
				firstRow = false
			} else {
				r.Segs = append(r.Segs, NewSeg(spaces(indent+ci), nil))
			}
			r.Segs = append(r.Segs, lineSegs...)
			rows = append(rows, r)
			src = append(src, srcLine)
		}
	}
	if len(rows) == 0 && withMarker {
		rows = append(rows, Row{
			Gutter: indent + ci,
			Segs:   []Seg{NewSeg(spaces(indent), nil), NewSeg(marker, ColorMuted)},
		})
		src = append(src, srcLine)
	}
	return rows, src
}
