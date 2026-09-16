package components

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	// readGutterMinInner is the narrowest body that still gets line numbers.
	// Below it the gutter costs more than it tells you.
	readGutterMinInner = 30
	// readGutterMaxDigits caps the gutter so a generated file with a million
	// lines cannot eat the body.
	readGutterMaxDigits = 5
	// readTabWidth is where a tab lands inside the code column. Tabs are
	// expanded by us rather than left to the terminal, whose stops are
	// measured from the screen edge and would be thrown off by the gutter.
	readTabWidth = 4
)

// readArgs pulls the path and byte offset out of a Read call's arguments.
func readArgs(argsJSON string) (path string, offset int) {
	if strings.TrimSpace(argsJSON) == "" {
		return "", 0
	}
	var args map[string]any
	if json.Unmarshal([]byte(argsJSON), &args) != nil {
		return "", 0
	}
	for _, k := range []string{"path", "file"} {
		if s, ok := args[k].(string); ok && s != "" {
			path = s
			break
		}
	}
	if f, ok := args["offset"].(float64); ok {
		offset = int(f)
	}
	return path, offset
}

// readToolRow renders a Read result as numbered source.
//
// Line numbers are added here rather than by the Read tool itself, for two
// reasons. The tool's offset and limit are byte quantities, so a model that
// saw a line number and passed it back as an offset would silently read the
// wrong region — worse than having no numbers. And a gutter in the tool's
// output would be content: it would cost tokens on every replay of the
// transcript, and it would end up in every copy.
//
// Numbering is correct when the read started at the beginning of the file
// (offset == 0). For partial reads the tool emits Meta["start_line"], which
// this function uses so the visible lines are still numbered correctly.
// Without that metadata a partial read is left unnumbered rather than numbered
// wrongly.
//
// Syntax highlighting is applied only when the row is expanded. Collapsed, a
// Read row is three lines, and colour there would compete with the diff and
// status colours that actually carry meaning.
func readToolRow(msg Message, width int, expand bool) (string, LineMap) {
	path, offset := readArgs(msg.ToolArgs)
	if path == "" && msg.Meta != nil {
		if p, ok := msg.Meta["path"].(string); ok {
			path = p
		}
	}

	// Tabs are expanded before anything measures, highlights or slices the
	// text. Seg text is tab-free by construction, so expanding later would be
	// too late; and leaving tabs for the terminal would put the indentation
	// wherever its own stops fall, which the gutter has already shifted.
	content := expandTabsLines(strings.TrimRight(msg.Text(), "\n"), readTabWidth)

	lines := strings.Split(content, "\n")
	shown := lines
	var trunc truncation
	if !expand {
		if n := previewLines("Read"); len(lines) > n {
			shown = lines[:n]
			trunc = truncation{
				hidden: strings.Join(lines[n:], "\n"),
				label:  "… " + strconv.Itoa(len(lines)-n) + " more lines",
			}
		}
	}

	indent := "  "
	icol := visibleLen(indent)
	inner := max(width-icol, 8)

	startLine := 1
	hasStartLine := false
	if msg.Meta != nil {
		if sl, ok := msg.Meta["start_line"].(int); ok && sl > 0 {
			startLine = sl
			hasStartLine = true
		} else if sl, ok := msg.Meta["start_line"].(float64); ok && sl > 0 {
			startLine = int(sl)
			hasStartLine = true
		}
	}

	// The gutter is sized for the whole file, not the visible slice, so the
	// body does not shift sideways when the row is expanded.
	gutter := 0
	if (offset == 0 || hasStartLine) && inner >= readGutterMinInner {
		maxLine := startLine + len(lines) - 1
		gutter = min(len(strconv.Itoa(maxLine)), readGutterMaxDigits) + 1
	}

	var segs [][]Seg
	if expand {
		segs = Highlighted(path, content)
	}

	rows := make([]Row, 0, len(shown)+1)
	for i, line := range shown {
		r := Row{Gutter: icol + gutter, Segs: []Seg{NewSeg(indent, nil)}}
		if gutter > 0 {
			num := strconv.Itoa(startLine + i)
			if len(num) > gutter-1 {
				num = strings.Repeat("›", gutter-1)
			}
			r.Segs = append(r.Segs, NewSeg(spaces(gutter-1-len(num))+num+" ", ColorMuted))
		}
		r.Segs = append(r.Segs, codeSegs(line, segs, i, inner-gutter)...)
		rows = append(rows, r)
	}
	return renderRows(appendHint(rows, trunc, icol+gutter, width), width)
}

// codeSegs renders one source line, using its highlighted segments when they
// are available and falling back to plain text when they are not.
func codeSegs(line string, highlighted [][]Seg, idx, avail int) []Seg {
	if avail < 1 {
		return nil
	}
	if highlighted != nil && idx < len(highlighted) {
		return highlighted[idx]
	}
	return []Seg{NewSeg(line, nil)}
}

// expandTabsLines expands tabs in every line, measuring each line's stops from
// its own start — which is the start of the code column, not the screen edge.
func expandTabsLines(s string, width int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = expandTabs(line, 0, width)
	}
	return strings.Join(lines, "\n")
}

// appendHint places a truncation hint on the last row when it fits there, and
// on a row of its own when it does not. Placing it after the rows are built,
// rather than folding it into the text beforehand, is what keeps its cell
// range exactly known: a hint that went through wrapping could be split across
// two lines, leaving the marker unlocatable and the remainder uncopyable.
//
// width is the full row budget, gutter the leading decoration those rows carry.
func appendHint(rows []Row, trunc truncation, gutter, width int) []Row {
	if trunc.hidden == "" || len(rows) == 0 {
		return rows
	}
	hint := trunc.label
	last := &rows[len(rows)-1]
	used := lipgloss.Width(last.Plain())

	if used > 0 && used+2+visibleLen(hint) <= width {
		// Fits beside the last line.
		last.Segs = append(last.Segs, NewSeg("  ", nil), NewSeg(hint, ColorMuted))
		last.MarkerCol = used + 2
		last.MarkerWidth = visibleLen(hint)
		last.Hidden = trunc.hidden
		return rows
	}
	return append(rows, Row{
		Gutter:      gutter,
		Segs:        []Seg{NewSeg(spaces(gutter), nil), NewSeg(hint, ColorMuted)},
		MarkerCol:   gutter,
		MarkerWidth: visibleLen(hint),
		Hidden:      trunc.hidden,
	})
}
