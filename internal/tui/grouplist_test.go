package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// glFixture is n groups of rows each, one with a section heading.
func glFixture(n, rows int) []glGroup {
	var groups []glGroup
	idx := 0
	for g := 0; g < n; g++ {
		grp := glGroup{key: fmt.Sprintf("g%d", g), title: fmt.Sprintf("Group %d", g)}
		if g == 1 {
			grp.rows = append(grp.rows, glRow{idx: -1, header: true, label: "section"})
		}
		for r := 0; r < rows; r++ {
			src := "default"
			if r == 1 {
				src = "global"
			}
			grp.rows = append(grp.rows, glRow{
				idx: idx, key: fmt.Sprintf("k%d", idx), label: fmt.Sprintf("row %d", idx),
				value: "value", src: src, match: "help text",
				detail: []string{"row " + fmt.Sprint(idx), "some detail that is long enough to wrap across a narrow pane when the terminal is small"},
			})
			idx++
		}
		groups = append(groups, grp)
	}
	return groups
}

func TestGlBodyFitsAnyTerminal(t *testing.T) {
	groups := glFixture(9, 30)
	for _, w := range []int{60, 80, 120} {
		for _, h := range []int{12, 18, 24, 40} {
			for _, cursor := range []int{0, 45, 100, 269} {
				var st glState
				out := glBody(glSpec{groups: groups, cursor: cursor}, &st, w, h)
				lines := strings.Split(out, "\n")
				if len(lines) > h {
					t.Fatalf("w=%d h=%d cursor=%d: %d lines, want at most %d\n%s", w, h, cursor, len(lines), h, out)
				}
				for _, l := range lines {
					if got := ansi.StringWidth(l); got > w {
						t.Fatalf("w=%d h=%d: line is %d cells wide: %q", w, h, got, l)
					}
				}
				if !strings.Contains(out, "row "+fmt.Sprint(cursor)) {
					t.Fatalf("w=%d h=%d: cursor row %d is not on screen\n%s", w, h, cursor, out)
				}
			}
		}
	}
}

func TestGlBodyUsesRailOnlyWhenWide(t *testing.T) {
	groups := glFixture(3, 4)
	var st glState
	wide := glBody(glSpec{groups: groups, cursor: 0}, &st, 120, 20)
	if !strings.Contains(wide, "Group 2") || strings.Contains(wide, "1/3") {
		t.Fatalf("wide layout should list every group in a rail:\n%s", wide)
	}
	narrow := glBody(glSpec{groups: groups, cursor: 0}, &st, 80, 20)
	if !strings.Contains(narrow, "1/3") {
		t.Fatalf("narrow layout should show the tab strip's position:\n%s", narrow)
	}
}

func TestGlNavigationSkipsHeadersAndStaysInGroup(t *testing.T) {
	groups := glFixture(3, 3)
	// Group 1 starts with a header; its first row is idx 3.
	if got := glStepGroup(groups, 0, 1); got != 3 {
		t.Fatalf("next group's first row = %d, want 3", got)
	}
	if got := glStep(groups, 3, -1); got != 3 {
		t.Fatalf("step up from a group's first row = %d, want it to stay", got)
	}
	if got := glStep(groups, 3, 5); got != 5 {
		t.Fatalf("step down clamps at the group's last row, got %d", got)
	}
	if got := glStepGroup(groups, 0, -1); got != 6 {
		t.Fatalf("previous group wraps to the last, got %d", got)
	}
}

func TestGlScrollKeepsTheCursorAndMarksTheRest(t *testing.T) {
	groups := glFixture(1, 40)
	var st glState
	out := glBody(glSpec{groups: groups, cursor: 30}, &st, 120, 16)
	if !strings.Contains(out, "above") || !strings.Contains(out, "below") {
		t.Fatalf("a scrolled list should say what is above and below:\n%s", out)
	}
	first := st.scroll
	// Moving up one row inside the window must not move the window.
	glBody(glSpec{groups: groups, cursor: 29}, &st, 120, 16)
	if st.scroll != first {
		t.Fatalf("scroll moved from %d to %d for a cursor still in view", first, st.scroll)
	}
}

func TestGlFilterAndChangedOnly(t *testing.T) {
	groups := glFixture(3, 4)
	st := glState{changed: true}
	vis := st.visible(groups)
	if len(vis) != 1 || vis[0].selectable() != 3 {
		t.Fatalf("changed-only should keep the one overridden row per group, got %+v", vis)
	}
	st = glState{filter: "row 7"}
	vis = st.visible(groups)
	if len(vis) != 1 || vis[0].selectable() != 1 || vis[0].rows[0].idx != 7 {
		t.Fatalf("filter should find row 7 alone, got %+v", vis)
	}
	if got := glSnap(vis, 0); got != 7 {
		t.Fatalf("a cursor hidden by the filter snaps to the first match, got %d", got)
	}
	if !strings.Contains(strings.Join(vis[0].rows[0].detail, "\n"), "in Group 1") {
		t.Fatal("a result should name its group")
	}
}

func TestGlFilterKeyEditsAndEscClears(t *testing.T) {
	st := glState{filtering: true}
	for _, r := range "ab" {
		st.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	st.key(tea.KeyMsg{Type: tea.KeyBackspace})
	if st.filter != "a" {
		t.Fatalf("filter = %q, want a", st.filter)
	}
	st.key(tea.KeyMsg{Type: tea.KeyEsc})
	if st.filter != "" || st.filtering {
		t.Fatalf("esc should clear and stop filtering: %+v", st)
	}
	if st.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}) {
		t.Fatal("keys are only consumed while filtering")
	}
}

func TestGlHelpLineDropsLeastImportantFirst(t *testing.T) {
	keys := []glKey{{"↑↓", "move", 1}, {"esc", "back", 1}, {"m", "changed", 5}, {"s", "scope", 4}}
	full := glHelpLine(keys, 200)
	if !strings.Contains(full, "changed") {
		t.Fatalf("a wide line keeps every key: %q", full)
	}
	narrow := glHelpLine(keys, ansi.StringWidth(glHelpLine(keys[:2], 200)))
	if strings.Contains(narrow, "changed") || strings.Contains(narrow, "scope") || !strings.Contains(narrow, "back") {
		t.Fatalf("a narrow line drops the low-priority keys and keeps back: %q", narrow)
	}
}
