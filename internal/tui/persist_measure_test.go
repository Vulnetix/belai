package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/testpass"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/transcript"
	"github.com/vulnetix/belai/internal/tui/components"
)

func measureEntries(entries []session.Entry, typ string) []session.Entry {
	var out []session.Entry
	for _, e := range entries {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func measureNum(t *testing.T, meta map[string]any, key string) float64 {
	t.Helper()
	v, ok := meta[key].(float64)
	if !ok {
		t.Fatalf("meta[%q] = %v (%T); meta = %v", key, meta[key], meta[key], meta)
	}
	return v
}

func TestPersistAssistantCarriesCacheTokensAndCost(t *testing.T) {
	cases := []struct {
		name            string
		provider, model string
		cost            float64 // -1: absent
	}{
		{"built-in price", "anthropic", "claude-sonnet-4-5", 3030},
		{"local provider is free", "ollama", "qwen3", 0},
		{"unpriced model omits the cost", "custom", "my-finetune", -1},
	}
	for _, c := range cases {
		a := newPersistApp(t)
		a.echoUser("hi")
		a.messages = append(a.messages, components.Message{
			Role: "assistant", Content: "done", Provider: c.provider, Model: c.model,
			Usage: &transcript.Usage{PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 200},
		})
		a.persistTail()

		got := measureEntries(persistedEntries(t, a), "assistant")
		if len(got) != 1 {
			t.Fatalf("%s: %d assistant entries", c.name, len(got))
		}
		meta := got[0].Meta
		for k, want := range map[string]float64{"prompt_tokens": 1000, "completion_tokens": 100, "total_tokens": 1100, "cache_read_tokens": 600, "cache_write_tokens": 200} {
			if v := measureNum(t, meta, k); v != want {
				t.Errorf("%s: %s = %v, want %v", c.name, k, v, want)
			}
		}
		if c.cost < 0 {
			if _, has := meta["cost_micro_usd"]; has {
				t.Errorf("%s: cost_micro_usd = %v, want absent", c.name, meta["cost_micro_usd"])
			}
		} else if v := measureNum(t, meta, "cost_micro_usd"); v != c.cost {
			t.Errorf("%s: cost_micro_usd = %v, want %v", c.name, v, c.cost)
		}
	}
}

func TestPersistBashToolWritesACheckRunAfterItsResult(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("run the tests")
	a.messages = append(a.messages,
		components.Message{Role: "assistant", ToolCalls: []components.AgentToolCall{
			{ID: "c1", Name: "Bash", Args: `{"command":"go test ./..."}`},
			{ID: "c2", Name: "Bash", Args: `{"command":"ls"}`},
		}},
		components.Message{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"go test ./..."}`, ToolCallID: "c1",
			Content: "FAIL\nexit status 1", Status: "✗", DurationMS: 2500},
		components.Message{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"ls"}`, ToolCallID: "c2", Content: "a", Status: "✓"},
	)
	a.persistTail()

	entries := persistedEntries(t, a)
	checks := measureEntries(entries, "check_run")
	if len(checks) != 1 {
		t.Fatalf("%d check_run entries, want 1: %+v", len(checks), entries)
	}
	m := checks[0].Meta
	if m["kind"] != "test" || m["program"] != "go" || m["subcommand"] != "test" || m["ok"] != false ||
		measureNum(t, m, "exit_code") != 1 || measureNum(t, m, "duration_ms") != 2500 {
		t.Fatalf("meta = %v", m)
	}
	// It follows the tool entry it measures.
	var order []string
	for _, e := range entries {
		order = append(order, e.Type)
	}
	if strings.Join(order, ",") != "user,assistant,tool,check_run,tool" {
		t.Fatalf("entry order = %v", order)
	}
}

func TestPersistShellPanelWritesACheckRun(t *testing.T) {
	a := newPersistApp(t)
	a.echoUser("!make lint")
	a.messages = append(a.messages,
		components.Message{Role: components.ShellRole, ToolArgs: components.ShellArgs("make lint"), ToolCallID: "s1",
			Content: strings.Repeat("x", maxToolResultBytes+10) + "\nexit status 2", Status: "✗", DurationMS: 90},
		components.Message{Role: components.ShellRole, ToolArgs: components.ShellArgs("make test"), ToolCallID: "s2",
			Content: "failed: could not start", Status: "✗"},
	)
	a.persistTail()

	checks := measureEntries(persistedEntries(t, a), "check_run")
	// The exit line lies past the cap on what is stored; it is read before the cut.
	if len(checks) != 1 {
		t.Fatalf("%d check_run entries, want 1", len(checks))
	}
	m := checks[0].Meta
	if m["kind"] != "lint" || m["program"] != "make" || m["subcommand"] != "lint" || measureNum(t, m, "exit_code") != 2 {
		t.Fatalf("meta = %v", m)
	}
}

func TestHandleTestPassWritesACheckRunPerSuiteThatRan(t *testing.T) {
	a := newPersistApp(t)
	a.handleTestPass(testPassMsg{trigger: testpass.TriggerGoal, step: testpass.Step{Outcome: testpass.Outcome{
		Trigger: testpass.TriggerGoal,
		Results: []testrun.Result{
			{Suite: "go", Command: []string{"go", "test", "./..."}, Status: testrun.Passed},
			{Suite: "web", Command: []string{"npm", "test"}, Status: testrun.Failed, ExitCode: 1},
			{Suite: "x", Command: []string{"make", "test"}, Status: testrun.Denied},
		},
	}}})
	got := measureEntries(persistedEntries(t, a), sessionmeasure.EntryCheckRun)
	if len(got) != 2 {
		t.Fatalf("%d check_run entries, want 2", len(got))
	}
	if got[0].Meta["program"] != "go" || got[1].Meta["program"] != "npm" || got[1].Meta["ok"] != false {
		t.Fatalf("entries = %+v", got)
	}
}
