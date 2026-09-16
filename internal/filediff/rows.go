package filediff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/charmbracelet/x/ansi"
)

const (
	// ContextLines is how many unchanged lines are shown either side of a
	// change. Four is enough to recognise where you are in a function without
	// the context outweighing the change.
	ContextLines = 4

	// TabWidth is where a tab lands. Tabs are expanded here, before anything
	// measures the text, because the terminal resolves its own stops from the
	// screen edge and the diff gutter has already shifted the code.
	TabWidth = 4

	// maxDiffLines bails out on files large enough that diffing them would be
	// felt. A file this size is generated, and its diff would be unreadable
	// anyway.
	maxDiffLines = 20000

	// maxWordDiffTokens bounds the quadratic intra-line diff. A line with more
	// tokens than this is minified or generated.
	maxWordDiffTokens = 256
)

// Rows renders one file change as a flat sequence of rows.
func (fc FileChange) Rows() []Row {
	if fc.Binary || fc.Truncated {
		return nil
	}
	if countLines(fc.Old) > maxDiffLines || countLines(fc.New) > maxDiffLines {
		return nil
	}

	old, new := expandTabsLines(fc.Old), expandTabsLines(fc.New)
	if old == new {
		return nil
	}

	edits := udiff.Strings(old, new)
	u, err := udiff.ToUnifiedDiff(fc.Path, fc.Path, old, edits, ContextLines)
	if err != nil {
		return nil
	}

	var rows []Row
	// delta tracks how far the new file's numbering has drifted from the old
	// file's. go-udiff gives each hunk its old-file start line only, so the
	// new-file numbering has to be carried across hunks by hand.
	delta := 0
	prevOldEnd := 0

	for _, h := range u.Hunks {
		oldNo := h.FromLine
		newNo := h.FromLine + delta

		// A gap between hunks is unchanged content that was skipped.
		if prevOldEnd > 0 && oldNo > prevOldEnd {
			rows = append(rows, Row{Op: OpElide})
		}

		for _, line := range h.Lines {
			text := strings.TrimSuffix(line.Content, "\n")
			switch line.Kind {
			case udiff.Equal:
				rows = append(rows, Row{Op: OpContext, OldLine: oldNo, NewLine: newNo, Text: text})
				oldNo++
				newNo++
			case udiff.Delete:
				rows = append(rows, Row{Op: OpDel, OldLine: oldNo, Text: text})
				oldNo++
				delta--
			case udiff.Insert:
				rows = append(rows, Row{Op: OpAdd, NewLine: newNo, Text: text})
				newNo++
				delta++
			}
		}
		prevOldEnd = oldNo
	}

	markWordChanges(rows)
	return rows
}

// markWordChanges annotates single-line modifications with the parts that
// actually changed.
//
// The gate is narrow on purpose: emphasis is only applied to a change batch of
// exactly one removed line followed by exactly one added line. That is the
// case where "this line became that line" is unambiguous and the highlight
// tells you something. For a batch of several lines there is no reliable
// pairing — a word-level diff across them produces scattered, confident
// nonsense — so those lines are left plain and the +/- markers do the work.
func markWordChanges(rows []Row) {
	for i := 0; i < len(rows); {
		if !changed(rows[i].Op) {
			i++
			continue
		}
		// A batch is the whole contiguous run of changed rows. Taking anything
		// smaller would find a 1:1 pair inside a larger interleaved run —
		// where the differ has grouped several removals and additions together
		// and no such pairing exists.
		j := i
		for j < len(rows) && changed(rows[j].Op) {
			j++
		}
		if j-i == 2 && rows[i].Op == OpDel && rows[i+1].Op == OpAdd {
			rows[i].Emph, rows[i+1].Emph = wordSpans(rows[i].Text, rows[i+1].Text)
		}
		i = j
	}
}

func changed(op Op) bool { return op == OpDel || op == OpAdd }

// wordSpans returns the cell ranges that differ between two lines.
func wordSpans(oldText, newText string) (oldSpans, newSpans []Span) {
	a, b := tokenise(oldText), tokenise(newText)
	if len(a) > maxWordDiffTokens || len(b) > maxWordDiffTokens {
		return nil, nil
	}

	keepA, keepB := lcsMask(a, b)
	return spansFrom(a, keepA), spansFrom(b, keepB)
}

// tokenise splits a line into alternating runs of word characters and
// everything else, which is a good enough unit for "what changed here".
func tokenise(s string) []string {
	var out []string
	var cur strings.Builder
	var curWord bool
	for _, r := range s {
		w := isWordRune(r)
		if cur.Len() > 0 && w != curWord {
			out = append(out, cur.String())
			cur.Reset()
		}
		curWord = w
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9') || r > 0x7f
}

// lcsMask marks which tokens of each side are part of the longest common
// subsequence, i.e. which ones did not change.
func lcsMask(a, b []string) (keepA, keepB []bool) {
	keepA, keepB = make([]bool, len(a)), make([]bool, len(b))

	// Standard LCS table. Both inputs are one line, and bounded by
	// maxWordDiffTokens above.
	table := make([][]int, len(a)+1)
	for i := range table {
		table[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			keepA[i], keepB[j] = true, true
			i++
			j++
		case table[i+1][j] >= table[i][j+1]:
			i++
		default:
			j++
		}
	}
	return keepA, keepB
}

// spansFrom turns a per-token keep mask into cell ranges over the joined text,
// merging adjacent changed tokens into one span.
//
// Leading whitespace is deliberately excluded: a span that starts at the left
// edge would highlight the indentation, which is never what changed in any
// interesting sense and reads as a flashing block down the side of the diff.
func spansFrom(tokens []string, keep []bool) []Span {
	var spans []Span
	col := 0
	for i, tok := range tokens {
		w := ansi.StringWidth(tok)
		if !keep[i] {
			from := col
			if i == 0 {
				if trimmed := strings.TrimLeft(tok, " \t"); trimmed != tok {
					from += w - ansi.StringWidth(trimmed)
				}
			}
			if to := col + w; to > from {
				if n := len(spans); n > 0 && spans[n-1].To == from {
					spans[n-1].To = to
				} else {
					spans = append(spans, Span{From: from, To: to})
				}
			}
		}
		col += w
	}
	return spans
}

// Stat counts the added and removed lines across a file's rows.
func Stat(rows []Row) (added, removed int) {
	for _, r := range rows {
		switch r.Op {
		case OpAdd:
			added++
		case OpDel:
			removed++
		}
	}
	return added, removed
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// expandTabsLines expands tabs in every line, measuring each line's stops from
// its own start.
func expandTabsLines(s string) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = expandTabs(line)
	}
	return strings.Join(lines, "\n")
}

func expandTabs(line string) string {
	if !strings.ContainsRune(line, '\t') {
		return line
	}
	var b strings.Builder
	col := 0
	for _, r := range line {
		if r == '\t' {
			n := TabWidth - (col % TabWidth)
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += ansi.StringWidth(string(r))
	}
	return b.String()
}
