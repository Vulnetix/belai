package components

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vulnetix/signet/internal/filediff"
)

// A diff row is laid out as
//
//	<indent><sign><space><right-aligned number><space><code>
//
// with no vertical rule between the gutter and the code. A rule would cost a
// cell in the most contended place — at the narrow end the code column is
// already down to about twenty cells — and the row wash already draws the same
// boundary as a contiguous vertical band.
//
// Added lines carry new-file numbering, removed lines old-file numbering, and
// context lines old-file numbering. The last is Pi's rule and worth keeping:
// the context column then reads as "the file you had", which is what you scan
// against when reviewing a removal.

const (
	// Width ladder, measured on the body width inside the row's indent.
	diffFullGutterMin  = 60 // sign, space, number, space
	diffTightGutterMin = 40 // sign, number, space
	diffNoNumbersMin   = 26 // sign, space
	diffMinUsable      = 20 // below this a diff is not a diff
	diffWashMin        = 26 // a wash narrower than this is noise, not signal

	diffMaxDigits = 5

	// diffPreviewRows is how many rows a collapsed diff shows.
	diffPreviewRows = 6
)

// diffToolRow renders what a command changed, beneath its tool row.
func diffToolRow(msg Message, width int, expand bool) (string, LineMap) {
	ch := msg.Diff()
	indent := "  "
	icol := visibleLen(indent)
	inner := max(width-icol, 8)

	if ch.Unavailable != "" {
		if !expand {
			// Not a failure the user caused, and outside a repository it is the
			// common case — so it stays out of the collapsed view entirely.
			return "", nil
		}
		return renderRows([]Row{{
			Gutter: icol,
			Segs:   []Seg{NewSeg(indent, nil), NewSeg("~ "+ch.Unavailable, ColorMuted)},
		}}, width)
	}

	var rows []Row
	for _, fc := range ch.Files {
		fileRows := fc.Rows()
		added, removed := filediff.Stat(fileRows)

		// A header per file: the path, then its net effect.
		head := Row{Gutter: icol, Segs: []Seg{
			NewSeg(indent, nil),
			NewSeg(fc.Path, ColorCream),
		}}
		switch {
		case fc.Binary:
			head.Segs = append(head.Segs, NewSeg("  binary file changed", ColorMuted))
		case fc.Truncated:
			head.Segs = append(head.Segs, NewSeg("  too large to diff", ColorMuted))
		case fc.Deleted:
			head.Segs = append(head.Segs, NewSeg("  deleted", ColorDanger))
		default:
			head.Segs = append(head.Segs,
				NewSeg("  +"+strconv.Itoa(added), ColorTealSoft),
				NewSeg(" −"+strconv.Itoa(removed), ColorDanger))
		}
		rows = append(rows, head)

		if inner < diffMinUsable {
			continue
		}
		rows = append(rows, fileDiffRows(fc, fileRows, indent, icol, inner, expand)...)
	}

	if len(rows) == 0 {
		return "", nil
	}
	return renderRows(rows, width)
}

// fileDiffRows renders one file's rows, collapsed to the first change or in
// full.
func fileDiffRows(fc filediff.FileChange, rows []filediff.Row, indent string, icol, inner int, expand bool) []Row {
	if len(rows) == 0 {
		return nil
	}

	gutter := diffGutterWidth(rows, inner)
	shown := rows
	var trunc truncation

	if !expand && len(rows) > diffPreviewRows {
		// Anchor on the change. Six rows of leading context is six wasted rows.
		start := 0
		for start < len(rows) && rows[start].Op == filediff.OpContext {
			start++
		}
		start = max(min(start, len(rows)-diffPreviewRows), 0)
		shown = rows[start : start+diffPreviewRows]
		hidden := len(rows) - diffPreviewRows
		trunc = truncation{
			hidden: plainDiffText(rows, gutter),
			label:  "… " + strconv.Itoa(hidden) + " more diff lines",
		}
	}

	oldSegs, newSegs := diffHighlights(fc, expand)

	out := make([]Row, 0, len(shown)+1)
	for _, r := range shown {
		out = append(out, diffRow(r, oldSegs, newSegs, indent, icol, gutter, inner, expand))
	}
	return appendHint(out, trunc, icol+gutter, icol+inner)
}

// diffHighlights lexes each side of the file once, so a line is coloured in
// the context it actually had rather than as a standalone fragment.
func diffHighlights(fc filediff.FileChange, expand bool) (oldSegs, newSegs [][]Seg) {
	if !expand || fc.Binary || fc.Truncated {
		return nil, nil
	}
	return Highlighted(fc.Path, fc.Old), Highlighted(fc.Path, fc.New)
}

// diffRow renders one line of a diff.
func diffRow(r filediff.Row, oldSegs, newSegs [][]Seg, indent string, icol, gutter, inner int, expand bool) Row {
	row := Row{Gutter: icol + gutter, Segs: []Seg{NewSeg(indent, nil)}}

	if r.Op == filediff.OpElide {
		row.Segs = append(row.Segs, NewSeg(spaces(gutter)+"…", ColorMuted))
		return row
	}

	sign, fg, bg, lineNo, segs := diffRowStyle(r, oldSegs, newSegs)

	// Collapsed rows are foreground-only: at three or six lines the diff
	// colours are the whole signal, and a wash would be decoration. Expanded
	// rows get the wash, and syntax colours ride on top of it.
	if !expand || inner < diffWashMin || lipgloss.ColorProfile() == termenv.Ascii ||
		lipgloss.ColorProfile() == termenv.ANSI {
		bg = nil
	}
	row.BG = bg
	row.Pad = bg != nil

	if gutter > 0 {
		row.Segs = append(row.Segs, NewSeg(diffGutter(sign, lineNo, gutter), fg))
	}

	// Without a wash the whole line takes the diff colour, which is what makes
	// a collapsed row readable at a glance. With one, the row is already
	// marked, so syntax colour is free to carry the structure.
	if row.BG == nil || segs == nil {
		body := NewSeg(r.Text, fg)
		body.Emph = toSpans(r.Emph)
		row.Segs = append(row.Segs, body)
		return row
	}
	row.Segs = append(row.Segs, emphasised(segs, r.Emph)...)
	return row
}

// diffRowStyle resolves one row's sign, colours, line number and highlighted
// segments.
func diffRowStyle(r filediff.Row, oldSegs, newSegs [][]Seg) (
	sign string, fg, bg lipgloss.TerminalColor, lineNo int, segs []Seg,
) {
	switch r.Op {
	case filediff.OpAdd:
		return "+", ColorTealSoft, ColorDiffAddBg, r.NewLine, segAt(newSegs, r.NewLine)
	case filediff.OpDel:
		return "−", ColorDanger, ColorDiffDelBg, r.OldLine, segAt(oldSegs, r.OldLine)
	default:
		return " ", ColorMuted, nil, r.OldLine, segAt(oldSegs, r.OldLine)
	}
}

// segAt returns the highlighted segments for a 1-based file line.
func segAt(segs [][]Seg, line int) []Seg {
	if segs == nil || line < 1 || line > len(segs) {
		return nil
	}
	return segs[line-1]
}

// emphasised overlays the intra-line change spans onto syntax-coloured
// segments, splitting a segment where a span crosses it.
func emphasised(segs []Seg, spans []filediff.Span) []Seg {
	if len(spans) == 0 {
		return segs
	}
	out := make([]Seg, 0, len(segs))
	col := 0
	for _, s := range segs {
		w := lipgloss.Width(s.Text)
		var local []Span
		for _, sp := range spans {
			from, to := max(sp.From-col, 0), min(sp.To-col, w)
			if to > from {
				local = append(local, Span{From: from, To: to})
			}
		}
		out = append(out, Seg{Text: s.Text, FG: s.FG, Emph: local})
		col += w
	}
	return out
}

func toSpans(in []filediff.Span) []Span {
	if len(in) == 0 {
		return nil
	}
	out := make([]Span, len(in))
	for i, s := range in {
		out[i] = Span{From: s.From, To: s.To}
	}
	return out
}

// diffGutter renders the sign and line number to exactly gutter cells.
func diffGutter(sign string, lineNo, gutter int) string {
	if gutter <= 2 {
		return sign + spaces(gutter-1)
	}
	digits := gutter - 2 // the sign and the trailing space
	num := ""
	if lineNo > 0 {
		num = strconv.Itoa(lineNo)
		if len(num) > digits {
			num = num[len(num)-digits:]
		}
	}
	return sign + spaces(digits-len(num)) + num + " "
}

// diffGutterWidth picks a gutter for the available body width.
func diffGutterWidth(rows []filediff.Row, inner int) int {
	maxNo := 0
	for _, r := range rows {
		maxNo = max(maxNo, max(r.OldLine, r.NewLine))
	}
	digits := min(len(strconv.Itoa(max(maxNo, 1))), diffMaxDigits)

	switch {
	case inner >= diffFullGutterMin:
		return digits + 2 // sign + digits + trailing space, plus one for air
	case inner >= diffTightGutterMin:
		return digits + 2
	case inner >= diffNoNumbersMin:
		return 2 // sign and a space
	default:
		return 2
	}
}

// plainDiffText renders rows as plain unified text, gutters included. This is
// what a selection over a truncation hint copies: a diff is a document, and
// half of one without its signs and line numbers is not useful.
func plainDiffText(rows []filediff.Row, gutter int) string {
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		if r.Op == filediff.OpElide {
			b.WriteString(spaces(gutter) + "…")
			continue
		}
		sign, _, _, lineNo, _ := diffRowStyle(r, nil, nil)
		b.WriteString(diffGutter(sign, lineNo, gutter) + r.Text)
	}
	return b.String()
}
