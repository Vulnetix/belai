package goals

import (
	"reflect"
	"testing"
)

func TestGoalMemoriseLoadRoundTrip(t *testing.T) {
	workdir := t.TempDir()
	g := Goal{Name: "ship-signet", Content: "build and release signet"}

	path, err := Memorise(workdir, g)
	if err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	if path == "" {
		t.Fatalf("Memorise returned empty path")
	}

	got, err := Load(workdir, "ship-signet")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != "ship-signet" || got.Content != g.Content {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestGoalNamesForAutocomplete(t *testing.T) {
	workdir := t.TempDir()
	for _, n := range []string{"beta", "alpha"} {
		if _, err := Memorise(workdir, Goal{Name: n, Content: n}); err != nil {
			t.Fatalf("Memorise(%s): %v", n, err)
		}
	}
	names, err := Names(workdir)
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "beta"}) {
		t.Fatalf("names = %v, want [alpha beta]", names)
	}
}

func TestGoalNamesEmpty(t *testing.T) {
	names, err := Names(t.TempDir())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected no goals, got %v", names)
	}
}
