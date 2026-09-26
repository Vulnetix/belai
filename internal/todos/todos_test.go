package todos

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/session"
)

func TestNewDropsBlankStepsAndSetsActive(t *testing.T) {
	l := New("ship it", []string{" one ", "", "  ", "two", "three"})
	if len(l.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(l.Items))
	}
	if l.Items[0].Text != "one" || l.Items[0].N != 1 {
		t.Fatalf("item 0 = %+v", l.Items[0])
	}
	if l.Items[0].Status != StatusActive {
		t.Fatalf("first item should be active, got %q", l.Items[0].Status)
	}
	for _, it := range l.Items[1:] {
		if it.Status != StatusPending {
			t.Fatalf("non-first item should be pending, got %+v", it)
		}
	}
	if l.Prompt != "ship it" {
		t.Fatalf("prompt = %q", l.Prompt)
	}
	if l.ID == "" {
		t.Fatal("expected a generated ID")
	}
}

func TestApplyMarkersAdvancesAndReactivates(t *testing.T) {
	l := New("p", []string{"a", "b", "c"})
	l.ApplyMarkers("done with [DONE:1] and [DONE:3]")
	if l.Items[0].Status != StatusDone {
		t.Fatalf("item 1 should be done, got %q", l.Items[0].Status)
	}
	if l.Items[1].Status != StatusActive {
		t.Fatalf("item 2 should now be active, got %q", l.Items[1].Status)
	}
	if l.Items[2].Status != StatusDone {
		t.Fatalf("item 3 should be done, got %q", l.Items[2].Status)
	}

	// Out-of-range and malformed markers are ignored.
	l.ApplyMarkers("[DONE:99] [DONE:x] [DONE:]")
	if l.Items[0].Status != StatusDone || l.Items[1].Status != StatusActive {
		t.Fatalf("malformed markers must not change state: %+v", l.Items)
	}
}

func TestAdoptReplacesAndResyncs(t *testing.T) {
	l := New("p", []string{"a", "b"})
	l.ApplyMarkers("[DONE:1]")
	l.Adopt([]Item{{N: 1, Text: "x", Status: StatusPending}, {N: 2, Text: "y", Status: StatusDone}})
	if len(l.Items) != 2 || l.Items[0].Text != "x" || l.Items[1].Text != "y" {
		t.Fatalf("adopt = %+v", l.Items)
	}
	if l.Items[0].Status != StatusActive {
		t.Fatalf("first adopted item should be active, got %q", l.Items[0].Status)
	}
	if l.Items[1].Status != StatusDone {
		t.Fatalf("adopted done item must stay done, got %q", l.Items[1].Status)
	}
}

func TestAdoptIgnoresClearedList(t *testing.T) {
	l := New("p", []string{"a"})
	l.Cleared = true
	l.Adopt([]Item{{N: 1, Text: "x", Status: StatusPending}})
	if len(l.Items) != 1 || l.Items[0].Text != "a" {
		t.Fatalf("cleared list must not adopt: %+v", l.Items)
	}
}

func TestMarkAllDone(t *testing.T) {
	l := New("p", []string{"a", "b", "c"})
	l.ApplyMarkers("[DONE:1]")
	l.MarkAllDone()
	for _, it := range l.Items {
		if it.Status != StatusDone {
			t.Fatalf("item %d = %q, want done", it.N, it.Status)
		}
	}
	if !l.Complete() {
		t.Fatal("MarkAllDone must complete the list")
	}
}

func TestMarkAllDoneIgnoresClearedList(t *testing.T) {
	l := New("p", []string{"a"})
	l.Cleared = true
	l.MarkAllDone()
	if l.Items[0].Status == StatusDone {
		t.Fatal("cleared list must not advance")
	}
}

func TestApplyMarkersIgnoresClearedList(t *testing.T) {
	l := New("p", []string{"a", "b"})
	l.Cleared = true
	l.ApplyMarkers("[DONE:1] [DONE:2]")
	for _, it := range l.Items {
		if it.Status == StatusDone {
			t.Fatalf("cleared list must not advance: %+v", l.Items)
		}
	}
}

func TestComplete(t *testing.T) {
	if New("p", nil).Complete() {
		t.Fatal("empty list must not be complete")
	}
	l := New("p", []string{"a", "b"})
	if l.Complete() {
		t.Fatal("incomplete list must not be complete")
	}
	l.ApplyMarkers("[DONE:1] [DONE:2]")
	if !l.Complete() {
		t.Fatal("all items done should be complete")
	}
}

func TestProgressReusesPlans(t *testing.T) {
	l := New("p", []string{"a", "b", "c"})
	l.ApplyMarkers("[DONE:1] [DONE:3]")
	p := l.Progress()
	if p.Total != 3 {
		t.Fatalf("total = %d", p.Total)
	}
	if p.Completed() != 2 {
		t.Fatalf("completed = %d, want 2", p.Completed())
	}
	rem := p.Remaining()
	if len(rem) != 1 || rem[0] != 2 {
		t.Fatalf("remaining = %v, want [2]", rem)
	}
}

func TestWindowBoundaries(t *testing.T) {
	// Empty.
	p, c, n, more := New("p", nil).Window()
	if p != nil || c != nil || n != nil || more != 0 {
		t.Fatalf("empty window = %v %v %v %d", p, c, n, more)
	}

	// Single active item.
	p, c, n, more = New("p", []string{"a"}).Window()
	if p != nil || c == nil || c.Text != "a" || n != nil || more != 0 {
		t.Fatalf("single-active window = %v %v %v %d", p, c, n, more)
	}

	// Single done item: completed list surfaces the last item as prev.
	l := New("p", []string{"a"})
	l.ApplyMarkers("[DONE:1]")
	p, c, n, more = l.Window()
	if p == nil || p.Text != "a" || c != nil || n != nil || more != 0 {
		t.Fatalf("single-done window = %v %v %v %d", p, c, n, more)
	}

	// First item active in a longer list: no prev, next present.
	p, c, n, more = New("p", []string{"a", "b", "c", "d"}).Window()
	if p != nil || c == nil || c.Text != "a" || n == nil || n.Text != "b" || more != 2 {
		t.Fatalf("first-active window = %v %v %v %d", p, c, n, more)
	}

	// Last item active: prev and current, no next.
	l = New("p", []string{"a", "b", "c"})
	l.ApplyMarkers("[DONE:1] [DONE:2]")
	p, c, n, more = l.Window()
	if p == nil || p.Text != "b" || c == nil || c.Text != "c" || n != nil || more != 0 {
		t.Fatalf("last-active window = %v %v %v %d", p, c, n, more)
	}
}

func TestEntryRoundTrip(t *testing.T) {
	l := New("p", []string{"a", "b"})
	l.ApplyMarkers("[DONE:1]")

	e := l.ToEntry("parent")
	if e.Type != EntryType {
		t.Fatalf("entry type = %q", e.Type)
	}
	if e.ParentID != "parent" {
		t.Fatalf("parent id = %q", e.ParentID)
	}

	back, err := FromEntry(e)
	if err != nil {
		t.Fatalf("FromEntry: %v", err)
	}
	if back.ID != l.ID || back.Prompt != l.Prompt || len(back.Items) != 2 {
		t.Fatalf("round-trip mismatch: %+v vs %+v", back, l)
	}
	if back.Items[0].Status != StatusDone || back.Items[1].Status != StatusActive {
		t.Fatalf("round-trip statuses = %+v", back.Items)
	}
}

func TestFromEntryRejectsMalformed(t *testing.T) {
	if _, err := FromEntry(session.Entry{Content: "not json"}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestClearingPreservesPriorEntry(t *testing.T) {
	l := New("p", []string{"a", "b"})
	cleared := l.ClearedCopy()
	if !cleared.Cleared {
		t.Fatal("ClearedCopy must set Cleared")
	}
	// The original is untouched: clearing supersedes, never mutates history.
	if l.Cleared {
		t.Fatal("ClearedCopy must not mutate the original")
	}
	if len(cleared.Items) != len(l.Items) {
		t.Fatalf("cleared copy lost items: %+v", cleared.Items)
	}
}

func TestLatestWins(t *testing.T) {
	first := New("p", []string{"a"})
	second := New("p2", []string{"x", "y"})
	entries := []session.Entry{
		{Type: "user", Content: "ignored"},
		first.ToEntry(""),
		second.ToEntry(""),
		{Type: EntryType, Content: "garbage"}, // malformed: skipped
	}

	got, ok := Latest(entries)
	if !ok {
		t.Fatal("expected a latest list")
	}
	if got.ID != second.ID {
		t.Fatalf("latest = %q, want %q", got.ID, second.ID)
	}

	if _, ok := Latest(nil); ok {
		t.Fatal("empty entries should not find a list")
	}
}

func TestRender(t *testing.T) {
	l := New("p", []string{"a", "b"})
	l.ApplyMarkers("[DONE:1]")
	out := l.Render()
	if !strings.Contains(out, "[x] a") || !strings.Contains(out, "[>] b") {
		t.Fatalf("render = %q", out)
	}
	if New("p", nil).Render() != "(no plan recorded)" {
		t.Fatalf("empty render = %q", New("p", nil).Render())
	}
}
