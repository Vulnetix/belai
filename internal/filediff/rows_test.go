package filediff

import (
	"strconv"
	"strings"
	"testing"
)

// render lays rows out as a compact text form for assertions: sign, old line,
// new line, text.
func render(rows []Row) string {
	var b strings.Builder
	for _, r := range rows {
		switch r.Op {
		case OpAdd:
			b.WriteString("+")
		case OpDel:
			b.WriteString("-")
		case OpElide:
			b.WriteString("…\n")
			continue
		default:
			b.WriteString(" ")
		}
		b.WriteString(lineNo(r.OldLine) + "/" + lineNo(r.NewLine) + " " + r.Text + "\n")
	}
	return b.String()
}

// lineNo renders a line number, or "-" where the row has none.
func lineNo(n int) string {
	if n == 0 {
		return "-"
	}
	return strconv.Itoa(n)
}

func TestRowsNumbersBothSides(t *testing.T) {
	fc := FileChange{
		Path: "a.go",
		Old:  "one\ntwo\nthree",
		New:  "one\nTWO\nthree",
	}
	got := render(fc.Rows())
	want := " 1/1 one\n-2/- two\n+-/2 TWO\n 3/3 three\n"
	if got != want {
		t.Fatalf("rows:\n%s\nwant:\n%s", got, want)
	}
}

// TestRowsNewNumberingSurvivesMultipleHunks is the case a hunk's own metadata
// cannot answer: go-udiff reports each hunk's old-file start only, so the
// new-file numbering has to be carried across hunks or it drifts.
func TestRowsNewNumberingSurvivesMultipleHunks(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 1; i <= 40; i++ {
		oldB.WriteString("line\n")
		newB.WriteString("line\n")
	}
	// Two separated changes: an insertion early, a modification late.
	old := "HEAD\n" + oldB.String() + "TAIL"
	new := "HEAD\nextra\n" + newB.String() + "CHANGED"

	rows := FileChange{Path: "a.go", Old: old, New: new}.Rows()

	var lastAdd Row
	for _, r := range rows {
		if r.Op == OpAdd && r.Text == "CHANGED" {
			lastAdd = r
		}
	}
	if lastAdd.Text == "" {
		t.Fatalf("did not find the trailing change:\n%s", render(rows))
	}
	// The new file is HEAD, extra, 40 lines, CHANGED => line 43.
	if lastAdd.NewLine != 43 {
		t.Fatalf("new-file numbering drifted: got %d, want 43\n%s", lastAdd.NewLine, render(rows))
	}
}

func TestRowsElidesUnchangedRuns(t *testing.T) {
	var old, new strings.Builder
	old.WriteString("first\n")
	new.WriteString("FIRST\n")
	for i := 0; i < 40; i++ {
		old.WriteString("middle\n")
		new.WriteString("middle\n")
	}
	old.WriteString("last")
	new.WriteString("LAST")

	rows := FileChange{Path: "a.go", Old: old.String(), New: new.String()}.Rows()

	var elides, context int
	for _, r := range rows {
		switch r.Op {
		case OpElide:
			elides++
		case OpContext:
			context++
		}
	}
	if elides != 1 {
		t.Fatalf("want one elide between the two hunks, got %d:\n%s", elides, render(rows))
	}
	if context > 2*ContextLines {
		t.Fatalf("too much context kept (%d):\n%s", context, render(rows))
	}
}

func TestRowsIdenticalContentProducesNothing(t *testing.T) {
	fc := FileChange{Path: "a.go", Old: "same\n", New: "same\n"}
	if rows := fc.Rows(); len(rows) != 0 {
		t.Fatalf("expected no rows, got:\n%s", render(rows))
	}
}

func TestRowsExpandTabs(t *testing.T) {
	fc := FileChange{Path: "a.go", Old: "\tone", New: "\ttwo"}
	for _, r := range fc.Rows() {
		if strings.ContainsRune(r.Text, '\t') {
			t.Fatalf("tab survived into a row: %q", r.Text)
		}
		if !strings.HasPrefix(r.Text, "    ") {
			t.Fatalf("tab not expanded to %d spaces: %q", TabWidth, r.Text)
		}
	}
}

// TestWordDiffOnlyForOneToOne pins the gate. A single-line modification gets
// intra-line emphasis; anything else is left plain, because there is no
// reliable pairing between several removed and several added lines.
func TestWordDiffOnlyForOneToOne(t *testing.T) {
	one := FileChange{Path: "a.go", Old: "value := compute(a)", New: "value := compute(b)"}.Rows()
	var marked bool
	for _, r := range one {
		if len(r.Emph) > 0 {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("a one-line change should be word-diffed:\n%s", render(one))
	}

	// Whatever edit script the differ picks, the invariant must hold: a row
	// carries emphasis only when it belongs to a change batch of exactly one
	// removal followed by exactly one addition. Asserting the invariant rather
	// than a fixture keeps this honest against the differ's freedom to choose
	// between equally minimal scripts.
	inputs := []FileChange{
		{Path: "a.go", Old: "alpha\nbravo\ncharlie", New: "ALPHA\nBRAVO\nCHARLIE"},
		{Path: "a.go", Old: "keep\nalpha\nbravo\nkeep2", New: "keep\nwholly\ndifferent\nblock\nkeep2"},
		{Path: "a.go", Old: "one\ntwo\nthree\nfour", New: "one\nfour"},
	}
	for _, fc := range inputs {
		rows := fc.Rows()
		for i, r := range rows {
			if len(r.Emph) == 0 {
				continue
			}
			if !inOneToOneBatch(rows, i) {
				t.Fatalf("emphasis outside a 1:1 batch at row %d (%+v):\n%s", i, r, render(rows))
			}
		}
	}
}

// inOneToOneBatch reports whether the row at i belongs to a change batch made
// of exactly one removal followed by exactly one addition.
func inOneToOneBatch(rows []Row, i int) bool {
	start := i
	for start > 0 && (rows[start-1].Op == OpDel || rows[start-1].Op == OpAdd) {
		start--
	}
	dels := 0
	for start+dels < len(rows) && rows[start+dels].Op == OpDel {
		dels++
	}
	adds := 0
	for start+dels+adds < len(rows) && rows[start+dels+adds].Op == OpAdd {
		adds++
	}
	return dels == 1 && adds == 1
}

// TestWordDiffMarksOnlyTheChangedPart keeps the highlight meaningful.
func TestWordDiffMarksOnlyTheChangedPart(t *testing.T) {
	rows := FileChange{Path: "a.go", Old: "foo := newThing()", New: "bar := newThing()"}.Rows()

	for _, r := range rows {
		if r.Op == OpContext || r.Op == OpElide {
			continue
		}
		if len(r.Emph) != 1 {
			t.Fatalf("want one span on %q, got %v", r.Text, r.Emph)
		}
		got := r.Text[r.Emph[0].From:r.Emph[0].To]
		want := map[Op]string{OpDel: "foo", OpAdd: "bar"}[r.Op]
		if got != want {
			t.Fatalf("span covers %q, want %q", got, want)
		}
	}
}

// TestWordDiffSkipsLeadingIndentation: a span starting at the left edge would
// highlight the indentation, which reads as a flashing block down the side of
// the diff and never says anything useful.
func TestWordDiffSkipsLeadingIndentation(t *testing.T) {
	rows := FileChange{
		Path: "a.go",
		Old:  "    return old",
		New:  "        return new",
	}.Rows()
	for _, r := range rows {
		for _, sp := range r.Emph {
			if sp.From == 0 {
				t.Fatalf("span starts in the indentation of %q: %v", r.Text, r.Emph)
			}
		}
	}
}

func TestStatCountsRows(t *testing.T) {
	rows := []Row{
		{Op: OpContext}, {Op: OpDel}, {Op: OpAdd}, {Op: OpAdd}, {Op: OpElide},
	}
	if added, removed := Stat(rows); added != 2 || removed != 1 {
		t.Fatalf("stat = +%d -%d, want +2 -1", added, removed)
	}
}

// TestRowsReconstructBothFiles is the property that actually matters, and the
// one an exact-count assertion cannot express: whichever minimal edit script
// the differ chooses, replaying the context and removed rows must rebuild the
// old file, and the context and added rows must rebuild the new one.
//
// The fixtures are small enough that nothing is elided, so reconstruction is
// total.
func TestRowsReconstructBothFiles(t *testing.T) {
	cases := []struct{ name, old, new string }{
		{"modify", "a\nb\nc\n", "a\nB\nc\n"},
		{"append", "a\nb\n", "a\nb\nc\nd\n"},
		{"delete", "a\nb\nc\nd\n", "a\nd\n"},
		{"create", "", "one\ntwo\n"},
		{"remove all", "one\ntwo\n", ""},
		{"replace block", "keep\nalpha\nbravo\nkeep2\n", "keep\nwholly\ndifferent\nblock\nkeep2\n"},
		{"no trailing newline", "a\nb", "a\nB"},
		{"leading change", "first\nsame\n", "FIRST\nsame\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := FileChange{Path: "a.go", Old: tc.old, New: tc.new}.Rows()
			if len(rows) == 0 {
				if tc.old != tc.new {
					t.Fatalf("no rows for a real change")
				}
				return
			}
			for _, r := range rows {
				if r.Op == OpElide {
					t.Skip("fixture elided; reconstruction is only total without elision")
				}
			}

			var gotOld, gotNew []string
			for _, r := range rows {
				switch r.Op {
				case OpContext:
					gotOld = append(gotOld, r.Text)
					gotNew = append(gotNew, r.Text)
				case OpDel:
					gotOld = append(gotOld, r.Text)
				case OpAdd:
					gotNew = append(gotNew, r.Text)
				}
			}
			wantOld := splitFile(expandTabsLines(tc.old))
			wantNew := splitFile(expandTabsLines(tc.new))
			if strings.Join(gotOld, "\n") != strings.Join(wantOld, "\n") {
				t.Fatalf("old side rebuilt as %q, want %q\n%s", gotOld, wantOld, render(rows))
			}
			if strings.Join(gotNew, "\n") != strings.Join(wantNew, "\n") {
				t.Fatalf("new side rebuilt as %q, want %q\n%s", gotNew, wantNew, render(rows))
			}
		})
	}
}

// splitFile splits a file into the lines a unified diff enumerates: a trailing
// newline terminates the last line rather than starting an empty one.
func splitFile(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func TestRowsHandlesCreateAndDelete(t *testing.T) {
	created := FileChange{Path: "new.go", Old: "", New: "one\ntwo", Created: true}.Rows()
	if a, r := Stat(created); a != 2 || r != 0 {
		t.Fatalf("created file: +%d -%d\n%s", a, r, render(created))
	}
	deleted := FileChange{Path: "gone.go", Old: "one\ntwo", New: "", Deleted: true}.Rows()
	if a, r := Stat(deleted); a != 0 || r != 2 {
		t.Fatalf("deleted file: +%d -%d\n%s", a, r, render(deleted))
	}
}

func TestExpandedTabExpandsBothSides(t *testing.T) {
	fc := FileChange{Path: "a.go", Old: "\told", New: "\tnew"}
	old, new := fc.Expanded()
	if old != "    old" || new != "    new" {
		t.Fatalf("Expanded = (%q, %q), want tabs expanded", old, new)
	}
}

func TestRowsSkipsBinaryAndTruncated(t *testing.T) {
	if rows := (FileChange{Path: "x", Old: "a", New: "b", Binary: true}).Rows(); rows != nil {
		t.Fatal("binary files must not be diffed")
	}
	if rows := (FileChange{Path: "x", Old: "a", New: "b", Truncated: true}).Rows(); rows != nil {
		t.Fatal("truncated files must not be diffed")
	}
}
