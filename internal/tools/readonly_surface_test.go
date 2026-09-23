package tools

import (
	"testing"
)

// TestReadOnlySurface pins the agent-mode read_only narrowing: no Write, no
// Edit, no plan-only tools, and a Bash that is the allowlisted copy — while
// the full registry it was derived from keeps the mutating Bash for goal mode.
func TestReadOnlySurface(t *testing.T) {
	full := Default(t.TempDir(), false)
	ro := full.ReadOnlySurface()

	for _, name := range []string{"Write", "Edit", "ExitPlanMode"} {
		if _, ok := ro.Find(name); ok {
			t.Fatalf("%s must not be on the read-only surface", name)
		}
	}
	bt, ok := ro.Find("Bash")
	if !ok {
		t.Fatal("read-only surface must keep an allowlisted Bash")
	}
	if b, ok := bt.(*Bash); !ok || !b.ReadOnly {
		t.Fatalf("Bash on the read-only surface must be ReadOnly, got %#v", bt)
	}
	if ro.Cwd() != full.Cwd() {
		t.Fatal("read-only surface must share the working-directory tracker")
	}

	fb, _ := full.Find("Bash")
	if fb.(*Bash).ReadOnly {
		t.Fatal("deriving the read-only surface must not mutate the full registry's Bash")
	}
	if _, ok := full.Find("Write"); !ok {
		t.Fatal("full registry must keep Write")
	}
}
