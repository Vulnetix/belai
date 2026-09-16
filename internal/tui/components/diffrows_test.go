package components

import (
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/vulnetix/signet/internal/filediff"
)

func bashRowWithDiff(ch *filediff.Change) Message {
	m := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"sed -i s/foo/bar/ main.go"}`,
		Content:  "",
		Status:   "✓",
	}
	m.SetDiff(ch)
	return m
}

func oneFileChange() *filediff.Change {
	return &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go",
		Old:  "package main\n\nfunc main() {\n    foo := newThing()\n    println(foo)\n}\n",
		New:  "package main\n\nfunc main() {\n    bar := newThing()\n    println(bar)\n}\n",
	}}}
}

func TestDiffRowShowsPathAndStat(t *testing.T) {
	out, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, true)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "main.go") {
		t.Fatalf("no path in:\n%s", plain)
	}
	if !regexp.MustCompile(`\+2\s+−2`).MatchString(plain) {
		t.Fatalf("no +/- stat in:\n%s", plain)
	}
}

// TestDiffRowNumbersBothSides pins the gutter: added lines carry new-file
// numbering, removed lines old-file numbering.
func TestDiffRowNumbersBothSides(t *testing.T) {
	out, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, true)
	plain := ansi.Strip(out)

	if !regexp.MustCompile(`−\s*4 .*foo := newThing`).MatchString(plain) {
		t.Fatalf("removed line not numbered from the old file:\n%s", plain)
	}
	if !regexp.MustCompile(`\+\s*4 .*bar := newThing`).MatchString(plain) {
		t.Fatalf("added line not numbered from the new file:\n%s", plain)
	}
}

// TestDiffCollapsedIsChangeAnchored: six rows of leading context would be six
// wasted rows, so a collapsed diff starts at the first change.
func TestDiffCollapsedIsChangeAnchored(t *testing.T) {
	var old, new strings.Builder
	for i := 0; i < 30; i++ {
		old.WriteString("unchanged\n")
		new.WriteString("unchanged\n")
	}
	old.WriteString("before\n")
	new.WriteString("after\n")

	ch := &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go", Old: old.String(), New: new.String(),
	}}}

	out, _ := toolRow(bashRowWithDiff(ch), 80, false)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "before") || !strings.Contains(plain, "after") {
		t.Fatalf("collapsed diff does not show the change:\n%s", plain)
	}
	// Header + file header + at most diffPreviewRows body rows + a hint row.
	if n := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); n > diffPreviewRows+3 {
		t.Fatalf("collapsed diff is %d rows:\n%s", n, plain)
	}
}

// TestDiffWashOnlyWhenExpanded is the colour model: collapsed rows are
// foreground-only, expanded rows get a row wash with syntax colour on top.
func TestDiffWashOnlyWhenExpanded(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		collapsed, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, false)
		expanded, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, true)

		addBg := bgSeq(ColorDiffAddBg)
		if addBg == "" {
			t.Fatal("expected a background sequence under TrueColor")
		}
		if strings.Contains(collapsed, addBg) {
			t.Fatalf("collapsed diff should not be washed:\n%q", collapsed)
		}
		if !strings.Contains(expanded, addBg) {
			t.Fatalf("expanded diff should be washed:\n%q", expanded)
		}
	})
}

// TestDiffWashNeverTornByAFullReset is the single most valuable assertion
// here: a \x1b[0m inside a washed row clears the background as well as the
// foreground, so the wash would stop at the first syntax-coloured token and
// the rest of the line would render bare. This is what stops someone
// reintroducing lipgloss.Render inside a diff row.
func TestDiffWashNeverTornByAFullReset(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		out, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, true)
		for i, line := range strings.Split(out, "\n") {
			if !strings.Contains(line, bgSeq(ColorDiffAddBg)) &&
				!strings.Contains(line, bgSeq(ColorDiffDelBg)) {
				continue
			}
			if strings.Contains(line, "\x1b[0m") {
				t.Fatalf("washed row %d contains a full reset:\n%q", i, line)
			}
			if !strings.HasSuffix(line, bgOff) {
				t.Fatalf("washed row %d does not close its background:\n%q", i, line)
			}
		}
	})
}

// TestDiffSyntaxRidesOnTheWash: the point of moving to bg-per-row is that
// syntax colour becomes free to carry structure inside a changed line.
func TestDiffSyntaxRidesOnTheWash(t *testing.T) {
	ch := &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go",
		Old:  "package main\n\nfunc main() {\n    return nil\n}\n",
		New:  "package main\n\nfunc main() {\n    return errors.New(\"boom\")\n}\n",
	}}}
	withProfile(t, termenv.TrueColor, func() {
		out, _ := toolRow(bashRowWithDiff(ch), 100, true)
		if !strings.Contains(out, fgSeq(ColorAmber)) {
			t.Fatalf("string literal not syntax coloured inside the diff:\n%q", out)
		}
	})
}

// TestDiffWordEmphasisRendersAsReverseVideo: reverse video is used because it
// composes with a background the segment cannot see.
//
// The fixture changes exactly one line, which is the only shape the intra-line
// gate accepts — see filediff.markWordChanges.
func TestDiffWordEmphasisRendersAsReverseVideo(t *testing.T) {
	ch := &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go",
		Old:  "package main\n\nfunc main() {\n    foo := newThing()\n}\n",
		New:  "package main\n\nfunc main() {\n    bar := newThing()\n}\n",
	}}}
	withProfile(t, termenv.TrueColor, func() {
		out, _ := toolRow(bashRowWithDiff(ch), 80, true)
		if !strings.Contains(out, emphOn) {
			t.Fatalf("no intra-line emphasis in:\n%q", out)
		}
	})
}

// TestDiffMultiLineChangeHasNoEmphasis is the other half of the gate: with
// several removed and several added lines there is no reliable pairing, so a
// word diff across them would be scattered nonsense.
func TestDiffMultiLineChangeHasNoEmphasis(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		// oneFileChange alters two adjacent lines.
		out, _ := toolRow(bashRowWithDiff(oneFileChange()), 80, true)
		if strings.Contains(out, emphOn) {
			t.Fatalf("multi-line change should not be word-diffed:\n%q", out)
		}
	})
}

// TestDiffHighlightingAlignsWithRowText guards a subtle break: the rows have
// their tabs expanded, so a highlighter fed the raw file measures the
// indentation in different cells. Every column then shifts, taking the
// intra-line emphasis spans with it.
func TestDiffHighlightingAlignsWithRowText(t *testing.T) {
	ch := &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go",
		Old:  "package main\n\nfunc main() {\n\tname := \"old\"\n}\n",
		New:  "package main\n\nfunc main() {\n\tname := \"new\"\n}\n",
	}}}
	withProfile(t, termenv.TrueColor, func() {
		out, _ := toolRow(bashRowWithDiff(ch), 80, true)
		plain := ansi.Strip(out)

		// The tab must survive as four cells on the washed rows, exactly as it
		// does on the unwashed context rows.
		if !strings.Contains(plain, "    name := \"old\"") {
			t.Fatalf("indentation lost on a highlighted diff row:\n%s", plain)
		}

		// And the emphasis must cover the changed word only.
		for _, line := range strings.Split(out, "\n") {
			i, j := strings.Index(line, emphOn), strings.Index(line, emphOff)
			if i < 0 || j < 0 {
				continue
			}
			marked := ansi.Strip(line[i+len(emphOn) : j])
			if marked != "old" && marked != "new" {
				t.Fatalf("emphasis covers %q, want the changed word only", marked)
			}
		}
	})
}

func TestDiffUnavailableIsQuietWhenCollapsed(t *testing.T) {
	ch := &filediff.Change{Unavailable: "diff unavailable: the command's targets are unknown"}

	collapsed, _ := toolRow(bashRowWithDiff(ch), 80, false)
	if strings.Contains(ansi.Strip(collapsed), "unavailable") {
		t.Fatalf("an unavailable diff should stay out of the collapsed view:\n%s", collapsed)
	}
	expanded, _ := toolRow(bashRowWithDiff(ch), 80, true)
	if !strings.Contains(ansi.Strip(expanded), "unavailable") {
		t.Fatalf("expanded view should explain why there is no diff:\n%s", expanded)
	}
	// Never an error colour: not producing a diff is not a failure.
	withProfile(t, termenv.TrueColor, func() {
		out, _ := toolRow(bashRowWithDiff(ch), 80, true)
		if strings.Contains(out, fgSeq(ColorDanger)) {
			t.Fatalf("unavailable notice must not be rendered as an error:\n%q", out)
		}
	})
}

func TestDiffBinaryAndDeletedFiles(t *testing.T) {
	ch := &filediff.Change{Files: []filediff.FileChange{
		{Path: "blob.bin", Binary: true},
		{Path: "gone.go", Old: "package main\n", Deleted: true},
	}}
	out, _ := toolRow(bashRowWithDiff(ch), 80, true)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "binary file changed") {
		t.Fatalf("binary file not reported:\n%s", plain)
	}
	if !strings.Contains(plain, "deleted") {
		t.Fatalf("deleted file not reported:\n%s", plain)
	}
}

// TestDiffRowInvariants runs diff rows through the same LineMap contract as
// every other row, at both profiles and across the whole width ladder.
func TestDiffRowInvariants(t *testing.T) {
	changes := []*filediff.Change{
		oneFileChange(),
		{Unavailable: "diff unavailable: not a git repo and the command's targets are unknown"},
		{Files: []filediff.FileChange{
			{Path: "a.go", Old: "one\ntwo\n", New: "one\nTWO\nthree\n"},
			{Path: "b.go", Old: "", New: "package b\n"},
		}},
	}
	for _, profile := range renderProfiles {
		withProfile(t, profile, func() {
			for _, expand := range []bool{false, true} {
				for width := 30; width <= 140; width += 3 {
					for i, ch := range changes {
						ml := MessageList{
							Width: width, ShowTools: true, ExpandAll: expand,
							Messages: []Message{bashRowWithDiff(ch)},
						}
						rendered, lm := ml.Render()
						assertLineMapInvariant(t,
							profileName(profile)+"/change"+string(rune('0'+i)),
							rendered, lm, max(width, messageMinWidth))
					}
				}
			}
		})
	}
}

// TestDiffWidthLadderDegrades: the gutter gives up its parts in order — the
// spacing, then the numbers, then the diff itself — rather than letting the
// code column vanish.
func TestDiffWidthLadderDegrades(t *testing.T) {
	for _, width := range []int{140, 80, 60, 45, 34, 30} {
		out, _ := toolRow(bashRowWithDiff(oneFileChange()), width, true)
		for _, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > max(width, messageMinWidth) {
				t.Fatalf("width %d: row is %d cells: %q", width, w, line)
			}
		}
		if !strings.Contains(ansi.Strip(out), "newThing") {
			t.Fatalf("width %d: the code column disappeared:\n%s", width, ansi.Strip(out))
		}
	}
}

// TestDiffHintCopiesTheWholeDiff: a diff is a document, and half of one
// without its signs and line numbers is not useful, so the hidden remainder
// keeps its gutters.
func TestDiffHintCopiesTheWholeDiff(t *testing.T) {
	// Two separated changes, so the diff is longer than a collapsed row shows
	// and something is actually hidden.
	var old, new strings.Builder
	old.WriteString("head-before\n")
	new.WriteString("head-after\n")
	for i := 0; i < 30; i++ {
		old.WriteString("unchanged\n")
		new.WriteString("unchanged\n")
	}
	old.WriteString("before\n")
	new.WriteString("after\n")
	ch := &filediff.Change{Files: []filediff.FileChange{{
		Path: "main.go", Old: old.String(), New: new.String(),
	}}}

	_, lm := toolRow(bashRowWithDiff(ch), 80, false)
	var hidden string
	for _, sl := range lm {
		if sl.MarkerWidth > 0 && strings.Contains(sl.Hidden, "after") {
			hidden = sl.Hidden
		}
	}
	if hidden == "" {
		t.Fatal("no truncation marker carrying the diff")
	}
	if !strings.Contains(hidden, "+") || !strings.Contains(hidden, "−") {
		t.Fatalf("hidden diff lost its signs:\n%s", hidden)
	}
}

// TestDiffNeverReachesTheModel guards the whole feature's render-only
// contract at the component level.
func TestDiffAttachDoesNotChangeContent(t *testing.T) {
	m := Message{Role: "tool", ToolName: "Bash", Content: "ok"}
	before := m.Text()
	m.SetDiff(oneFileChange())
	if m.Text() != before {
		t.Fatalf("attaching a diff changed the message content: %q", m.Text())
	}
}
