package tui

import (
	"reflect"
	"slices"
	"testing"
)

func TestRegistryNames(t *testing.T) {
	r := NewRegistry(t.TempDir())
	want := []string{"add-dir", "agent", "agents", "budgets", "clear", "compact", "execute", "exit", "help", "lsp", "mode", "model", "new", "permissions", "process", "processes", "profile", "prompts", "providers", "quit", "refine", "rename", "resume", "settings", "todos", "vulnetix", "yolo"}
	if !reflect.DeepEqual(r.Names(), want) {
		t.Fatalf("Names = %v, want %v", r.Names(), want)
	}
}

func TestCompleteCommandFuzzy(t *testing.T) {
	r := NewRegistry(t.TempDir())

	// Prefix matches rank first, in name order; scattered subsequence
	// matches follow.
	got := r.Complete("/p")
	wantHead := []string{"/permissions", "/process", "/processes", "/profile", "/prompts", "/providers"}
	if len(got) < len(wantHead) || !reflect.DeepEqual(got[:len(wantHead)], wantHead) {
		t.Fatalf("Complete(/p) = %v, want it to start with %v", got, wantHead)
	}
	if !slices.Contains(got, "/help") {
		t.Fatalf("Complete(/p) = %v, want the non-prefix /help too", got)
	}

	if got := r.Complete("/pmt"); len(got) == 0 || got[0] != "/prompts" {
		t.Fatalf("Complete(/pmt) = %v, want /prompts first", got)
	}
	if got := r.Complete("/dir"); len(got) == 0 || got[0] != "/add-dir" {
		t.Fatalf("Complete(/dir) = %v, want /add-dir first", got)
	}
	if got := r.Complete("/"); !reflect.DeepEqual(got[0], "/add-dir") || len(got) != len(r.Names()) {
		t.Fatalf("Complete(/) = %v, want every command in name order", got)
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
	if got := r.Complete("/n"); len(got) == 0 || got[0] != "/new" {
		t.Fatalf("Complete(/n) = %v, want /new first", got)
	}
}

func TestCompleteArgFuzzy(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if got := r.Complete("/agent stp"); !reflect.DeepEqual(got, []string{"/agent stop"}) {
		t.Fatalf("Complete(/agent stp) = %v, want [/agent stop]", got)
	}
}

func TestCompleteAgentSubcommands(t *testing.T) {
	r := NewRegistry(t.TempDir())
	got := r.Complete("/agent ")
	want := []string{"/agent create", "/agent edit", "/agent list", "/agent log", "/agent pause", "/agent resume", "/agent start", "/agent stop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Complete(/agent ) = %v, want %v", got, want)
	}
}

func TestCommandLookup(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if _, ok := r.Command("mode"); !ok {
		t.Fatalf("mode command missing")
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

func TestProvidersCompletionIncludesReport(t *testing.T) {
	r := NewRegistry(t.TempDir())
	got := r.Complete("/providers ")
	want := []string{"/providers download", "/providers launch", "/providers report", "/providers status", "/providers stop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Complete(/providers ) = %v, want %v", got, want)
	}
}

func TestAliasCanonical(t *testing.T) {
	r := NewRegistry(t.TempDir())
	if got := r.Canonical("new"); got != "clear" {
		t.Fatalf("Canonical(new) = %q, want clear", got)
	}
	if got := r.Canonical("provider"); got != "providers" {
		t.Fatalf("Canonical(provider) = %q, want providers", got)
	}
	if got := r.Canonical("agent"); got != "agent" {
		t.Fatalf("Canonical(agent) = %q, want agent", got)
	}
}

func TestHiddenProviderAbsentFromNames(t *testing.T) {
	r := NewRegistry(t.TempDir())
	for _, n := range r.Names() {
		if n == "provider" {
			t.Fatalf("hidden provider alias must not appear in Names()")
		}
	}
	// It still dispatches.
	if _, ok := r.Command(r.Canonical("provider")); !ok {
		t.Fatalf("provider alias should dispatch to a real command")
	}
}
