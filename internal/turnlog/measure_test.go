package turnlog

import (
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/transcript"
)

func lastOf(t *testing.T, entries []session.Entry, typ string) session.Entry {
	t.Helper()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type == typ {
			return entries[i]
		}
	}
	t.Fatalf("no %s entry in %d entries", typ, len(entries))
	return session.Entry{}
}

func num(t *testing.T, meta map[string]any, key string) float64 {
	t.Helper()
	v, ok := meta[key].(float64)
	if !ok {
		t.Fatalf("meta[%q] = %v (%T), want a number; meta = %v", key, meta[key], meta[key], meta)
	}
	return v
}

func modelCall(u transcript.Usage) agent.Event {
	return agent.Event{Kind: agent.EventModelCallKind, Usage: &u}
}

func TestAssistantEntryCarriesCacheTokensAndCost(t *testing.T) {
	l, read := newLog(t)
	l.SetModel("anthropic", "claude-sonnet-4-5", nil)
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "Done."})
	l.Observe(modelCall(transcript.Usage{PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 200}))
	l.Flush()

	meta := lastOf(t, read(), "assistant").Meta
	if meta["provider"] != "anthropic" || meta["model"] != "claude-sonnet-4-5" {
		t.Fatalf("meta = %v", meta)
	}
	for k, want := range map[string]float64{
		"prompt_tokens": 1000, "completion_tokens": 100, "total_tokens": 1100,
		"cache_read_tokens": 600, "cache_write_tokens": 200,
		// 200 uncached x 3 + 600 x 0.3 + 200 x 3.75 + 100 x 15
		"cost_micro_usd": 3030,
	} {
		if got := num(t, meta, k); got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}
}

func TestEachAssistantEntryTakesItsOwnCallsUsage(t *testing.T) {
	l, read := newLog(t)
	l.SetModel("openai", "gpt-4o", nil)
	call := func(text string, in int) {
		l.Observe(agent.Event{Kind: agent.EventTextKind, Text: text})
		l.Observe(modelCall(transcript.Usage{PromptTokens: in, CompletionTokens: 10}))
		l.Observe(agent.Event{Kind: agent.EventToolStartKind, Tool: &rolemanager.ToolCall{ID: text, Name: "Read", RawArgs: "{}"}})
		l.Observe(agent.Event{Kind: agent.EventToolResultKind, ToolCallID: text, ToolName: "Read", ToolResult: "x"})
	}
	call("one", 100)
	call("two", 200)
	var got []float64
	for _, e := range read() {
		if e.Type == "assistant" {
			got = append(got, num(t, e.Meta, "prompt_tokens"))
		}
	}
	if len(got) != 2 || got[0] != 100 || got[1] != 200 {
		t.Fatalf("prompt tokens per assistant entry = %v, want [100 200]", got)
	}
}

func TestCostFollowsThePriceAndTheModel(t *testing.T) {
	u := transcript.Usage{PromptTokens: 1000, CompletionTokens: 100}
	cases := []struct {
		name            string
		provider, model string
		price           *budget.Price
		cost            float64 // -1: the key is absent
	}{
		{"factory price wins over the table", "anthropic", "claude-sonnet-4-5", &budget.Price{Input: 1, Output: 1}, 1100},
		{"local provider is free", "ollama", "qwen3", nil, 0},
		{"unpriced model omits the cost", "custom", "my-finetune", nil, -1},
		{"no model set omits the cost", "", "", nil, -1},
	}
	for _, c := range cases {
		l, read := newLog(t)
		l.SetModel(c.provider, c.model, c.price)
		l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "x"})
		l.Observe(modelCall(u))
		l.Flush()
		meta := lastOf(t, read(), "assistant").Meta
		if c.cost < 0 {
			if _, has := meta["cost_micro_usd"]; has {
				t.Errorf("%s: cost_micro_usd = %v, want absent", c.name, meta["cost_micro_usd"])
			}
		} else if got := num(t, meta, "cost_micro_usd"); got != c.cost {
			t.Errorf("%s: cost_micro_usd = %v, want %v", c.name, got, c.cost)
		}
		if got := num(t, meta, "prompt_tokens"); got != 1000 {
			t.Errorf("%s: prompt_tokens = %v", c.name, got)
		}
	}
}

func TestTurnFactsNameTheModel(t *testing.T) {
	l, read := newLog(t)
	l.SetModel("custom", "old", &budget.Price{Input: 9, Output: 9})
	l.TurnStarted(map[string]any{"provider": "openai", "model": "gpt-4o"})
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "x"})
	l.Observe(modelCall(transcript.Usage{PromptTokens: 1000, CompletionTokens: 0}))
	l.Flush()
	meta := lastOf(t, read(), "assistant").Meta
	if meta["provider"] != "openai" || meta["model"] != "gpt-4o" {
		t.Fatalf("meta = %v", meta)
	}
	// 1000 x 2.5 at the built-in price: the old model's price was dropped.
	if got := num(t, meta, "cost_micro_usd"); got != 2500 {
		t.Fatalf("cost_micro_usd = %v, want 2500", got)
	}
}

func TestAnAssistantEntryWithoutUsageHasNoTokens(t *testing.T) {
	l, read := newLog(t)
	l.Observe(agent.Event{Kind: agent.EventTextKind, Text: "x"})
	l.Flush()
	meta := lastOf(t, read(), "assistant").Meta
	for _, k := range []string{"prompt_tokens", "cache_read_tokens", "cost_micro_usd"} {
		if _, has := meta[k]; has {
			t.Errorf("meta[%q] present without usage: %v", k, meta)
		}
	}
}

func TestBashResultWritesACheckRun(t *testing.T) {
	l, read := newLog(t)
	l.Observe(agent.Event{Kind: agent.EventToolResultKind, ToolCallID: "c1", ToolName: "Bash",
		ToolArgs: `{"command":"cd pkg && go test ./..."}`, ToolResult: "FAIL\nexit status 1", Duration: 4210 * time.Millisecond})
	l.Observe(agent.Event{Kind: agent.EventToolResultKind, ToolCallID: "c2", ToolName: "Bash",
		ToolArgs: `{"command":"ls"}`, ToolResult: "a b"})
	l.Observe(agent.Event{Kind: agent.EventToolResultKind, ToolCallID: "c3", ToolName: "Read",
		ToolArgs: `{"path":"go test"}`, ToolResult: "x"})
	var checks []session.Entry
	for _, e := range read() {
		if e.Type == "check_run" {
			checks = append(checks, e)
		}
	}
	if len(checks) != 1 {
		t.Fatalf("%d check_run entries, want 1", len(checks))
	}
	m := checks[0].Meta
	if m["kind"] != "test" || m["program"] != "go" || m["subcommand"] != "test" || m["ok"] != false ||
		num(t, m, "exit_code") != 1 || num(t, m, "duration_ms") != 4210 {
		t.Fatalf("meta = %v", m)
	}
}

func TestShellCheckAndCheckRunsAndCommit(t *testing.T) {
	l, read := newLog(t)
	l.ShellCheck("make lint", 2, 300*time.Millisecond)
	l.ShellCheck("echo hi", 0, time.Second)
	l.CheckRuns([]testrun.Result{
		{Command: []string{"go", "test", "./..."}, Status: testrun.Passed, Duration: time.Second},
		{Command: []string{"npm", "test"}, Status: testrun.Denied},
	})
	l.Check(sessionmeasure.Check{Kind: sessionmeasure.KindGate, Program: "go", Subcommand: "test", DurationMS: 5})
	l.Commit(sessionmeasure.Commit{SHA: "0123456789abcdef0123456789abcdef01234567", Branch: "b"})

	counts := map[string]int{}
	for _, e := range read() {
		if e.Type == "check_run" {
			counts[e.Meta["kind"].(string)]++
		}
		if e.Type == "commit" && e.Meta["sha"] != "0123456789abcdef0123456789abcdef01234567" {
			t.Errorf("commit meta = %v", e.Meta)
		}
	}
	if counts["lint"] != 1 || counts["test"] != 1 || counts["gate"] != 1 || len(counts) != 3 {
		t.Fatalf("checks = %v", counts)
	}
	lastOf(t, read(), "commit")

	// A nil or writer-less log ignores all of it.
	var nilLog *Log
	nilLog.SetModel("a", "b", nil)
	nilLog.ShellCheck("make test", 0, 0)
	nilLog.Check(sessionmeasure.Check{})
	nilLog.Commit(sessionmeasure.Commit{})
	nilLog.CheckRuns(nil)
	New(nil).ShellCheck("make test", 0, 0)
}
