package components

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The three-class model is the whole contract: a motion crosses exactly one
// run of whitespace-skipping plus one run of word or non-word runes. Each case
// is written as the cursor position before and after, so the table reads as
// the behaviour a user sees.
func TestWordRight(t *testing.T) {
	cases := []struct {
		name string
		line string
		col  int
		want int
	}{
		{"start of a word lands on its end", "hello world", 0, 5},
		{"mid-word lands on the end of that word", "hello world", 2, 5},
		{"end of a word skips the gap and crosses the next", "hello world", 5, 11},
		{"leading whitespace is skipped first", "   hi", 0, 5},
		{"punctuation is its own run", "foo.bar", 3, 4},
		{"a word stops at internal punctuation", "foo.bar", 0, 3},
		{"a punctuation run is one hop", "foo();", 3, 6},
		{"underscores are part of the word", "foo_bar baz", 0, 7},
		{"digits are word runes", "abc123 x", 0, 6},
		{"a decimal stops at the point", "3.14", 0, 1},
		{"at the end stays at the end", "hello", 5, 5},
		{"an empty line stays put", "", 0, 0},
		{"whitespace-only runs to the end", "    ", 0, 4},
		{"a column past the end clamps", "hi", 99, 2},
		{"a negative column is treated as zero", "hi there", -5, 2},
		{"tabs count as whitespace", "a\t\tb", 1, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wordRight([]rune(tc.line), tc.col); got != tc.want {
				t.Fatalf("wordRight(%q, %d) = %d, want %d", tc.line, tc.col, got, tc.want)
			}
		})
	}
}

func TestWordLeft(t *testing.T) {
	cases := []struct {
		name string
		line string
		col  int
		want int
	}{
		{"end of a word lands on its start", "hello world", 11, 6},
		{"mid-word lands on the start of that word", "hello world", 8, 6},
		{"start of a word skips the gap and crosses the previous", "hello world", 6, 0},
		{"trailing whitespace is skipped first", "hi   ", 5, 0},
		{"punctuation is its own run", "foo.bar", 4, 3},
		{"a word stops at internal punctuation", "foo.bar", 7, 4},
		{"a punctuation run is one hop", "foo();", 6, 3},
		{"underscores are part of the word", "foo_bar", 7, 0},
		{"at the start stays at the start", "hello", 0, 0},
		{"an empty line stays put", "", 0, 0},
		{"whitespace-only runs to the start", "    ", 4, 0},
		{"a column past the end clamps", "hi", 99, 0},
		{"tabs count as whitespace", "a\t\tb", 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := wordLeft([]rune(tc.line), tc.col); got != tc.want {
				t.Fatalf("wordLeft(%q, %d) = %d, want %d", tc.line, tc.col, got, tc.want)
			}
		})
	}
}

// Walking a line forward and then back must visit the same boundaries, and
// must always terminate. A motion that can return its own input is an infinite
// loop under a held key.
func TestWordMotionAlwaysAdvances(t *testing.T) {
	lines := []string{
		"hello world",
		"foo.bar baz_qux (a, b)",
		"   leading and trailing   ",
		"...",
		"a",
		"",
	}
	for _, line := range lines {
		r := []rune(line)

		var forward []int
		for col := 0; col < len(r); {
			next := wordRight(r, col)
			if next <= col {
				t.Fatalf("wordRight(%q, %d) = %d did not advance", line, col, next)
			}
			forward = append(forward, next)
			col = next
		}

		var backward []int
		for col := len(r); col > 0; {
			prev := wordLeft(r, col)
			if prev >= col {
				t.Fatalf("wordLeft(%q, %d) = %d did not retreat", line, col, prev)
			}
			backward = append(backward, prev)
			col = prev
		}

		// The two walks visit the same number of boundaries: forward ends on
		// the last stop before the end, backward on 0.
		if len(forward) != len(backward) {
			t.Fatalf("%q: %d forward stops but %d backward stops (%v vs %v)", line, len(forward), len(backward), forward, backward)
		}
	}
}

// Rune indices, not bytes: a motion over multi-byte text must not split a
// rune or overshoot.
func TestWordMotionIsRuneIndexed(t *testing.T) {
	line := []rune("héllo wörld")
	if got := wordRight(line, 0); got != 5 {
		t.Fatalf("wordRight = %d, want 5 (rune index of the space)", got)
	}
	if got := wordLeft(line, len(line)); got != 6 {
		t.Fatalf("wordLeft = %d, want 6 (rune index after the space)", got)
	}
	// An accent carried as a combining mark is part of the word, not a break.
	combining := []rune("éclair x")
	if got := wordRight(combining, 0); got != 7 {
		t.Fatalf("wordRight over a combining mark = %d, want 7", got)
	}
}

// ctrl+left and ctrl+right are handled by the Editor itself rather than by the
// textarea, so they work in every text-entry context and never reach the
// textarea's own (whitespace-only) word motion.
func TestEditorWordMotionKeys(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("hello world")
	e.CursorEnd()

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if _, _, col := e.cursor(); col != 6 {
		t.Fatalf("ctrl+left put the cursor at %d, want 6", col)
	}

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if _, _, col := e.cursor(); col != 0 {
		t.Fatalf("second ctrl+left put the cursor at %d, want 0", col)
	}

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	if _, _, col := e.cursor(); col != 5 {
		t.Fatalf("ctrl+right put the cursor at %d, want 5", col)
	}
}

// A word motion must never edit the text — it is a cursor move, and the keys
// are close enough to the editing ones that a regression here would be quiet.
func TestEditorWordMotionDoesNotEdit(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("keep this text intact")
	e.CursorEnd()
	for range 10 {
		e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	}
	for range 10 {
		e.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	}
	if got := e.Value(); got != "keep this text intact" {
		t.Fatalf("word motion changed the value to %q", got)
	}
}

// At a line boundary the motion steps to the neighbouring line rather than
// stalling, so a held key walks the whole prompt. At the very ends it stays
// put instead of wrapping.
func TestEditorWordMotionCrossesLines(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("first line\nsecond line")

	// Cursor at the start of line 2; ctrl+left steps to the end of line 1.
	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	_, row, col := e.cursor()
	if row != 0 {
		t.Fatalf("ctrl+left did not cross to the previous line: row = %d", row)
	}
	if col != len("first line") {
		t.Fatalf("crossing left landed at col %d, want the end of line 1 (%d)", col, len("first line"))
	}

	// From the end of line 1, ctrl+right steps to the start of line 2.
	e.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	_, row, col = e.cursor()
	if row != 1 || col != 0 {
		t.Fatalf("crossing right landed at row %d col %d, want row 1 col 0", row, col)
	}
}

func TestEditorWordMotionStopsAtTheEnds(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("one\ntwo")

	// Walk to the very start and keep pressing: it must stay there.
	for range 6 {
		e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	}
	if _, row, col := e.cursor(); row != 0 || col != 0 {
		t.Fatalf("ctrl+left past the start landed at row %d col %d, want 0,0", row, col)
	}

	// And the same at the end.
	for range 6 {
		e.Update(tea.KeyMsg{Type: tea.KeyCtrlRight})
	}
	_, row, col := e.cursor()
	if row != 1 || col != 3 {
		t.Fatalf("ctrl+right past the end landed at row %d col %d, want 1,3", row, col)
	}
}

// home and end (Fn+left / Fn+right on a laptop keyboard) keep their meaning:
// they jump to the ends of the logical line, not to a word boundary.
func TestEditorHomeEndJumpToLineEnds(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("some words here")
	e.CursorEnd()

	e.Update(tea.KeyMsg{Type: tea.KeyHome})
	if _, _, col := e.cursor(); col != 0 {
		t.Fatalf("home put the cursor at %d, want 0", col)
	}

	e.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if _, _, col := e.cursor(); col != len("some words here") {
		t.Fatalf("end put the cursor at %d, want %d", col, len("some words here"))
	}
}

// Terminals disagree on how they encode ctrl+arrow, and several forms decode
// with the alt flag set (urxvt's \x1b[Od, xterm's \x1b[1;7D). Matching on the
// key type rather than on String() means those still move by word instead of
// doing nothing.
func TestEditorWordMotionIgnoresStrayAltFlag(t *testing.T) {
	e := NewEditor()
	e.Focus()
	e.SetValue("hello world")
	e.CursorEnd()

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft, Alt: true})
	if _, _, col := e.cursor(); col != 6 {
		t.Fatalf("alt-flagged ctrl+left put the cursor at %d, want 6", col)
	}

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlRight, Alt: true})
	if _, _, col := e.cursor(); col != 11 {
		t.Fatalf("alt-flagged ctrl+right put the cursor at %d, want 11", col)
	}
}

// A blurred editor must not move: every other key is ignored while blurred, so
// these two would otherwise drive a cursor nothing else can reach.
func TestEditorWordMotionRequiresFocus(t *testing.T) {
	e := NewEditor()
	e.SetValue("hello world")
	e.CursorEnd()
	before := func() int { _, _, c := e.cursor(); return c }()

	e.Update(tea.KeyMsg{Type: tea.KeyCtrlLeft})
	if got := func() int { _, _, c := e.cursor(); return c }(); got != before {
		t.Fatalf("a blurred editor moved from %d to %d", before, got)
	}
}
