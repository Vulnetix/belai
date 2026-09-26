package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

// budgetApp is an App on its own BELAI_HOME, selected on provider p and
// model m, with the given budgets in its effective settings.
func budgetApp(t *testing.T, budgets ...config.TokenBudget) *App {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	t.Cleanup(a.closeBudgets)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	a.cfg.Provider, a.cfg.Model = "p", "m"
	a.cfg.Routing = run.RoutingConfig{}
	a.settings.TokenBudgets = budgets
	if a.budgets == nil {
		t.Fatal("the usage ledger did not open")
	}
	return a
}

func bud(provider, model, scope string, tokens int64) config.TokenBudget {
	return config.TokenBudget{Provider: provider, Model: model, Scope: scope, Tokens: tokens}
}

func systemLines(a *App) []string {
	var out []string
	for _, m := range a.messages {
		if m.Role == "system" {
			out = append(out, m.Content)
		}
	}
	return out
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// R9: the footer gauge shows only under "defined" routing, and only when the
// selected provider+model has a budget.
func TestBudgetRule9_FooterGaugeOnlyForSelectedModelUnderDefined(t *testing.T) {
	a := budgetApp(t, bud("p", "m", config.BudgetScopeSession, 1000))
	a.budgets.Add("p", "m", 250)
	a.refreshFooter()
	g := a.footer.Budget
	if g == nil || g.Scope != "session" || g.TokenPct != 75 || g.TimeLeft != "" {
		t.Fatalf("gauge = %+v, want session 75%% with no time", g)
	}
	a.cfg.Model = "other"
	a.refreshFooter()
	if a.footer.Budget != nil {
		t.Fatal("a model with no budget still shows a gauge")
	}
}

// E10: under "routed" no single model serves the turn, so the gauge hides —
// but usage is still recorded.
func TestBudgetEdge10_RoutedHidesGaugeButRecords(t *testing.T) {
	a := budgetApp(t, bud("p", "m", config.BudgetScopeDay, 1000))
	a.cfg.Routing = run.RoutingConfig{Kind: config.RoutingRouted, Candidates: []run.RoutingCandidate{{Key: "x", Cfg: run.Config{Provider: "q", Model: "n"}}}}
	a.budgets.Add("p", "m", 10)
	a.refreshFooter()
	if a.footer.Budget != nil {
		t.Fatalf("routed footer gauge = %+v, want hidden", a.footer.Budget)
	}
	if got := a.budgets.Used(bud("p", "m", config.BudgetScopeDay, 1)); got != 10 {
		t.Fatalf("routed usage = %d, want 10 recorded", got)
	}
}

// R10: two or more budgets cycle in scope order, one cycle period each.
func TestBudgetRule10_FooterCyclesThroughTheModelsBudgets(t *testing.T) {
	a := budgetApp(t,
		bud("p", "m", config.BudgetScopeMonth, 1000),
		bud("p", "m", config.BudgetScopeSession, 1000),
		bud("p", "m", config.BudgetScopeDay, 1000))
	base := time.Unix(990_000_000, 0) // a multiple of 30 s: the cycle starts here
	var scopes []string
	for i := 0; i < 4; i++ {
		scopes = append(scopes, a.budgetGauge(base.Add(time.Duration(i)*10*time.Second)).Scope)
	}
	if strings.Join(scopes, ",") != "session,day,month,session" {
		t.Fatalf("cycle = %v, want session, day, month, session", scopes)
	}
}

// R11: with warnings on, a call to the selected model prints one line per
// amber or red budget; off, or for another model, nothing is printed.
func TestBudgetRule11_WarningsForTheSelectedModelOnly(t *testing.T) {
	a := budgetApp(t,
		bud("p", "m", config.BudgetScopeSession, 100),
		// 1 token left of a million: amber for all but the month's last minutes.
		bud("p", "m", config.BudgetScopeMonth, 1_000_000),
		bud("p", "m", config.BudgetScopeDay, 1_000_000_000))
	a.budgets.Add("p", "m", 999_999)
	before := len(systemLines(a))

	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Tokens: 1})
	if got := systemLines(a)[before:]; len(got) != 0 {
		t.Fatalf("warnings off printed %q", got)
	}

	on := true
	a.settings.UI = &config.UISettings{BudgetWarn: &on}
	a.handleUsage(run.UsageEvent{Provider: "q", Model: "m", Tokens: 1})
	if got := systemLines(a)[before:]; len(got) != 0 {
		t.Fatalf("a call to another model printed %q", got)
	}

	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Tokens: 1})
	got := systemLines(a)[before:]
	if len(got) != 2 {
		t.Fatalf("warnings = %q, want the red session and the amber month (not the teal day)", got)
	}
	if !strings.Contains(got[0], "budget: session exhausted") || !strings.Contains(got[0], "(p/m)") {
		t.Fatalf("red line = %q", got[0])
	}
	if !strings.Contains(got[1], "budget: month — 1% of tokens left with") {
		t.Fatalf("amber line = %q", got[1])
	}
}

// R2: the budgets screen writes the global settings file even when /settings
// is on the project scope.
func TestBudgetRule2_ScreenWritesGlobalWhateverSettingsScope(t *testing.T) {
	a := budgetApp(t)
	a.settingsState.scope = config.ScopeProject
	if err := a.saveBudget(bud("p", "m", config.BudgetScopeDay, 5000)); err != nil {
		t.Fatal(err)
	}
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if len(g.TokenBudgets) != 1 || g.TokenBudgets[0].Tokens != 5000 {
		t.Fatalf("global budgets = %+v", g.TokenBudgets)
	}
	p, err := config.LoadProject(a.workdir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.TokenBudgets) != 0 {
		t.Fatalf("project budgets = %+v, want none", p.TokenBudgets)
	}
	if len(a.settings.TokenBudgets) != 1 {
		t.Fatal("effective settings not reloaded after the write")
	}
}

// E13: adding a second budget for the same model and scope on the screen is
// refused, pointing at the existing one; editing it changes the allowance.
func TestBudgetEdge13_ScreenRefusesDuplicateAndEditsInPlace(t *testing.T) {
	a := budgetApp(t)
	_ = a.push(viewBudgets)
	add := func(scope, tokens string) {
		a.handleBudgetsKey(key("a"))
		if a.editor.Value() != "p/m" {
			t.Fatalf("add prefill = %q, want the selected p/m", a.editor.Value())
		}
		a.handleBudgetsKey(key("enter"))
		a.handleBudgetsKey(key(scope))
		if tokens != "" {
			a.editor.SetValue(tokens)
			a.handleBudgetsKey(key("enter"))
		}
	}
	add("d", "100k")
	if len(a.settings.TokenBudgets) != 1 || a.settings.TokenBudgets[0].Tokens != 100_000 {
		t.Fatalf("after add = %+v", a.settings.TokenBudgets)
	}
	add("d", "")
	if !strings.Contains(a.budgetsState.errorMsg, "already has a day budget") {
		t.Fatalf("duplicate error = %q", a.budgetsState.errorMsg)
	}
	a.handleBudgetsKey(key("esc"))

	a.budgetsState.selected = 0
	a.handleBudgetsKey(key("enter"))
	a.editor.SetValue("2.5M")
	a.handleBudgetsKey(key("enter"))
	if len(a.settings.TokenBudgets) != 1 || a.settings.TokenBudgets[0].Tokens != 2_500_000 {
		t.Fatalf("after edit = %+v", a.settings.TokenBudgets)
	}
	a.handleBudgetsKey(key("x"))
	if len(a.settings.TokenBudgets) != 0 {
		t.Fatalf("after delete = %+v", a.settings.TokenBudgets)
	}
}

// E14: allowance input accepts suffixes, decimals and separators and rejects
// zero, negatives and non-numbers.
func TestBudgetEdge14_AllowanceInput(t *testing.T) {
	for in, want := range map[string]int64{
		"250000": 250_000, "250k": 250_000, "250K": 250_000, "1.5M": 1_500_000,
		"2b": 2_000_000_000, "1,000,000": 1_000_000, "10_000": 10_000, " 3 m ": 3_000_000,
	} {
		got, err := parseBudgetTokens(in)
		if err != nil || got != want {
			t.Errorf("%q = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "-5k", "abc", "1.2.3M", "k", "NaN"} {
		if _, err := parseBudgetTokens(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

// E9: a budget for a model that is not selected is listed on the screen and
// keeps counting, but never reaches the footer.
func TestBudgetEdge9_OtherModelListedNotInFooter(t *testing.T) {
	a := budgetApp(t, bud("q", "n", config.BudgetScopeDay, 10), bud("p", "m", config.BudgetScopeSession, 10))
	rows := a.budgetRows()
	if len(rows) != 2 || rows[0].Key() != "p/m" || rows[1].Key() != "q/n" {
		t.Fatalf("rows = %+v, want the selected model first, then q/n", rows)
	}
	a.budgets.Add("q", "n", 4)
	if got := a.budgets.Used(rows[1]); got != 4 {
		t.Fatalf("q/n usage = %d, want 4", got)
	}
	a.refreshFooter()
	if g := a.footer.Budget; g == nil || g.Scope != "session" {
		t.Fatalf("footer = %+v, want only p/m's session budget", g)
	}
	if !strings.Contains(a.budgetsView(), "q/n") {
		t.Fatal("screen does not list the other model's budget")
	}
}

// The screen is one letter away in f1 and a submenu row in /settings.
func TestBudgetsScreenReachableFromF1AndSettings(t *testing.T) {
	a := budgetApp(t)
	found := false
	for _, e := range screenEntries {
		if e.key == "b" && e.view == viewBudgets {
			found = true
		}
	}
	if !found {
		t.Fatal("f1 has no b → budgets entry")
	}
	_ = a.push(viewSettings)
	for i, row := range a.settingsRows() {
		if row.key == "token_budgets" {
			if row.kind != "submenu" {
				t.Fatalf("token budgets row kind = %q", row.kind)
			}
			a.settingsState.selected = i
			a.handleSettingsKey(key("enter"))
			if a.view != viewBudgets {
				t.Fatalf("view = %v, want the budgets screen", a.view)
			}
			return
		}
	}
	t.Fatal("/settings has no token budgets row")
}

func TestBudgetFormatting(t *testing.T) {
	for d, want := range map[time.Duration]string{
		30 * time.Second:                 "<1m",
		42 * time.Minute:                 "42m",
		5*time.Hour + 12*time.Minute:     "5h 12m",
		(3*24+4)*time.Hour + time.Minute: "3d 4h",
	} {
		if got := formatTimeLeft(d); got != want {
			t.Errorf("formatTimeLeft(%v) = %q, want %q", d, got, want)
		}
	}
	for n, want := range map[int64]string{950: "950", 12_500: "12.5k", 1_200_000: "1.2M", 3_000_000_000: "3B", 1_999: "1.9k"} {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// R14: the TUI imports earlier sessions' transcripts at start (an Init
// command), so a day budget counts sessions it never recorded live.
func TestBudgetRule14_TUIImportsHistoryAtStart(t *testing.T) {
	a := budgetApp(t, bud("p", "m", config.BudgetScopeDay, 1000))
	dir, err := config.SessionsDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "proj"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"assistant","timestamp":%d,"content":"","meta":{"provider":"p","model":"m","mode":"agent","total_tokens":250}}`, time.Now().UnixMilli())
	if err := os.WriteFile(filepath.Join(dir, "proj", "earlier.jsonl"), []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := a.importHistory()
	if cmd == nil {
		t.Fatal("no import command")
	}
	cmd()
	if got := a.budgets.Used(bud("p", "m", config.BudgetScopeDay, 1)); got != 250 {
		t.Fatalf("day usage after the import = %d, want 250", got)
	}
	a.refreshFooter()
	if g := a.footer.Budget; g == nil || g.TokenPct != 75 {
		t.Fatalf("footer = %+v, want 75%% left", g)
	}
}
