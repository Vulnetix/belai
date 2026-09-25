package config

import "testing"

// Every Settings accessor encodes a default that docs/architecture.md states
// as a business rule. They are cheap to get backwards — most read "nil means
// on", but three read "nil means off" — so each one is pinned here in all
// three states: unset, explicit true, explicit false.
func TestSettingsAccessorDefaults(t *testing.T) {
	tr, fa := true, false

	tests := []struct {
		name             string
		get              func(Settings) bool
		set              func(*Settings, *bool)
		defaultWhenUnset bool
	}{
		{
			"colors", Settings.ColorsEnabled,
			func(s *Settings, v *bool) { s.UI = &UISettings{Colors: v} }, true,
		},
		{
			"spinner", Settings.SpinnerEnabled,
			func(s *Settings, v *bool) { s.UI = &UISettings{Spinner: v} }, true,
		},
		{
			"show_reasoning", Settings.ReasoningVisible,
			func(s *Settings, v *bool) { s.UI = &UISettings{ShowReasoning: v} }, false,
		},
		{
			"show_tool_calls", Settings.ToolCallsVisible,
			func(s *Settings, v *bool) { s.UI = &UISettings{ShowToolCalls: v} }, true,
		},
		{
			"show_edits", Settings.EditsVisible,
			func(s *Settings, v *bool) { s.UI = &UISettings{ShowEdits: v} }, true,
		},
		{
			"show_todos", Settings.TodosVisible,
			func(s *Settings, v *bool) { s.UI = &UISettings{ShowTodos: v} }, true,
		},
		{
			"mouse", Settings.MouseEnabled,
			func(s *Settings, v *bool) { s.UI = &UISettings{Mouse: v} }, true,
		},
		{
			"show_session_names", Settings.SessionNamesVisible,
			func(s *Settings, v *bool) { s.ShowSessionNames = v }, true,
		},
		{
			"guardrails", Settings.GuardrailsEnabled,
			func(s *Settings, v *bool) { s.Guardrails = v }, true,
		},
		{
			"ask_permission", Settings.AskPermissionEnabled,
			func(s *Settings, v *bool) { s.AskPermission = v }, true,
		},
		{
			"caveman", Settings.CavemanEnabled,
			func(s *Settings, v *bool) { s.Caveman = v }, false,
		},
		{
			"read_only", Settings.ReadOnlyEnabled,
			func(s *Settings, v *bool) { s.ReadOnly = v }, false,
		},
		{
			"auto_commit_per_task", Settings.AutoCommitPerTaskEnabled,
			func(s *Settings, v *bool) { s.AutoCommitPerTask = v }, false,
		},
		{
			"allow_project_providers", Settings.AllowProjectProvidersEnabled,
			func(s *Settings, v *bool) { s.AllowProjectProviders = v }, false,
		},
		{
			"lsp_enabled", Settings.LSPEnabled,
			func(s *Settings, v *bool) {
				if s.LSP == nil {
					s.LSP = &LSPSettings{}
				}
				s.LSP.Enabled = v
			}, true,
		},
		{
			"lsp_fallback", Settings.LSPFallbackEnabled,
			func(s *Settings, v *bool) {
				if s.LSP == nil {
					s.LSP = &LSPSettings{}
				}
				s.LSP.Fallback = v
			}, true,
		},
		{
			"lsp_classify_diagnostics", Settings.LSPClassifyDiagnostics,
			func(s *Settings, v *bool) {
				if s.LSP == nil {
					s.LSP = &LSPSettings{}
				}
				s.LSP.ClassifyDiagnostics = v
			}, false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var unset Settings
			if got := tc.get(unset); got != tc.defaultWhenUnset {
				t.Errorf("unset %s = %v, want %v", tc.name, got, tc.defaultWhenUnset)
			}

			var on Settings
			tc.set(&on, &tr)
			if !tc.get(on) {
				t.Errorf("explicit true %s must read as on", tc.name)
			}

			var off Settings
			tc.set(&off, &fa)
			if tc.get(off) {
				t.Errorf("explicit false %s must read as off", tc.name)
			}

			// A UI block that exists but leaves the key nil must fall back to
			// the same default as no UI block at all.
			var nilKey Settings
			tc.set(&nilKey, nil)
			if got := tc.get(nilKey); got != tc.defaultWhenUnset {
				t.Errorf("nil %s inside a populated block = %v, want %v", tc.name, got, tc.defaultWhenUnset)
			}
		})
	}
}

// Session retention is a count, not a toggle: unset means 28 days, and an
// explicit value is honoured verbatim — including zero, which is a real
// instruction to keep nothing, not an absent value.
func TestSessionRetentionDefault(t *testing.T) {
	var unset Settings
	if got := unset.SessionRetention(); got != 28 {
		t.Fatalf("unset retention = %d, want 28", got)
	}
	for _, want := range []int{0, 1, 365} {
		v := want
		s := Settings{SessionRetentionDays: &v}
		if got := s.SessionRetention(); got != want {
			t.Errorf("retention = %d, want %d", got, want)
		}
	}
}

// The resilience budgets all read "zero means use the caller's default", so a
// nil block and a zeroed block must behave identically. The callers' defaults
// are 3 attempts, 10 tool iterations, 3 clarify rounds and 8 explore
// iterations; max_passes is the odd one out, where zero is a real value
// meaning unbounded.
func TestResilienceBudgetDefaults(t *testing.T) {
	var nilBlock *ResilienceSettings
	zeroBlock := &ResilienceSettings{}

	for _, r := range []*ResilienceSettings{nilBlock, zeroBlock} {
		if got := r.MaxAttemptsOr(3); got != 3 {
			t.Errorf("MaxAttemptsOr(3) = %d, want 3", got)
		}
		if got := r.MaxIterationsOr(10); got != 10 {
			t.Errorf("MaxIterationsOr(10) = %d, want 10", got)
		}
		if got := r.MaxClarifyRoundsOr(3); got != 3 {
			t.Errorf("MaxClarifyRoundsOr(3) = %d, want 3", got)
		}
		if got := r.MaxExploreIterationsOr(8); got != 8 {
			t.Errorf("MaxExploreIterationsOr(8) = %d, want 8", got)
		}
		if got := r.MaxPassesOr(); got != 0 {
			t.Errorf("MaxPassesOr() = %d, want 0 (unbounded)", got)
		}
	}

	set := &ResilienceSettings{
		MaxAttempts:          5,
		MaxIterations:        20,
		MaxPasses:            7,
		MaxClarifyRounds:     1,
		MaxExploreIterations: 2,
	}
	if got := set.MaxAttemptsOr(3); got != 5 {
		t.Errorf("MaxAttemptsOr = %d, want 5", got)
	}
	if got := set.MaxIterationsOr(10); got != 20 {
		t.Errorf("MaxIterationsOr = %d, want 20", got)
	}
	if got := set.MaxPassesOr(); got != 7 {
		t.Errorf("MaxPassesOr = %d, want 7", got)
	}
	if got := set.MaxClarifyRoundsOr(3); got != 1 {
		t.Errorf("MaxClarifyRoundsOr = %d, want 1", got)
	}
	if got := set.MaxExploreIterationsOr(8); got != 2 {
		t.Errorf("MaxExploreIterationsOr = %d, want 2", got)
	}
}

// A negative max_clarify_rounds is the documented way to disable clarification
// entirely. The accessor passes it through unchanged — the sign is the signal,
// so it must not be clamped to the default here.
func TestNegativeClarifyRoundsSurvivesTheAccessor(t *testing.T) {
	r := &ResilienceSettings{MaxClarifyRounds: -1}
	if got := r.MaxClarifyRoundsOr(3); got != -1 {
		t.Fatalf("MaxClarifyRoundsOr = %d, want -1 so the caller can disable clarification", got)
	}
}

// The two safety gates may only be tightened by the project layer. Everything
// else in the merge takes the project value outright, so the asymmetry is
// worth pinning on its own.
func TestGuardrailsAndAskTightenOnly(t *testing.T) {
	on, off := true, false

	// Project true tightens a global that was off.
	tightened := Settings{Guardrails: &off, AskPermission: &off}.
		Override(Settings{Guardrails: &on, AskPermission: &on})
	if !tightened.GuardrailsEnabled() || !tightened.AskPermissionEnabled() {
		t.Fatal("project true must turn the gates back on")
	}

	// Project false may not loosen a global that was on.
	kept := Settings{Guardrails: &on, AskPermission: &on}.
		Override(Settings{Guardrails: &off, AskPermission: &off})
	if !kept.GuardrailsEnabled() || !kept.AskPermissionEnabled() {
		t.Fatal("project false must not loosen the gates")
	}

	// Project false may not loosen the default either, which is on.
	fromDefault := Settings{}.Override(Settings{Guardrails: &off, AskPermission: &off})
	if !fromDefault.GuardrailsEnabled() || !fromDefault.AskPermissionEnabled() {
		t.Fatal("project false must not loosen the default-on gates")
	}

	// caveman is not a safety gate: the project layer sets it either way.
	cav := Settings{Caveman: &on}.Override(Settings{Caveman: &off})
	if cav.CavemanEnabled() {
		t.Fatal("caveman is not tighten-only; the project layer must be able to turn it off")
	}
}

// A project file may disable clarification with a negative value: the merge
// takes the minimum, so negative always wins over any global budget. That is a
// tightening, which the project layer is allowed to do.
func TestProjectCanDisableClarification(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: 3}}
	merged := global.Override(Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: -1}})
	if got := merged.Resilience.MaxClarifyRoundsOr(3); got != -1 {
		t.Fatalf("merged clarify rounds = %d, want -1", got)
	}

	// With no global block at all the project value is taken outright.
	fresh := Settings{}.Override(Settings{Resilience: &ResilienceSettings{MaxClarifyRounds: -1}})
	if got := fresh.Resilience.MaxClarifyRoundsOr(3); got != -1 {
		t.Fatalf("clarify rounds with no global block = %d, want -1", got)
	}
}

// A project file cannot widen any budget: every field takes the minimum of the
// two when both are set. The table is the guard against a new budget being
// added to the struct with the merge step forgotten.
func TestProjectCannotWidenAnyResilienceBudget(t *testing.T) {
	global := Settings{Resilience: &ResilienceSettings{
		MaxAttempts:          2,
		MaxIterations:        5,
		MaxPasses:            4,
		MaxClarifyRounds:     1,
		MaxExploreIterations: 3,
	}}
	merged := global.Override(Settings{Resilience: &ResilienceSettings{
		MaxAttempts:          99,
		MaxIterations:        99,
		MaxPasses:            99,
		MaxClarifyRounds:     99,
		MaxExploreIterations: 99,
	}})

	r := merged.Resilience
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"max_attempts", r.MaxAttemptsOr(3), 2},
		{"max_iterations", r.MaxIterationsOr(10), 5},
		{"max_passes", r.MaxPassesOr(), 4},
		{"max_clarify_rounds", r.MaxClarifyRoundsOr(3), 1},
		{"max_explore_iterations", r.MaxExploreIterationsOr(8), 3},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want the global (lower) value %d", tc.name, tc.got, tc.want)
		}
	}
}
