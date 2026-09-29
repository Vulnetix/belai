package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
)

func keyF12() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyF12} }

func panelText(a *App) string { return ansi.Strip(a.renderRunsPanel()) }

// observeFiveHour records a five-hour plan limit for provider p.
func observeFiveHour(a *App, used float64) {
	now := time.Now()
	a.budgets.ObserveLimits([]budget.PlanLimit{{Provider: "p", Window: budget.WindowFiveHour, Used: used, Reset: now.Add(3 * time.Hour), ObservedAt: now}})
}

// R15: the intel slot is always in the cycle: with no budget, or under routed,
// it is the only slot and the footer shows it at every moment.
func TestBudgetRule15_IntelIsTheOnlySlotWithoutBudgetsAndWhenRouted(t *testing.T) {
	a := intelApp(t)
	base := time.Unix(990_000_000, 0)
	for i := 0; i < 6; i++ {
		now := base.Add(time.Duration(i) * 7 * time.Second)
		if a.budgetGauge(now) != nil || a.intelGauge(now) == nil {
			t.Fatalf("no budgets at +%ds: budget %v intel %v, want intel alone", i*7, a.budgetGauge(now), a.intelGauge(now))
		}
	}
	a.refreshFooter()
	if a.footer.Budget != nil || a.footer.Intel == nil {
		t.Fatalf("footer = budget %v intel %v, want only intel", a.footer.Budget, a.footer.Intel)
	}

	// Routed: no single model serves the turn, so no budget slot exists even
	// though the model has budgets, and intel holds the line.
	r := intelApp(t, bud("p", "m", config.BudgetScopeDay, 1000), bud("p", "m", config.BudgetScopeMonth, 1000))
	r.cfg.Routing = run.RoutingConfig{Kind: config.RoutingRouted, Candidates: []run.RoutingCandidate{{Key: "x", Cfg: run.Config{Provider: "q", Model: "n"}}}}
	for i := 0; i < 6; i++ {
		now := base.Add(time.Duration(i) * 7 * time.Second)
		if r.budgetGauge(now) != nil || r.intelGauge(now) == nil {
			t.Fatalf("routed at +%ds: want intel alone", i*7)
		}
	}
}

// R15: the intel slot shows today's tokens, the tightest plan limit when the
// provider reported one, and the pace otherwise.
func TestBudgetRule15_IntelGaugeCarriesTodayAndTheTightestLimit(t *testing.T) {
	a := intelApp(t)
	a.budgets.AddCall("p", "m", "agent", 34_400_000)
	now := time.Now()
	g := a.intelGauge(now)
	if g == nil || g.Today != "34.4M" || g.Limit != "" || g.LimitFrac != -1 || g.Pace == "" {
		t.Fatalf("gauge without a limit = %+v, want today 34.4M, no limit, a pace", g)
	}

	observeFiveHour(a, 0.23)
	g = a.intelGauge(now)
	if g == nil || g.Limit != "5h 23%" || g.LimitFrac != 0.23 || g.ElapsedFrac < 0 || g.ElapsedFrac > 1 {
		t.Fatalf("gauge with a limit = %+v, want 5h 23%% with an elapsed share", g)
	}
	// The window has 3 h left of 5, so 40 % of it has passed.
	if g.ElapsedFrac < 0.39 || g.ElapsedFrac > 0.41 {
		t.Fatalf("elapsed = %v, want about 0.4", g.ElapsedFrac)
	}
}

// R17: the hint shows in the last third of the cycle period, following the clock
// like the cycle, and never otherwise.
func TestBudgetRule17_HintInTheLastThirdOfTheCyclePeriod(t *testing.T) {
	a := intelApp(t)
	base := time.Unix(990_000_000, 0) // a multiple of 10 s
	for offset, want := range map[time.Duration]bool{
		0: false, 3 * time.Second: false, 6 * time.Second: false, 6500 * time.Millisecond: false,
		6700 * time.Millisecond: true, 8 * time.Second: true, 9900 * time.Millisecond: true, 10 * time.Second: false,
	} {
		if got := a.intelHint(base.Add(offset)); got != want {
			t.Fatalf("hint at +%v = %v, want %v", offset, got, want)
		}
		if g := a.intelGauge(base.Add(offset)); g == nil || g.Hint != want {
			t.Fatalf("gauge hint at +%v = %+v, want %v", offset, g, want)
		}
	}
	// A different period moves the boundary with it.
	nine := 9
	a.settings.UI = &config.UISettings{BudgetCycleSeconds: &nine}
	nineBase := time.Unix(990_000_000-990_000_000%9, 0)
	if a.intelHint(nineBase.Add(5*time.Second)) || !a.intelHint(nineBase.Add(6*time.Second)) {
		t.Fatal("the hint boundary did not follow a 9 s cycle")
	}
}

// E26: ui.intel off removes the slot from the cycle, the intel tab, the f12 key
// and the /intel screen. Budgets keep their own cycle.
func TestBudgetEdge26_IntelOffRemovesTheSlotTheTabAndTheKey(t *testing.T) {
	a := intelApp(t)
	off := false
	a.settings.UI = &config.UISettings{Intel: &off}
	now := time.Now()
	if a.intelGauge(now) != nil || a.budgetGauge(now) != nil {
		t.Fatal("with ui.intel off and no budgets the footer still shows a gauge")
	}
	a.refreshFooter()
	if a.footer.Intel != nil || a.footer.Budget != nil {
		t.Fatalf("footer = %v / %v, want none", a.footer.Budget, a.footer.Intel)
	}
	if a.runsTabVisible(tabIntel) {
		t.Fatal("the intel tab is still offered")
	}
	for _, n := range a.runsTabNames() {
		if n == "intel" {
			t.Fatalf("tab names = %v", a.runsTabNames())
		}
	}
	a.Update(keyF12())
	if a.runsOpen {
		t.Fatal("f12 opened the panel with ui.intel off")
	}
	before := len(systemLines(a))
	a.handleCommand("/intel")
	if a.view == viewIntel || len(systemLines(a)) != before+1 || !strings.Contains(systemLines(a)[before], "ui.intel is false") {
		t.Fatalf("/intel with ui.intel off: view %v, notes %q", a.view, systemLines(a)[before:])
	}

	// A budget still cycles alone.
	b := budgetApp(t, bud("p", "m", config.BudgetScopeDay, 1000))
	for i := 0; i < 5; i++ {
		if b.budgetGauge(time.Unix(990_000_000+int64(i)*10, 0)) == nil || b.intelGauge(time.Unix(990_000_000+int64(i)*10, 0)) != nil {
			t.Fatal("with ui.intel off the budget must hold the slot at every moment")
		}
	}
}

// R21: f12 opens the runs panel focused on the intel tab, and f12 again closes
// it; with the panel open on another tab f12 moves to the intel tab instead.
func TestBudgetRule21_F12TogglesTheIntelTab(t *testing.T) {
	a := intelApp(t)
	a.budgets.AddCall("p", "m", "agent", 1500)
	observeFiveHour(a, 0.23)

	a.Update(keyF12())
	if !a.runsOpen || !a.runsFocus || a.runsTab != tabIntel {
		t.Fatalf("after f12: open=%v focus=%v tab=%d, want the panel open on intel", a.runsOpen, a.runsFocus, a.runsTab)
	}
	text := panelText(a)
	for _, want := range []string{"[ intel ]", "f12 close", "p · m", "today 1.5k", "5-hour limit", "23%", "resets in", "pace", "trend", "runway", "today", "this week", "last 30 days"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the intel pane lacks %q:\n%s", want, text)
		}
	}
	a.Update(keyF12())
	if a.runsOpen {
		t.Fatal("a second f12 did not close the panel")
	}

	// f9 opens the activity tab; f12 then switches to intel without closing.
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if !a.runsOpen || a.runsTab != tabActivity {
		t.Fatalf("f9: open=%v tab=%d", a.runsOpen, a.runsTab)
	}
	a.Update(keyF12())
	if !a.runsOpen || a.runsTab != tabIntel || !a.runsFocus {
		t.Fatalf("f12 over the activity tab: open=%v tab=%d focus=%v", a.runsOpen, a.runsTab, a.runsFocus)
	}
	// Inside the pane f12 closes it too, and f9 as well.
	a.Update(keyF12())
	if a.runsOpen {
		t.Fatal("f12 in the focused pane did not close it")
	}
	a.Update(keyF12())
	a.Update(tea.KeyMsg{Type: tea.KeyF9})
	if a.runsOpen {
		t.Fatal("f9 in the intel pane did not close it")
	}
}

// R21: without a reported limit the pane says so and points at budgets.
func TestBudgetRule21_PaneWithoutALimitSaysSo(t *testing.T) {
	a := intelApp(t)
	a.Update(keyF12())
	text := panelText(a)
	if !strings.Contains(text, "no plan limit reported by p") || strings.Contains(text, "5-hour limit") {
		t.Fatalf("pane = \n%s", text)
	}
	if !strings.Contains(text, "runway") || !strings.Contains(text, "no limit set") || !strings.Contains(text, "idle") {
		t.Fatalf("pane = \n%s\nwant an idle pace and no limit set", text)
	}
	if !strings.Contains(text, "no 7-day baseline yet") {
		t.Fatalf("pane = \n%s\nwant no trend baseline on an empty ledger", text)
	}
}

// R21: the pane's keys: left and right move the window, m lists models, r the
// session's roles, t returns to the timeline, b opens budgets and enter opens
// the full screen, from which esc comes back.
func TestBudgetRule21_PaneKeys(t *testing.T) {
	a := intelApp(t)
	a.budgets.AddCall("p", "m", "agent", 900)
	a.budgets.AddCall("q", "other", "security", 100)
	a.Update(keyF12())

	a.Update(key("m"))
	text := panelText(a)
	if a.intelState.mode != intelModeModels || !strings.Contains(text, "models · today · 1k") || !strings.Contains(text, "p/m") || !strings.Contains(text, "q/other") {
		t.Fatalf("models mode = \n%s", text)
	}
	if strings.Index(text, "p/m") > strings.Index(text, "q/other") {
		t.Fatalf("models are not largest first:\n%s", text)
	}
	if !strings.Contains(text, "90%") || !strings.Contains(text, "10%") {
		t.Fatalf("models lack their shares:\n%s", text)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.intelState.window != 1 || !strings.Contains(panelText(a), "models · this week") {
		t.Fatalf("right: window %d", a.intelState.window)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.intelState.window != 0 {
		t.Fatalf("three rights = window %d, want it to wrap to today", a.intelState.window)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if a.intelState.window != 2 {
		t.Fatalf("left from today = window %d, want it to wrap to 30 days", a.intelState.window)
	}

	a.Update(key("r"))
	text = panelText(a)
	if a.intelState.mode != intelModeRoles || !strings.Contains(text, "roles · this session · 1k") || !strings.Contains(text, "agent") || !strings.Contains(text, "security") {
		t.Fatalf("roles mode = \n%s", text)
	}
	a.Update(key("t"))
	if a.intelState.mode != intelModeTimeline || !strings.Contains(panelText(a), "last 30 days") {
		t.Fatalf("t did not return to the timeline: mode %q", a.intelState.mode)
	}

	a.Update(key("b"))
	if a.view != viewBudgets {
		t.Fatalf("b opened view %v, want the budgets screen", a.view)
	}
	a.popToChat()
	if !a.runsOpen {
		a.Update(keyF12())
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.view != viewIntel {
		t.Fatalf("enter opened view %v, want session intelligence", a.view)
	}
	a.Update(key("esc"))
	if a.view != viewChat {
		t.Fatalf("esc left view %v, want chat", a.view)
	}
}

// R21: the panel height the chat layout reserves equals what the panel draws, on
// a tall and a short terminal, and the intel tab takes half the height.
func TestBudgetRule21_PanelHeightMatchesWhatIsDrawn(t *testing.T) {
	for _, h := range []int{40, 24, 14, 10} {
		a := intelApp(t)
		a.budgets.AddCall("p", "m", "agent", 100)
		observeFiveHour(a, 0.4)
		a.Update(tea.WindowSizeMsg{Width: 120, Height: h})
		a.Update(keyF12())
		got := strings.Count(a.renderRunsPanel(), "\n")
		if got != a.runsPanelHeight() {
			t.Fatalf("height %d: drew %d lines, reserved %d", h, got, a.runsPanelHeight())
		}
		if limit := h/2 + 1; got > limit {
			t.Fatalf("height %d: the pane drew %d lines, want at most about half (%d)", h, got, limit)
		}
	}
}

// E25: on a short terminal the pane drops header lines from the bottom and keeps
// at least one list row; the tab bar and help line always draw.
func TestBudgetEdge25_ShortTerminalKeepsTheTabBarAndOneRow(t *testing.T) {
	a := intelApp(t)
	observeFiveHour(a, 0.4)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 12})
	a.Update(keyF12())
	header, rows := a.intelPanelLayout(120)
	if len(header) == 0 || rows < 1 || len(header)+rows > 12/2-3+len(header) {
		t.Fatalf("layout on 12 rows = %d header lines, %d list rows", len(header), rows)
	}
	text := panelText(a)
	if !strings.Contains(text, "[ intel ]") || !strings.Contains(text, "f12") {
		t.Fatalf("short pane = \n%s", text)
	}
	full, _ := intelApp(t).intelPanelLayout(120)
	if len(full) <= len(header) {
		t.Fatalf("a short terminal kept %d header lines, a tall one %d; want fewer", len(header), len(full))
	}
}

// R21: the full screen shows the limits, pace, the seven-day heatmap, the model
// table and what this session's agent calls sent, and its keys move the window
// and the list.
func TestBudgetRule21_FullScreenShowsEveryPanel(t *testing.T) {
	a := intelApp(t, bud("p", "m", config.BudgetScopeDay, 100_000_000))
	a.budgets.AddCall("p", "m", "agent", 2_000_000)
	a.budgets.AddCall("q", "other", "agent", 500_000)
	observeFiveHour(a, 0.23)
	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Tokens: 10, Role: run.RoleAgent,
		Request: run.RequestShape{System: 4100, ToolDefs: 9800, History: 22600, ToolResults: map[string]int{"Read": 18000, "Bash": 7100}}})
	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Tokens: 10, Role: run.RoleSecurity, Request: run.RequestShape{System: 999_000}})

	a.handleCommand("/intel")
	if a.view != viewIntel {
		t.Fatalf("/intel opened view %v", a.view)
	}
	text := ansi.Strip(a.intelView())
	for _, want := range []string{
		"Session intelligence", "5-hour limit", "day budget", "left", "pace", "trend", "runway",
		"7 days · tokens per hour", "models · today", "p/m", "q/other", "sent this session", "system 4.1k", "tool defs 9.8k", "history 22.6k", "tool results 25.1k",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("full screen lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "999") {
		t.Fatalf("a security call's request shape leaked into the agent composition:\n%s", text)
	}
	if strings.Count(text, "\n"+"") < 12 {
		t.Fatalf("full screen is too short:\n%s", text)
	}

	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.intelState.sel != 1 {
		t.Fatalf("down: selection %d", a.intelState.sel)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.intelState.sel != 1 {
		t.Fatalf("down past the last row: selection %d, want it clamped to 1", a.intelState.sel)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRight})
	if a.intelState.window != 1 || a.intelState.sel != 0 || !strings.Contains(ansi.Strip(a.intelView()), "models · this week") {
		t.Fatalf("right: window %d sel %d", a.intelState.window, a.intelState.sel)
	}
	a.Update(key("r"))
	if !strings.Contains(ansi.Strip(a.intelView()), "roles · this session") {
		t.Fatal("r did not list roles")
	}
	a.Update(key("m"))
	a.Update(key("R"))
	a.Update(key("b"))
	if a.view != viewBudgets {
		t.Fatalf("b opened view %v", a.view)
	}
}

// R21: the screen switcher lists session intelligence under i and opens it.
func TestBudgetRule21_ScreenSwitcherListsIntel(t *testing.T) {
	a := intelApp(t)
	var found bool
	for _, e := range screenEntries {
		if e.key == "i" {
			found = true
			if e.view != viewIntel || e.name != "intel" {
				t.Fatalf("entry i = %+v", e)
			}
			if !strings.HasPrefix(e.status(a), "today ") {
				t.Fatalf("status = %q, want today's tokens and the pace", e.status(a))
			}
		}
	}
	if !found {
		t.Fatal("the switcher has no i entry")
	}
}

// R18: the request composition counts agent calls only, sums them per part and
// keeps sizes, never content.
func TestBudgetRule18_RequestCompositionSumsAgentCalls(t *testing.T) {
	var r intelRequest
	r.add(run.UsageEvent{Role: run.RoleAgent, Request: run.RequestShape{System: 10, ToolDefs: 20, History: 30, ToolResults: map[string]int{"Read": 5, "Grep": 7}}})
	r.add(run.UsageEvent{Role: run.RoleAgent, Request: run.RequestShape{System: 1, ToolResults: map[string]int{"Read": 2}}})
	r.add(run.UsageEvent{Role: run.RoleSecurity, Request: run.RequestShape{System: 1000}})
	if r.calls != 2 || r.system != 11 || r.toolDefs != 20 || r.history != 30 || r.toolTotal() != 14 || r.toolResults["Read"] != 7 {
		t.Fatalf("composition = %+v", r)
	}
}

// R22: /settings has a row for each intel setting. ui.intel is a UI toggle that
// follows the scope; intel.plan_limits always writes the global file and turns
// header parsing off for the process.
func TestBudgetRule22_SettingsScreenTogglesBothKeys(t *testing.T) {
	a := intelApp(t)
	row := func(key string) settingsRow {
		for _, r := range a.settingsRows() {
			if r.key == key {
				return r
			}
		}
		t.Fatalf("no %q row in /settings", key)
		return settingsRow{}
	}
	if row("intel").value != "on" || row("plan_limits").value != "on" {
		t.Fatalf("defaults = %q / %q, want both on", row("intel").value, row("plan_limits").value)
	}

	// A toggle cycles unset, true, false, so the second press turns a default-on
	// setting off.
	a.settingsState.scope = config.ScopeProject
	for i := 0; i < 2; i++ {
		if err := a.cycleToggle("plan_limits"); err != nil {
			t.Fatal(err)
		}
	}
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.Intel == nil || g.Intel.PlanLimits == nil || *g.Intel.PlanLimits {
		t.Fatalf("global intel = %+v, want plan_limits toggled off in the global file even on project scope", g.Intel)
	}
	if row("plan_limits").value != "off" {
		t.Fatalf("plan_limits row = %q, want off", row("plan_limits").value)
	}
	a.refreshFooter() // applies the setting to the header parser
	t.Cleanup(func() { run.SetPlanLimits(true) })
	if err := a.unsetSetting("plan_limits"); err != nil {
		t.Fatal(err)
	}
	if row("plan_limits").value != "on" {
		t.Fatalf("plan_limits after unset = %q, want on", row("plan_limits").value)
	}

	a.settingsState.scope = config.ScopeGlobal
	for i := 0; i < 2; i++ {
		if err := a.cycleToggle("intel"); err != nil {
			t.Fatal(err)
		}
	}
	if row("intel").value != "off" || a.settings.IntelEnabled() {
		t.Fatalf("intel row = %q, want off", row("intel").value)
	}
	if err := a.unsetSetting("intel"); err != nil {
		t.Fatal(err)
	}
	if row("intel").value != "on" {
		t.Fatalf("intel after unset = %q, want on", row("intel").value)
	}
}
