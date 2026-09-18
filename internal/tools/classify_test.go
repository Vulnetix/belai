package tools

import "testing"

// Bash is the only kind that reaches the classifier. Every other kind has a
// fixed argument shape, so the harness knows what command produced the
// output; Bash takes an arbitrary command string and does not.
func TestOnlyBashNeedsTheClassifier(t *testing.T) {
	for _, k := range AllKinds {
		want := k == KindBash
		if got := k.NeedsClassifier(); got != want {
			t.Errorf("Kind(%q).NeedsClassifier() = %v, want %v", k, got, want)
		}
	}
}

// The set is closed over AllKinds: a kind added to the harness without being
// considered here defaults to sanitise-only, which is the cheap answer, so
// this test is what forces the decision to be made deliberately.
func TestClassifierKindsAreAllRegisteredKinds(t *testing.T) {
	known := map[Kind]bool{}
	for _, k := range AllKinds {
		known[k] = true
	}
	for k := range classifierKinds {
		if !known[k] {
			t.Errorf("classifierKinds contains %q, which is not in AllKinds", k)
		}
	}
}

// An unregistered kind is sanitise-only rather than a panic.
func TestUnknownKindDoesNotNeedTheClassifier(t *testing.T) {
	if Kind("nonsense").NeedsClassifier() {
		t.Fatal("an unknown kind asked for the classifier")
	}
}
