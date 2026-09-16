package plans

import (
	"reflect"
	"testing"
)

func TestProgressApply(t *testing.T) {
	p := NewProgress(3)
	p.Apply("[DONE:1] [DONE:2]")
	if p.Completed() != 2 {
		t.Fatalf("Completed = %d, want 2", p.Completed())
	}
	if !reflect.DeepEqual(p.Remaining(), []int{3}) {
		t.Fatalf("Remaining = %v", p.Remaining())
	}
	if p.Done() {
		t.Fatalf("Done = true, want false")
	}

	p.Apply("finished [DONE:3]")
	if !p.Done() {
		t.Fatalf("Done = false, want true")
	}
	if p.Completed() != 3 {
		t.Fatalf("Completed = %d, want 3", p.Completed())
	}
}

func TestProgressIgnoresOutOfRange(t *testing.T) {
	p := NewProgress(2)
	p.Apply("[DONE:0] [DONE:5]")
	if p.Completed() != 0 {
		t.Fatalf("Completed = %d, want 0", p.Completed())
	}
}

func TestParseDoneMarkersReturnsNumbersInOrder(t *testing.T) {
	got := ParseDoneMarkers("did [DONE:3] then [DONE:1] and [DONE:3] again")
	want := []int{3, 1, 3}

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseDoneMarkersIgnoresNonMarkers(t *testing.T) {
	for _, text := range []string{
		"",
		"no markers here",
		"[DONE]",
		"[DONE:]",
		"[DONE:x]",
		"DONE:1",
	} {
		if got := ParseDoneMarkers(text); len(got) != 0 {
			t.Fatalf("ParseDoneMarkers(%q) = %v, want none", text, got)
		}
	}
}

func TestProgressApplyUsesParseDoneMarkers(t *testing.T) {
	p := NewProgress(3)
	p.Apply("finished [DONE:1] and [DONE:3]")

	if p.Completed() != 2 {
		t.Fatalf("Completed() = %d, want 2", p.Completed())
	}
	if p.Done() {
		t.Fatal("two of three steps is not done")
	}
}
