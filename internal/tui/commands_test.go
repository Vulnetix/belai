package tui

import (
	"reflect"
	"testing"

	"github.com/vulnetix/signet/internal/goals"
)

func TestRegistryNames(t *testing.T) {
	r := NewRegistry(t.TempDir())
	want := []string{"clear", "code-review", "compact", "credentials", "goal", "help", "mode", "model", "new", "permissions", "plan", "profile", "rename", "settings", "todos"}
	if !reflect.DeepEqual(r.Names(), want) {
		t.Fatalf("Names = %v, want %v", r.Names(), want)
	}
}

func TestCompleteCommandPrefix(t *testing.T) {
	r := NewRegistry(t.TempDir())

	got := r.Complete("/p")
	want := []string{"/permissions", "/plan", "/profile"}
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

func TestCompleteAlias(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if got := r.Complete("/n"); !reflect.DeepEqual(got, []string{"/new"}) {
		t.Fatalf("Complete(/n) = %v, want [/new]", got)
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

func TestEveryCommandHasHandler(t *testing.T) {
	r := NewRegistry(t.TempDir())
	for _, name := range r.Names() {
		cmd, ok := r.Command(name)
		if !ok {
			t.Fatalf("command %q missing from registry", name)
		}
		if cmd.AliasOf == "" && cmd.Run == nil {
			t.Fatalf("command %q has no handler", name)
		}
		if cmd.AliasOf != "" {
			if _, ok := r.Command(cmd.AliasOf); !ok {
				t.Fatalf("alias %q targets missing command %q", name, cmd.AliasOf)
			}
		}
	}
}

func TestAliasCanonical(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if got := r.Canonical("new"); got != "clear" {
		t.Fatalf("Canonical(new) = %q, want clear", got)
	}
	if got := r.Canonical("provider"); got != "model" {
		t.Fatalf("Canonical(provider) = %q, want model", got)
	}
	if got := r.Canonical("plan"); got != "plan" {
		t.Fatalf("Canonical(plan) = %q, want plan", got)
	}
}

func TestHiddenProviderAbsentFromNames(t *testing.T) {
	r := NewRegistry(t.TempDir())
	for _, n := range r.Names() {
		if n == "provider" {
			t.Fatalf("hidden provider alias must not appear in Names()")
		}
	}
	if got := r.Complete("/prov"); got != nil {
		t.Fatalf("Complete(/prov) = %v, want nil (hidden)", got)
	}
	// It still dispatches.
	if _, ok := r.Command(r.Canonical("provider")); !ok {
		t.Fatalf("provider alias should dispatch to a real command")
	}
}
