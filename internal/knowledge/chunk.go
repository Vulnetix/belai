package knowledge

import (
	"strings"

	"github.com/vulnetix/belai/internal/offload"
)

// Chunking bounds, in estimated tokens (offload.Tokens: four characters each).
const (
	// TargetTokens is the size a chunk grows to before it is closed.
	TargetTokens = 200
	// OverlapTokens is how much of the end of a closed chunk opens the next.
	OverlapTokens = 24
	// maxLineRunes splits a line longer than this, so a one-line JSON file or
	// a minified bundle still chunks.
	maxLineRunes = 400
)

// RawChunk is a span of lines before it is admitted: Start and End are 1-based
// and inclusive.
type RawChunk struct {
	Start, End int
	Text       string
}

// SplitText cuts text into chunks of about TargetTokens on line boundaries,
// preferring a blank line once a chunk is half full, with a small overlap so a
// passage on a boundary is whole in one chunk. A line over maxLineRunes is
// split; its pieces share its line number. Blank-only chunks are not returned.
func SplitText(text string) []RawChunk {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	type line struct {
		n    int
		text string
		tok  int
	}
	var lines []line
	for i, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		for _, piece := range splitLongLine(l) {
			lines = append(lines, line{n: i + 1, text: piece, tok: offload.Tokens(piece) + 1})
		}
	}
	var out []RawChunk
	emit := func(from, to int) {
		var b strings.Builder
		for i := from; i < to; i++ {
			if i > from {
				b.WriteByte('\n')
			}
			b.WriteString(lines[i].text)
		}
		body := strings.TrimSpace(b.String())
		if body == "" {
			return
		}
		out = append(out, RawChunk{Start: lines[from].n, End: lines[to-1].n, Text: body})
	}
	start, tok := 0, 0
	for i := 0; i < len(lines); i++ {
		tok += lines[i].tok
		blank := strings.TrimSpace(lines[i].text) == ""
		if tok >= TargetTokens || (blank && tok >= TargetTokens/2) {
			emit(start, i+1)
			// The next chunk reopens on the tail of this one.
			next, back := i+1, 0
			for next > start+1 && back+lines[next-1].tok <= OverlapTokens {
				next--
				back += lines[next].tok
			}
			start, tok = next, back
		}
	}
	if start < len(lines) {
		// A tail that is only the overlap of the chunk before it adds nothing.
		if len(out) == 0 || lines[len(lines)-1].n > out[len(out)-1].End {
			emit(start, len(lines))
		}
	}
	return out
}

func splitLongLine(l string) []string {
	r := []rune(l)
	if len(r) <= maxLineRunes {
		return []string{l}
	}
	var out []string
	for len(r) > maxLineRunes {
		out = append(out, string(r[:maxLineRunes]))
		r = r[maxLineRunes:]
	}
	return append(out, string(r))
}
