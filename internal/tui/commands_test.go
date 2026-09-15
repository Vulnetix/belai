package tui

import (
	"reflect"
	"testing"

	"github.com/vulnetix/signet/internal/goals"
)

func TestRegistryNames(t *testing.T) {
	r := NewRegistry(t.TempDir())
	want := []string{"code-review", "credentials", "goal", "plan", "profile", "settings", "todos"}
	if !reflect.DeepEqual(r.Names(), want) {
		t.Fatalf("Names = %v, want %v", r.Names(), want)
	}
}

func TestCompleteCommandPrefix(t *testing.T) {
	r := NewRegistry(t.TempDir())

	got := r.Complete("/p")
	want := []string{"/plan", "/profile"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Complete(/p) = %v, want %v", got, want)
	}

	if got := r.Complete("/bogus"); got != nil {
		t.Fatalf("Complete(/bogus) = %v, want nil", got)
	}

	if got := r.Complete("plain text"); got != nil {
		t.Fatalf("Complete(non-slash) = %v, want nil", got)
	}
}

func TestCompleteGoalReplay(t *testing.T) {
	workdir := t.TempDir()
	if _, err := goals.Memorise(workdir, goals.Goal{Name: "alpha", Content: "a"}); err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	if _, err := goals.Memorise(workdir, goals.Goal{Name: "beta", Content: "b"}); err != nil {
		t.Fatalf("Memorise: %v", err)
	}
	r := NewRegistry(workdir)

	got := r.Complete("/goal ")
	want := []string{"/goal alpha", "/goal beta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Complete(/goal ) = %v, want %v", got, want)
	}

	got = r.Complete("/goal al")
	if !reflect.DeepEqual(got, []string{"/goal alpha"}) {
		t.Fatalf("Complete(/goal al) = %v", got)
	}
}

func TestCommandLookup(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if _, ok := r.Command("plan"); !ok {
		t.Fatalf("plan command missing")
	}
	if _, ok := r.Command("nope"); ok {
		t.Fatalf("unexpected command present")
	}
}
