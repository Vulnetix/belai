package sessionmeasure

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/transcript"
)

// jsonl writes entries as the session store does, one JSON object per line.
func jsonl(t *testing.T, entries ...session.Entry) []byte {
	t.Helper()
	var b bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

func assistant(provider, model string, u transcript.Usage, override *budget.Price) session.Entry {
	meta := map[string]any{"provider": provider, "model": model}
	AddUsage(meta, provider, model, override, u)
	return session.Entry{Type: "assistant", Role: "assistant", Meta: meta}
}

func TestAddUsage(t *testing.T) {
	u := transcript.Usage{PromptTokens: 10000, CompletionTokens: 1000, CacheReadTokens: 6000, CacheWriteTokens: 2000}
	cases := []struct {
		name            string
		provider, model string
		override        *budget.Price
		cost            any // nil: the key is absent
	}{
		{"built-in price", "anthropic", "claude-sonnet-4-5", nil, int64(6000 + 1800 + 7500 + 15000)},
		{"factory price wins", "anthropic", "claude-sonnet-4-5", &budget.Price{Input: 1, Output: 2}, int64(2000 + 6000 + 2000 + 2000)},
		{"local provider is free", "ollama", "qwen3", nil, int64(0)},
		{"unpriced model omits the cost", "custom", "my-finetune", nil, nil},
	}
	for _, c := range cases {
		meta := map[string]any{}
		AddUsage(meta, c.provider, c.model, c.override, u)
		if meta["prompt_tokens"] != 10000 || meta["completion_tokens"] != 1000 || meta["total_tokens"] != 11000 ||
			meta["cache_read_tokens"] != 6000 || meta["cache_write_tokens"] != 2000 {
			t.Errorf("%s: tokens = %v", c.name, meta)
		}
		got, has := meta["cost_micro_usd"]
		if c.cost == nil {
			if has {
				t.Errorf("%s: cost_micro_usd = %v, want absent", c.name, got)
			}
			continue
		}
		if got != c.cost {
			t.Errorf("%s: cost_micro_usd = %v (%T), want %v", c.name, got, got, c.cost)
		}
	}
}

func TestRollupFromASession(t *testing.T) {
	diff := func(files ...filediff.WireFile) map[string]any {
		b, _ := json.Marshal(filediff.WireChange{Files: files})
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	}
	sonnet := transcript.Usage{PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 600, CacheWriteTokens: 200}
	entries := []session.Entry{
		{Type: "session_meta"},
		{Type: "turn_state", Timestamp: 1_000_000, Meta: map[string]any{"state": "started", "started_at": 1_000_000}},
		assistant("anthropic", "claude-sonnet-4-5", sonnet, nil),
		assistant("anthropic", "claude-sonnet-4-5", sonnet, nil),
		assistant("ollama", "qwen3", transcript.Usage{PromptTokens: 50, CompletionTokens: 5}, nil),
		assistant("custom", "my-finetune", transcript.Usage{PromptTokens: 70, CompletionTokens: 7}, nil),
		{Type: "tool", Meta: map[string]any{"tool_name": "Edit", "diff": diff(
			filediff.WireFile{Path: "a.go", Adds: 10, Dels: 2}, filediff.WireFile{Path: "b.go", Created: true, Adds: 30})}},
		{Type: "tool", Meta: map[string]any{"tool_name": "Edit", "diff": diff(filediff.WireFile{Path: "a.go", Adds: 5, Dels: 5})}},
		{Type: "tool", Meta: map[string]any{"tool_name": "Read"}},
		{Type: EntryCheckRun, Meta: Check{KindTest, "go", "test", 0, 100}.Meta()},
		{Type: EntryCheckRun, Meta: Check{KindTest, "go", "test", 1, 100}.Meta()},
		{Type: EntryCheckRun, Meta: Check{KindBuild, "go", "build", 0, 100}.Meta()},
		{Type: EntryCheckRun, Meta: Check{KindGate, "go", "test", 0, 100}.Meta()},
		{Type: "ask", ID: "a1", Timestamp: 1_100_000, Meta: map[string]any{"ask_id": "a1", "kind": "permission"}},
		{Type: "ask", ID: "a2", Timestamp: 1_120_000, Meta: map[string]any{"ask_id": "a2", "kind": "clarify"}},
		{Type: "ask_answer", Timestamp: 1_130_000, Meta: map[string]any{"ask_id": "a2", "kind": "clarify"}},
		{Type: "ask_answer", Timestamp: 1_150_000, Meta: map[string]any{"ask_id": "a1", "kind": "permission"}},
		// A plan review does not block the agent, and an unanswered ask has no wait.
		{Type: "ask", Timestamp: 1_160_000, Meta: map[string]any{"ask_id": "p1", "kind": "plan_review"}},
		{Type: "ask_answer", Timestamp: 1_900_000, Meta: map[string]any{"ask_id": "p1", "kind": "plan_review"}},
		{Type: "ask", Timestamp: 1_170_000, Meta: map[string]any{"ask_id": "a3", "kind": "permission"}},
		{Type: "turn_state", Timestamp: 1_300_000, Meta: map[string]any{"state": "ended", "started_at": 1_000_000, "duration_ms": 300_000}},
	}
	got, err := Compute(bytes.NewReader(jsonl(t, entries...)))
	if err != nil {
		t.Fatal(err)
	}
	cost := func(v int64) *int64 { return &v }
	want := &Rollup{
		ActiveMS: 300_000 - 50_000, // a1 and a2 overlap: 1_100_000 to 1_150_000
		WaitMS:   50_000, ToolCalls: 3,
		CheckCounts:  &CheckCounts{Build: 1, Test: 2, Lint: 0, Gate: 1},
		FilesChanged: 2, Additions: 45, Deletions: 7,
		Models: []ModelUsage{
			{Provider: "anthropic", Model: "claude-sonnet-4-5", PromptTokens: 2000, CompletionTokens: 200, CacheReadTokens: 1200, CacheWriteTokens: 400,
				// 2 x (200 uncached x 3 + 600 x 0.3 + 200 x 3.75 + 100 x 15) = 2 x 3030
				CostMicroUSD: cost(6060), Priced: true},
			{Provider: "custom", Model: "my-finetune", PromptTokens: 70, CompletionTokens: 7},
			{Provider: "ollama", Model: "qwen3", PromptTokens: 50, CompletionTokens: 5, CostMicroUSD: cost(0), Priced: true},
		},
	}
	if !reflect.DeepEqual(got, want) {
		gb, _ := json.MarshalIndent(got, "", " ")
		wb, _ := json.MarshalIndent(want, "", " ")
		t.Fatalf("rollup =\n%s\nwant\n%s", gb, wb)
	}
}

func TestRollupJSONShape(t *testing.T) {
	cost := int64(642000)
	r := Rollup{ActiveMS: 1, WaitMS: 2, ToolCalls: 3, CheckCounts: &CheckCounts{1, 2, 3, 4}, FilesChanged: 5, Additions: 6, Deletions: 7,
		Models: []ModelUsage{{"anthropic", "m", 1, 2, 3, 4, &cost, true}, {"custom", "x", 1, 2, 0, 0, nil, false}}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"active_ms":1,"wait_ms":2,"tool_calls":3,"check_counts":{"build":1,"test":2,"lint":3,"gate":4},` +
		`"files_changed":5,"additions":6,"deletions":7,"models":[` +
		`{"provider":"anthropic","model":"m","prompt_tokens":1,"completion_tokens":2,"cache_read_tokens":3,"cache_write_tokens":4,"cost_micro_usd":642000,"priced":true},` +
		`{"provider":"custom","model":"x","prompt_tokens":1,"completion_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0,"cost_micro_usd":null,"priced":false}]}`
	if string(b) != want {
		t.Fatalf("json =\n%s\nwant\n%s", b, want)
	}
}

func TestRollupOfAnOldTranscriptHasNoCheckCounts(t *testing.T) {
	entries := []session.Entry{
		{Type: "user", Content: "hi"},
		{Type: "assistant", Meta: map[string]any{"provider": "anthropic", "model": "claude-sonnet-4-5", "prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}},
		{Type: "tool", Meta: map[string]any{"tool_name": "Bash"}},
	}
	got, err := Compute(bytes.NewReader(jsonl(t, entries...)))
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckCounts != nil {
		t.Fatalf("check_counts = %+v, want null", got.CheckCounts)
	}
	if len(got.Models) != 1 || got.Models[0].Priced || got.Models[0].CostMicroUSD != nil || got.Models[0].PromptTokens != 100 {
		t.Fatalf("models = %+v: tokens only, no cost", got.Models)
	}
	if got.ToolCalls != 1 || got.ActiveMS != 0 || got.WaitMS != 0 {
		t.Fatalf("rollup = %+v", got)
	}
}

func TestRollupOfAMeasuredSessionWithNoChecksCountsZero(t *testing.T) {
	entries := []session.Entry{assistant("anthropic", "claude-sonnet-4-5", transcript.Usage{PromptTokens: 10, CompletionTokens: 1}, nil)}
	got, err := Compute(bytes.NewReader(jsonl(t, entries...)))
	if err != nil || got.CheckCounts == nil || *got.CheckCounts != (CheckCounts{}) {
		t.Fatalf("rollup = %+v, %v", got, err)
	}
}

func TestRollupPartlyPricedModelIsTokensOnly(t *testing.T) {
	u := transcript.Usage{PromptTokens: 100, CompletionTokens: 10}
	priced := assistant("custom", "m", u, &budget.Price{Input: 1, Output: 1})
	unpriced := assistant("custom", "m", u, nil)
	got, err := Compute(bytes.NewReader(jsonl(t, priced, unpriced)))
	if err != nil {
		t.Fatal(err)
	}
	m := got.Models[0]
	if m.Priced || m.CostMicroUSD != nil || m.PromptTokens != 200 {
		t.Fatalf("model = %+v", m)
	}
}

func TestRollupSkipsDamagedLinesAndReadsAFile(t *testing.T) {
	good := jsonl(t, session.Entry{Type: "tool", Meta: map[string]any{"tool_name": "Read"}})
	data := append([]byte("not json\n{\"type\":\"tool\",\"meta\":7}\n"), good...)
	data = append(data, []byte(`{"type":"tool"}`)...) // an unfinished last line still counts
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := RollupFile(path)
	if err != nil || got.ToolCalls != 3 {
		t.Fatalf("rollup = %+v, %v", got, err)
	}
	if _, err := RollupFile(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Fatal("a missing file is not an error")
	}
	if !strings.Contains(string(good), `"tool"`) {
		t.Fatal("fixture")
	}
}

func TestRollupCountsTheRowsOfAFileWithoutCounts(t *testing.T) {
	f := filediff.WireFile{Path: "x.go", Rows: []filediff.WireRow{{Op: "+"}, {Op: "+"}, {Op: "-"}, {Op: " "}}}
	if a, d := f.Stat(); a != 2 || d != 1 {
		t.Fatalf("Stat = %d, %d", a, d)
	}
	if a, d := (filediff.WireFile{Path: "y.go", Adds: 9, Dels: 4}).Stat(); a != 9 || d != 4 {
		t.Fatalf("Stat = %d, %d", a, d)
	}
}
