package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// GFM pipe tables render as a box-drawn grid in ColorLine. Alignment comes
// from the :---: delimiter row, column widths from the content. Over-wide
// tables shrink their widest columns and wrap cells; below the minimum they
// fall back to plain rows joined with " | ".

// minTableCol is the narrowest a boxed column may be shrunk before the table
// falls back to plain rows.
const minTableCol = 3

type tableRow struct {
	cells []string
	src   int
}

// mdTableStart recognises a table: a line containing a pipe followed by a
// delimiter row.
func mdTableStart(lines []string, i int) bool {
	if i+1 >= len(lines) {
		return false
	}
	if !strings.Contains(lines[i], "|") {
		return false
	}
	return mdTableDelimiter(lines[i+1])
}

func mdTableDelimiter(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" || !strings.Contains(t, "|") {
		return false
	}
	cells := splitTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		c := strings.TrimSpace(cell)
		if c == "" {
			return false
		}
		c = strings.Trim(c, ":")
		if len(c) < 1 || strings.Trim(c, "-") != "" {
			return false
		}
	}
	return true
}

// splitTableRow splits a table line into cells, honouring \| escapes.
func splitTableRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] == '\\' && i+1 < len(t) && t[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if t[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(t[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

func cellAt(cells []string, c int) string {
	if c < len(cells) {
		return cells[c]
	}
	return ""
}

func segsWidth(segs []Seg) int {
	w := 0
	for _, s := range segs {
		w += lipgloss.Width(s.Text)
	}
	return w
}

func cellPlainWidth(cell string) int {
	return segsWidth(parseInline(cell))
}

func renderTable(lines []string, start, width int) ([]Row, []int, int) {
	header := splitTableRow(lines[start])
	delims := splitTableRow(lines[start+1])
	ncol := len(header)
	if len(delims) > ncol {
		ncol = len(delims)
	}

	align := make([]int, ncol)
	for c := 0; c < ncol; c++ {
		align[c] = -1 // left
		if c >= len(delims) {
			continue
		}
		d := strings.TrimSpace(delims[c])
		left := strings.HasPrefix(d, ":")
		right := strings.HasSuffix(d, ":")
		switch {
		case left && right:
			align[c] = 0 // center
		case right:
			align[c] = 1 // right
		}
	}

	var data []tableRow
	i := start + 2
	for i < len(lines) && !mdBlank(lines[i]) && strings.Contains(lines[i], "|") {
		data = append(data, tableRow{cells: splitTableRow(lines[i]), src: i})
		i++
	}

	widths := make([]int, ncol)
	for c := 0; c < ncol; c++ {
		w := cellPlainWidth(cellAt(header, c))
		for _, r := range data {
			w = max(w, cellPlainWidth(cellAt(r.cells, c)))
		}
		widths[c] = max(w, 1)
	}

	total := ncol + 1 // one border cell per column plus the trailing edge
	for _, w := range widths {
		total += w
	}
	for total > width {
		idx := -1
		for c, w := range widths {
			if w > minTableCol && (idx == -1 || w > widths[idx]) {
				idx = c
			}
		}
		if idx == -1 {
			break
		}
		widths[idx]--
		total--
	}
	if total > width {
		r, s := renderTablePlain(header, data, start, width)
		return r, s, i
	}
	r, s := renderTableBoxed(header, data, align, widths, start)
	return r, s, i
}

func renderTablePlain(header []string, data []tableRow, headerSrc, width int) ([]Row, []int) {
	var rows []Row
	var src []int
	emit := func(cells []string, srcLine int) {
		for _, lineSegs := range wrapSegs(parseInline(strings.Join(cells, " | ")), width) {
			rows = append(rows, Row{Segs: lineSegs})
			src = append(src, srcLine)
		}
	}
	emit(header, headerSrc)
	for _, r := range data {
		emit(r.cells, r.src)
	}
	return rows, src
}

func renderTableBoxed(header []string, data []tableRow, align []int, widths []int, start int) ([]Row, []int) {
	headerCells := make([][][]Seg, len(widths))
	for c := range widths {
		headerCells[c] = renderTableCell(cellAt(header, c), widths[c], align[c], true)
	}
	dataCells := make([][][][]Seg, len(data))
	for ri, r := range data {
		dataCells[ri] = make([][][]Seg, len(widths))
		for c := range widths {
			dataCells[ri][c] = renderTableCell(cellAt(r.cells, c), widths[c], align[c], false)
		}
	}

	var rows []Row
	var src []int
	rows = append(rows, tableBorder(widths, "┌", "┬", "┐"))
	src = append(src, start)
	appendTableContent(&rows, &src, headerCells, widths, start)
	rows = append(rows, tableBorder(widths, "├", "┼", "┤"))
	src = append(src, start+1)
	lastSrc := start + 1
	for ri := range data {
		appendTableContent(&rows, &src, dataCells[ri], widths, data[ri].src)
		lastSrc = data[ri].src
	}
	rows = append(rows, tableBorder(widths, "└", "┴", "┘"))
	src = append(src, lastSrc)
	return rows, src
}

func renderTableCell(cell string, colWidth, align int, header bool) [][]Seg {
	segs := parseInline(cell)
	if header {
		for i := range segs {
			segs[i].FG = ColorCream
			segs[i].Emph = nil
			segs[i].Strike = nil
			segs[i].Link = false
		}
	}
	lines := wrapSegs(segs, colWidth)
	if len(lines) == 0 {
		lines = [][]Seg{{}}
	}
	for i := range lines {
		pad := colWidth - segsWidth(lines[i])
		switch align {
		case 1: // right
			lines[i] = append([]Seg{NewSeg(spaces(pad), nil)}, lines[i]...)
		case 0: // center
			l := pad / 2
			r := pad - l
			out := []Seg{NewSeg(spaces(l), nil)}
			out = append(out, lines[i]...)
			out = append(out, NewSeg(spaces(r), nil))
			lines[i] = out
		default: // left
			lines[i] = append(lines[i], NewSeg(spaces(pad), nil))
		}
	}
	return lines
}

func tableBorder(widths []int, left, mid, right string) Row {
	segs := []Seg{NewSeg(left, ColorLine)}
	for c, w := range widths {
		if c > 0 {
			segs = append(segs, NewSeg(mid, ColorLine))
		}
		segs = append(segs, NewSeg(repeatRune('─', w), ColorLine))
	}
	segs = append(segs, NewSeg(right, ColorLine))
	return Row{Segs: segs}
}

func appendTableContent(rows *[]Row, src *[]int, cells [][][]Seg, widths []int, srcLine int) {
	maxLines := 0
	for c := range cells {
		maxLines = max(maxLines, len(cells[c]))
	}
	for li := 0; li < maxLines; li++ {
		segs := []Seg{NewSeg("│", ColorLine)}
		for c, w := range widths {
			if c > 0 {
				segs = append(segs, NewSeg("│", ColorLine))
			}
			if li < len(cells[c]) {
				segs = append(segs, cells[c][li]...)
			} else {
				segs = append(segs, NewSeg(spaces(w), nil))
			}
		}
		segs = append(segs, NewSeg("│", ColorLine))
		*rows = append(*rows, Row{Segs: segs})
		*src = append(*src, srcLine)
	}
}
