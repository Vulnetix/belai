package components

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// A rendered transcript line has to satisfy two contracts at once: it must
// carry colour, and its LineMap entry must describe it in plain cells (see
// linemap.go:16-23 and :41-45). Satisfying both by hand is where this package
// has historically gone wrong — measuring a string that already contains
// escape bytes, or slicing one by byte offset.
//
// Seg and Row remove the opportunity. A Row is built from plain text, clipped
// and measured while it is still plain, and only then turned into escape
// sequences. Its SourceLine is derived from the plain twin it just measured,
// so the invariant holds by construction rather than by review.

// Span is a half-open range of cells within a Seg's text.
type Span struct{ From, To int }

// Seg is a run of text with at most one foreground colour. Text is guaranteed
// by NewSeg to be free of escape sequences, control characters, tabs and
// newlines, so lipgloss.Width measures it exactly and ansi.Cut slices it on
// cluster boundaries.
type Seg struct {
	Text string
	FG   lipgloss.TerminalColor // nil renders in the terminal's default colour

	// Emph marks cell ranges to show in reverse video. Reverse video is used
	// rather than an explicit colour because it composes with a background the
	// segment cannot see — see sgr.go.
	Emph []Span
}

// NewSeg builds a segment, sanitising text.
//
// The sanitising is load-bearing, not hygiene. Tool output and file contents
// are attacker-controlled bytes on their way to a terminal: a file containing
// an OSC 52 sequence would write the user's clipboard the moment the model
// read it, and a stray CSI could move the cursor or scroll the frame. Until
// this package rendered tool bodies through Row, the only thing preventing
// that was an ansi.Strip in renderToolContent that also happened to throw away
// every colour. Stripping here keeps the protection and allows the colour.
//
// Tabs and newlines become single spaces as a backstop; callers that care
// about alignment expand tabs themselves first, with expandTabs.
func NewSeg(text string, fg lipgloss.TerminalColor) Seg {
	return Seg{Text: sanitiseCells(text), FG: fg}
}

// sanitiseCells removes every byte that could steer the terminal: ESC and the
// rest of C0, DEL, and the C1 range that terminals in 8-bit mode accept as
// single-byte equivalents of CSI and OSC. Tab and newline become a space.
//
// Decoding is done a rune at a time rather than with range, because a raw C1
// byte is not valid UTF-8: range would hand it over as RuneError and the
// dangerous byte would pass through untouched. Invalid bytes are dropped
// outright, which also removes them.
func sanitiseCells(s string) string {
	if !needsSanitising(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			// Invalid byte, including a raw C1. Dropped.
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case isControl(r):
			// Dropped.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsSanitising(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f || c >= 0x80 {
			return true
		}
	}
	return false
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// expandTabs replaces tabs with spaces to the next multiple of width, counting
// from startCol. Tab stops are resolved here rather than left to the terminal
// because a row's text is preceded by a gutter of our own choosing: the
// terminal would measure its stops from the screen edge and land somewhere
// else, making every cell count in this package a lie.
func expandTabs(s string, startCol, width int) string {
	if !strings.ContainsRune(s, '\t') || width < 1 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	col := startCol
	for _, r := range s {
		if r == '\t' {
			n := width - ((col - startCol) % width)
			b.WriteString(spaces(n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += lipgloss.Width(string(r))
	}
	return b.String()
}

// Row is exactly one physical screen row. It renders to one line of text and
// one SourceLine — never more, which is what keeps LineMap's length invariant
// true no matter how the row is built.
type Row struct {
	Segs []Seg

	// BG washes the whole row, including the padding. It is emitted once at
	// the far left and closed once at the far right, so foreground spans
	// inside it compose rather than cancel it.
	BG lipgloss.TerminalColor

	// Gutter is how many leading cells are decoration — a diff sign, a line
	// number, an indent. They render but never appear in a copy.
	Gutter int

	// Pad fills the row out to the full width. Required when BG is set,
	// otherwise the wash stops at the end of the text.
	Pad bool

	// Marker delimits an on-screen truncation hint, as on SourceLine: a
	// selection touching it copies Hidden instead.
	MarkerCol, MarkerWidth int
	Hidden                 string

	// Chrome marks a row that is pure decoration and never selectable.
	Chrome bool
}

// Plain returns the row's text with no styling and no padding.
func (r Row) Plain() string {
	var b strings.Builder
	for _, s := range r.Segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Render renders the row clipped to width, returning the styled line and its
// provenance. Clipping happens while the text is still plain, so the styled
// line's cell width always equals the plain one's — the property linemap.go's
// Highlight depends on.
func (r Row) Render(width int) (string, SourceLine) {
	if width < 1 {
		return "", SourceLine{Chrome: true}
	}

	kept, plain := clipSegs(r.Segs, width)
	trimmed := strings.TrimRight(plain, " ")

	bg := bgSeq(r.BG)

	var b strings.Builder
	b.WriteString(bg)
	for _, s := range kept {
		b.WriteString(s.render())
	}
	if r.Pad {
		if n := width - lipgloss.Width(plain); n > 0 {
			b.WriteString(spaces(n))
		}
	}
	// Close only a background that was actually opened: on a profile that
	// cannot render colour, bgSeq yields "" and an unpaired bgOff would be the
	// one escape sequence in an otherwise plain line.
	if bg != "" {
		b.WriteString(bgOff)
	}

	if r.Chrome {
		return b.String(), SourceLine{Chrome: true}
	}

	gutter := min(r.Gutter, lipgloss.Width(trimmed))
	sl := SourceLine{
		Col:   gutter,
		Width: max(lipgloss.Width(trimmed)-gutter, 0),
		Text:  ansi.Cut(trimmed, gutter, lipgloss.Width(trimmed)),
	}
	if r.MarkerWidth > 0 {
		sl.MarkerCol, sl.MarkerWidth, sl.Hidden = r.MarkerCol, r.MarkerWidth, r.Hidden
	}
	return b.String(), sl
}

// render turns one segment into text plus escape sequences, closing its
// foreground with fgOff so an enclosing background survives.
func (s Seg) render() string {
	if s.Text == "" {
		return ""
	}
	body := s.Text
	if len(s.Emph) > 0 && lipgloss.ColorProfile() != termenv.Ascii {
		body = applyEmph(s.Text, s.Emph)
	}
	fg := fgSeq(s.FG)
	if fg == "" {
		return body
	}
	return fg + body + fgOff
}

// applyEmph wraps each span in reverse video. Spans are cell ranges, so the
// text is sliced with ansi.Cut rather than by rune index.
func applyEmph(text string, spans []Span) string {
	w := lipgloss.Width(text)
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		from, to := max(sp.From, at), min(sp.To, w)
		if to <= from {
			continue
		}
		b.WriteString(ansi.Cut(text, at, from))
		b.WriteString(emphOn)
		b.WriteString(ansi.Cut(text, from, to))
		b.WriteString(emphOff)
		at = to
	}
	b.WriteString(ansi.Cut(text, at, w))
	return b.String()
}

// clipSegs truncates a segment list to width cells, returning the kept
// segments and their concatenated plain text.
func clipSegs(segs []Seg, width int) ([]Seg, string) {
	kept := make([]Seg, 0, len(segs))
	var plain strings.Builder
	used := 0
	for _, s := range segs {
		if used >= width {
			break
		}
		w := lipgloss.Width(s.Text)
		if used+w > width {
			s = s.clip(width - used)
			w = lipgloss.Width(s.Text)
		}
		if s.Text == "" {
			continue
		}
		kept = append(kept, s)
		plain.WriteString(s.Text)
		used += w
	}
	return kept, plain.String()
}

// clip cuts a segment to n cells, dropping and truncating emphasis spans to
// match.
func (s Seg) clip(n int) Seg {
	if n <= 0 {
		return Seg{FG: s.FG}
	}
	out := Seg{Text: ansi.Cut(s.Text, 0, n), FG: s.FG}
	for _, sp := range s.Emph {
		if sp.From >= n {
			break
		}
		out.Emph = append(out.Emph, Span{From: sp.From, To: min(sp.To, n)})
	}
	return out
}

// renderRows renders a block of rows. It is the only way rows reach the
// transcript, and it guarantees len(lm) == len(rows) == the rendered line
// count, which is the invariant LineMap documents at linemap.go:41-45 and
// Highlight refuses to run without.
func renderRows(rows []Row, width int) (string, LineMap) {
	var b strings.Builder
	lm := make(LineMap, 0, len(rows))
	for i, r := range rows {
		if i > 0 {
			b.WriteString("\n")
		}
		line, sl := r.Render(width)
		b.WriteString(line)
		lm = append(lm, sl)
	}
	return b.String(), lm
}
