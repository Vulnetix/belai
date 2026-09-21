package components

import (
	"strings"
	"testing"
)

// The rotation must walk the whole table before repeating: a user watching a
// long turn should not see the same hint twice while others go unshown.
func TestCycleTipCoversEveryTip(t *testing.T) {
	seen := map[string]bool{}
	for step := 0; step < TipCount(); step++ {
		seen[CycleTip("seed", step)] = true
	}
	if len(seen) != TipCount() {
		t.Fatalf("rotation covered %d of %d tips", len(seen), TipCount())
	}
	if CycleTip("seed", TipCount()) != CycleTip("seed", 0) {
		t.Fatal("rotation must wrap to its first tip")
	}
}

// Step 0 is the banner's own pick, so the composer line starts on the hint the
// session already showed rather than jumping.
func TestCycleTipStepZeroMatchesPickTip(t *testing.T) {
	if CycleTip("seed-a", 0) != PickTip("seed-a") {
		t.Fatal("step 0 must match the banner tip for the same seed")
	}
}

// A negative step can only come from a clock that moved backwards; it must not
// panic on a negative index.
func TestCycleTipNegativeStep(t *testing.T) {
	if CycleTip("seed", -3) == "" {
		t.Fatal("negative step returned no tip")
	}
}

// The first-run hint is the one hint a new user must see, so it stays out of
// the rotation and names both the signup URL and the default model.
func TestFirstRunTipIsNotInRotation(t *testing.T) {
	for step := 0; step < TipCount(); step++ {
		if CycleTip("seed", step) == FirstRunTip {
			t.Fatal("the first-run tip must not be in the rotation")
		}
	}
	if !strings.Contains(FirstRunTip, "https://openrouter.ai/") {
		t.Fatalf("first-run tip must link the signup page: %q", FirstRunTip)
	}
	if !strings.Contains(FirstRunTip, "openrouter/free") {
		t.Fatalf("first-run tip must name the default model: %q", FirstRunTip)
	}
}
