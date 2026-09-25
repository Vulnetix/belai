package config

import (
	"strings"
	"testing"
	"time"
)

func tb(provider, model, scope string, tokens int64) TokenBudget {
	return TokenBudget{Provider: provider, Model: model, Scope: scope, Tokens: tokens}
}

// R1: provider, model, a known scope and a positive allowance are required.
func TestBudgetRule1_ValidateTokenBudgets(t *testing.T) {
	ok := Settings{TokenBudgets: []TokenBudget{
		tb("openrouter", "a/b", BudgetScopeSession, 1),
		tb("openrouter", "a/b", BudgetScopeDay, 2),
		tb("openrouter", "a/b", BudgetScopeMonth, 3),
	}}
	if err := ValidateTokenBudgets(ok); err != nil {
		t.Fatalf("three scopes for one model: %v", err)
	}
	for name, b := range map[string]TokenBudget{
		"no provider": tb("", "m", BudgetScopeDay, 1),
		"no model":    tb("p", " ", BudgetScopeDay, 1),
		"bad scope":   tb("p", "m", "week", 1),
		"zero":        tb("p", "m", BudgetScopeDay, 0),
		"negative":    tb("p", "m", BudgetScopeDay, -5),
	} {
		if err := ValidateTokenBudgets(Settings{TokenBudgets: []TokenBudget{b}}); err == nil {
			t.Errorf("%s: accepted %+v", name, b)
		}
	}
}

// R1: an invalid budget fails resolution as a whole.
func TestBudgetRule1_InvalidBudgetFailsResolve(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := SaveGlobal(Settings{TokenBudgets: []TokenBudget{tb("p", "m", "fortnight", 10)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(t.TempDir(), func(string) string { return "" }, Settings{}); err == nil {
		t.Fatal("Resolve accepted an invalid budget")
	}
}

// E13: a second budget for the same provider, model and scope is rejected.
func TestBudgetEdge13_DuplicateBudgetRejected(t *testing.T) {
	s := Settings{TokenBudgets: []TokenBudget{tb("p", "m", BudgetScopeDay, 1), tb("p", "m", BudgetScopeDay, 2)}}
	err := ValidateTokenBudgets(s)
	if err == nil || !strings.Contains(err.Error(), "already set") {
		t.Fatalf("duplicate = %v, want an already-set error", err)
	}
}

// R2: budgets are global; a project layer's are ignored with a note.
func TestBudgetRule2_ProjectBudgetsIgnored(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	global := []TokenBudget{tb("p", "m", BudgetScopeDay, 1000)}
	if err := SaveGlobal(Settings{TokenBudgets: global}); err != nil {
		t.Fatal(err)
	}
	if err := SaveProject(workdir, Settings{TokenBudgets: []TokenBudget{tb("p", "m", BudgetScopeDay, 999_999_999)}}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Settings.TokenBudgets) != 1 || eff.Settings.TokenBudgets[0].Tokens != 1000 {
		t.Fatalf("budgets = %+v, want only the global 1000", eff.Settings.TokenBudgets)
	}
	if eff.Origin["token_budgets"] != SourceGlobal {
		t.Fatalf("origin = %q, want global", eff.Origin["token_budgets"])
	}
	found := false
	for _, n := range eff.Notes {
		found = found || strings.Contains(n, "token_budgets ignored")
	}
	if !found {
		t.Fatalf("notes = %v, want the ignore note", eff.Notes)
	}
}

// R10: a model's budgets come back in scope order, which is the cycle order.
func TestBudgetRule10_BudgetsForScopeOrder(t *testing.T) {
	s := Settings{TokenBudgets: []TokenBudget{
		tb("p", "m", BudgetScopeMonth, 3),
		tb("q", "m", BudgetScopeDay, 9),
		tb("p", "m", BudgetScopeSession, 1),
		tb("p", "m", BudgetScopeDay, 2),
	}}
	got := s.BudgetsFor("p", "m")
	if len(got) != 3 || got[0].Scope != BudgetScopeSession || got[1].Scope != BudgetScopeDay || got[2].Scope != BudgetScopeMonth {
		t.Fatalf("BudgetsFor = %+v, want session, day, month for p/m only", got)
	}
	if len(s.BudgetsFor("none", "m")) != 0 {
		t.Fatal("a model with no budgets returned some")
	}
}

// E12: the cycle defaults to 10 s, and is never shorter than 2 s.
func TestBudgetEdge12_CycleDefaultAndClamp(t *testing.T) {
	for _, tc := range []struct {
		set  *int
		want time.Duration
	}{
		{nil, 10 * time.Second},
		{intPtr(0), 10 * time.Second},
		{intPtr(-3), 10 * time.Second},
		{intPtr(1), 2 * time.Second},
		{intPtr(2), 2 * time.Second},
		{intPtr(30), 30 * time.Second},
	} {
		s := Settings{UI: &UISettings{BudgetCycleSeconds: tc.set}}
		if got := s.BudgetCycle(); got != tc.want {
			t.Errorf("cycle %v = %v, want %v", tc.set, got, tc.want)
		}
	}
	if (Settings{}).BudgetCycle() != 10*time.Second {
		t.Error("no UI settings must give the default")
	}
}

// R11: budget warnings are off unless turned on.
func TestBudgetRule11_WarningsOffByDefault(t *testing.T) {
	if (Settings{}).BudgetWarnEnabled() {
		t.Fatal("warnings on by default")
	}
	on := true
	if !(Settings{UI: &UISettings{BudgetWarn: &on}}).BudgetWarnEnabled() {
		t.Fatal("warnings not on when set")
	}
	// The UI merge carries both keys from a later layer.
	u := UISettings{}
	n := 5
	u.merge(&UISettings{BudgetCycleSeconds: &n, BudgetWarn: &on})
	if u.BudgetCycleSeconds == nil || *u.BudgetCycleSeconds != 5 || u.BudgetWarn == nil || !*u.BudgetWarn {
		t.Fatalf("merge = %+v", u)
	}
}

func intPtr(n int) *int { return &n }
