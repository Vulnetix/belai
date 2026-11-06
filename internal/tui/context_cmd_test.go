package tui

import (
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/run"
)

func contextApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("VULNETIX_WEB_URL", "")
	a := intelApp(t)
	a.settings.ContextWindows = map[string]int{"claude-sonnet-4-5": 200_000}
	a.cfg.Provider, a.cfg.Model = "anthropic", "claude-sonnet-4-5"
	return a
}

func agentCall(shape run.RequestShape) run.UsageEvent {
	return run.UsageEvent{Provider: "anthropic", Model: "claude-sonnet-4-5", Role: run.RoleAgent,
		Prompt: 10_000, Completion: 500, CacheRead: 9_000, Request: shape}
}

func TestContextBeforeAndAfterACall(t *testing.T) {
	a := contextApp(t)
	a.handleCommand("/context")
	if got := lastSystem(a); got != "context: nothing sent yet" {
		t.Fatalf("before any call: %q", got)
	}

	a.handleUsage(agentCall(run.RequestShape{System: 4000, ToolDefs: 6000, History: 20_000,
		ToolResults: map[string]int{"Read": 9000, "Bash": 12_000}}))
	a.handleCommand("/context")
	got := lastSystem(a)
	for _, want := range []string{
		"context: 51k of 200k tokens used (25%), estimated", "system", "4k", "tool defs", "history", "20k",
		"tool results", "21k", "Bash", "12k", "Read", "free", "149k", "session cost",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("/context lacks %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "Bash") > strings.Index(got, "Read") {
		t.Fatalf("tool results are not largest first:\n%s", got)
	}
}

// /context does not depend on ui.intel.
func TestContextWorksWithIntelOff(t *testing.T) {
	a := contextApp(t)
	off := false
	a.settings.UI = &config.UISettings{Intel: &off}
	a.handleUsage(agentCall(run.RequestShape{System: 100, ToolDefs: 100, History: 100}))
	a.handleCommand("/context")
	if !strings.Contains(lastSystem(a), "context:") {
		t.Fatal("/context needs ui.intel")
	}
}

// An unknown window prints the request size and no gauge or free row, and an
// unpriced model prints no cost figure.
func TestContextUnknownWindowAndPrice(t *testing.T) {
	a := intelApp(t) // model "m": no window, no price
	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Role: run.RoleAgent, Prompt: 100, Completion: 10,
		Request: run.RequestShape{System: 500, ToolDefs: 500, History: 500}})
	a.handleCommand("/context")
	got := lastSystem(a)
	if !strings.Contains(got, "the model's window is unknown") || strings.Contains(got, "free") || strings.Contains(got, "[") {
		t.Fatalf("unknown window:\n%s", got)
	}
	if !strings.Contains(got, "no price known") || strings.Contains(got, "$") {
		t.Fatalf("unknown price:\n%s", got)
	}
}

// A tool name comes from outside (an MCP server), so it is cleaned first.
func TestContextSanitisesToolNames(t *testing.T) {
	a := contextApp(t)
	a.handleUsage(agentCall(run.RequestShape{System: 1, ToolDefs: 1, History: 1,
		ToolResults: map[string]int{"mcp__x__<evil>\x1b[31m\u202e": 500}}))
	a.handleCommand("/context")
	got := lastSystem(a)
	for _, bad := range []string{"<", "\x1b", "\u202e"} {
		if strings.Contains(got, bad) {
			t.Fatalf("tool name was not cleaned (%q):\n%q", bad, got)
		}
	}
	if !strings.Contains(got, "mcp__x__evil31m") {
		t.Fatalf("cleaned name missing:\n%s", got)
	}
}

func TestReplyCostLine(t *testing.T) {
	a := contextApp(t)
	count := func() int { return len(systemLines(a)) }

	// Off by default: nothing printed.
	n := count()
	a.handleUsage(agentCall(run.RequestShape{System: 1, ToolDefs: 1}))
	if count() != n {
		t.Fatal("a reply cost line printed with ui.reply_cost off")
	}

	on := true
	a.settings.UI = &config.UISettings{ReplyCost: &on}
	a.handleUsage(agentCall(run.RequestShape{System: 1, ToolDefs: 1}))
	got := lastSystem(a)
	// 1000 uncached x $3 + 9000 cache read x $0.30 + 500 out x $15, per million.
	if got != "in 10k (cache 90%) · out 500 · ~$0.013 est." {
		t.Fatalf("line = %q", got)
	}

	// Only agent-role calls print.
	n = count()
	a.handleUsage(run.UsageEvent{Provider: "anthropic", Model: "claude-sonnet-4-5", Role: run.RoleSecurity, Prompt: 100, Completion: 5})
	if count() != n {
		t.Fatal("a role-manager call printed a reply cost line")
	}

	// A call with no reported split prints nothing instead of a made-up number.
	a.handleUsage(run.UsageEvent{Provider: "anthropic", Model: "claude-sonnet-4-5", Role: run.RoleAgent, Tokens: 900, Estimated: true})
	if count() != n {
		t.Fatal("an estimated call printed a cost line")
	}

	// An unpriced model keeps the token counts and drops the figure; no cache
	// activity drops the cache share.
	a.handleUsage(run.UsageEvent{Provider: "p", Model: "m", Role: run.RoleAgent, Prompt: 1200, Completion: 80})
	if got := lastSystem(a); got != "in 1.2k · out 80" {
		t.Fatalf("unpriced line = %q", got)
	}
}

// The /context output and the cost line are render-only rows: they never reach
// a session entry, a model turn or the transcript sent for compaction.
func TestContextAndCostRowsNeverReachTheRecordOrTheModel(t *testing.T) {
	a := contextApp(t)
	on := true
	a.settings.UI = &config.UISettings{ReplyCost: &on}
	a.handleUsage(agentCall(run.RequestShape{System: 4000, ToolDefs: 6000, History: 20_000, ToolResults: map[string]int{"Read": 9000}}))
	a.handleCommand("/context")

	var rows int
	for _, m := range a.messages {
		if m.Role != "system" {
			continue
		}
		txt := m.Text()
		if !strings.Contains(txt, "context:") && !strings.Contains(txt, " est.") {
			continue
		}
		rows++
		if !neverPersisted(m) {
			t.Fatalf("row would be written to the session record: %q", txt)
		}
	}
	if rows != 2 {
		t.Fatalf("expected the cost line and the /context row, found %d", rows)
	}
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "context:") || strings.Contains(turn.Content, " est.") {
			t.Fatalf("a measurement row reached a model turn: %q", turn.Content)
		}
	}
	for _, m := range a.transcriptMessages() {
		if m.Role != "system" && (strings.Contains(m.Content, "context:") || strings.Contains(m.Content, " est.")) {
			t.Fatalf("a measurement row became a conversation message: %q", m.Content)
		}
	}
}

// The intel screen carries the same window gauge.
func TestIntelScreenShowsTheContextWindow(t *testing.T) {
	a := contextApp(t)
	a.handleUsage(agentCall(run.RequestShape{System: 4000, ToolDefs: 6000, History: 20_000}))
	a.handleCommand("/intel")
	if txt := ansi.Strip(a.intelView()); !strings.Contains(txt, "context window · 30k of 200k (15%)") {
		t.Fatalf("intel screen lacks the window gauge:\n%s", txt)
	}
}

// The settings row writes the user's global file whatever scope the screen is
// on, so a repository file never carries a reply_cost true.
func TestReplyCostRowIsGlobalOnly(t *testing.T) {
	if !settingsWritesGlobalOnly("reply_cost") || settingsGroupOf("reply_cost") != "budgets" {
		t.Fatal("reply_cost should be a global-only row in the budgets group")
	}
	a := settingsGroupedApp(t, 120, 40)
	a.settingsState.scope = config.ScopeProject
	if err := a.cycleToggle("reply_cost"); err != nil {
		t.Fatal(err)
	}
	if proj, _ := config.LoadProject(a.workdir); proj.UI != nil && proj.UI.ReplyCost != nil {
		t.Fatalf("reply_cost reached the project file: %+v", proj.UI)
	}
	if glob, _ := config.LoadGlobal(); glob.UI == nil || glob.UI.ReplyCost == nil || !*glob.UI.ReplyCost {
		t.Fatalf("reply_cost was not written to the global file: %+v", glob.UI)
	}
	if !a.settings.ReplyCostEnabled() {
		t.Fatal("the reloaded settings do not show the line on")
	}
	if err := a.unsetSetting("reply_cost"); err != nil {
		t.Fatal(err)
	}
	if a.settings.ReplyCostEnabled() {
		t.Fatal("x did not clear the row")
	}
}
