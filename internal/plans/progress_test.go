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
