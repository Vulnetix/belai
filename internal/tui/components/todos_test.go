package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/todos"
)

func plain(s string) string { return ansi.Strip(s) }

func TestTodoPanelEmptyRendersNothing(t *testing.T) {
	if got := (TodoPanel{Width: 60}).View(); got != "" {
		t.Fatalf("nil list rendered %q, want empty", got)
	}
	empty := todos.New("goal", nil)
	if got := (TodoPanel{Width: 60, List: &empty}).View(); got != "" {
		t.Fatalf("empty list rendered %q, want empty", got)
	}
}

func TestTodoPanelShowsWindowAroundCurrent(t *testing.T) {
	l := todos.New("goal", []string{"one", "two", "three", "four", "five"})
	l.ApplyMarkers("[DONE:1]")

	out := plain((TodoPanel{Width: 60, List: &l}).View())

	// Window is prev (done), current, next, and a count of the rest.
	for _, want := range []string{"one", "two", "three", "2 more"} {
		if !strings.Contains(out, want) {
			t.Fatalf("panel missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "five") {
		t.Fatalf("panel showed an item beyond the window:\n%s", out)
	}
}

func TestTodoPanelFirstItemHasNoPrevious(t *testing.T) {
	l := todos.New("goal", []string{"one", "two"})
	out := plain((TodoPanel{Width: 60, List: &l}).View())

	if !strings.Contains(out, "one") || !strings.Contains(out, "two") {
		t.Fatalf("panel missing the first window:\n%s", out)
	}
	if strings.Contains(out, "more") {
		t.Fatalf("two items leave nothing beyond the window:\n%s", out)
	}
}

func TestTodoPanelCompletedListShowsLastItem(t *testing.T) {
	l := todos.New("goal", []string{"one", "two"})
	l.MarkAllDone()

	out := plain((TodoPanel{Width: 60, List: &l}).View())
	if !strings.Contains(out, "two") {
		t.Fatalf("finished list must still show what it finished on:\n%s", out)
	}
}

func TestTodoPanelTitleAndWidth(t *testing.T) {
	l := todos.New("goal", []string{"one"})
	out := (TodoPanel{Width: 60, List: &l}).View()

	if !strings.Contains(plain(out), "todo") {
		t.Fatalf("panel missing its title:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := visibleLen(plain(line)); w != 60 {
			t.Fatalf("line width %d, want 60: %q", w, plain(line))
		}
	}
}

func TestTodoPanelTruncatesLongItems(t *testing.T) {
	long := strings.Repeat("x", 500)
	l := todos.New("goal", []string{long})

	for _, line := range strings.Split((TodoPanel{Width: 40, List: &l}).View(), "\n") {
		if w := visibleLen(plain(line)); w > 40 {
			t.Fatalf("line width %d exceeds panel width 40", w)
		}
	}
}
