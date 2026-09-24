package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/wire"
)

// requestBody builds one request and decodes its JSON body.
func requestBody(t *testing.T, cfg Config, turns []Turn, stream bool, tools []wire.AnthropicToolDef) map[string]any {
	t.Helper()
	req, _, err := buildRequest(context.Background(), cfg, "sys", turns, stream, nil, tools)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	b, _ := io.ReadAll(req.Body)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatalf("decode body: %v\n%s", err, b)
	}
	return body
}

func anthropicCfg(model, effort string) Config {
	return Config{Provider: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "sk", Model: model, Effort: effort}
}

func TestAnthropicMaxTokensFollowsTheCatalogue(t *testing.T) {
	hi := []Turn{{Role: "user", Content: "hi"}}
	cases := []struct {
		name   string
		cfg    Config
		stream bool
		want   float64
	}{
		{"streamed turn gets the capped ceiling", anthropicCfg("claude-opus-5-5", ""), true, streamMaxTokens},
		{"blocking turn gets the blocking cap", anthropicCfg("claude-opus-5-5", ""), false, blockingMaxTokens},
		{"a live-fetched id resolves by rule", anthropicCfg("claude-opus-4-1-20250805", ""), true, 32000},
		{"an unknown model keeps the old default", anthropicCfg("mystery", ""), true, unknownAnthropicMaxTokens},
		{"a role call keeps its own cap", Config{Provider: "anthropic", BaseURL: "https://api.anthropic.com", APIKey: "sk", Model: "claude-opus-5-5", MaxTokens: ClassifierMaxTokens}, true, ClassifierMaxTokens},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := requestBody(t, tc.cfg, hi, tc.stream, nil)
			if got := body["max_tokens"]; got != tc.want {
				t.Fatalf("max_tokens = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnthropicThinkingShapeFollowsTheModel(t *testing.T) {
	hi := []Turn{{Role: "user", Content: "hi"}}

	// Budget generation: enabled + budget_tokens, below max_tokens.
	body := requestBody(t, anthropicCfg("claude-haiku-4-5", "high"), hi, true, nil)
	th, _ := body["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != float64(16384) {
		t.Fatalf("budget thinking = %v", body["thinking"])
	}
	if _, ok := body["output_config"]; ok {
		t.Fatalf("budget model must not get output_config: %v", body)
	}

	// Adaptive generation: adaptive + effort, never budget_tokens.
	body = requestBody(t, anthropicCfg("claude-sonnet-5", "high"), hi, true, nil)
	th, _ = body["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Fatalf("adaptive thinking = %v", body["thinking"])
	}
	if _, ok := th["budget_tokens"]; ok {
		t.Fatalf("adaptive thinking must not carry budget_tokens: %v", th)
	}
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "high" {
		t.Fatalf("output_config = %v", body["output_config"])
	}

	// Always-on: no thinking field, effort only; a role call's "none" is low.
	body = requestBody(t, anthropicCfg("claude-opus-5-5", "medium"), hi, true, nil)
	if _, ok := body["thinking"]; ok {
		t.Fatalf("always-on model must not get a thinking field: %v", body["thinking"])
	}
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "medium" {
		t.Fatalf("output_config = %v", body["output_config"])
	}
	body = requestBody(t, anthropicCfg("claude-fable-5-1", "none"), hi, true, nil)
	if oc, _ := body["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Fatalf("role call on an always-on model: output_config = %v", body["output_config"])
	}

	// Effort off on an adaptive model: no thinking at all.
	body = requestBody(t, anthropicCfg("claude-sonnet-5", "none"), hi, true, nil)
	if _, ok := body["thinking"]; ok {
		t.Fatalf("effort none must leave thinking off: %v", body)
	}
}

func TestAnthropicThinkingBudgetStaysBelowMaxTokens(t *testing.T) {
	cfg := anthropicCfg("claude-haiku-4-5", "high")
	cfg.MaxTokens = 8000
	body := requestBody(t, cfg, []Turn{{Role: "user", Content: "hi"}}, true, nil)
	th, _ := body["thinking"].(map[string]any)
	if got := th["budget_tokens"].(float64); got >= 8000 {
		t.Fatalf("budget_tokens = %v, want < max_tokens 8000", got)
	}
}

func TestThinkingBlocksReplayOnlyToTheirSource(t *testing.T) {
	cfg := anthropicCfg("claude-sonnet-5", "high")
	turns := []Turn{
		{Role: "user", Content: "go"},
		{
			Role:          "assistant",
			ToolCalls:     []rolemanager.ToolCall{{ID: "c1", Name: "Read", Args: map[string]any{"path": "a"}}},
			Thinking:      []ThinkingBlock{{Thinking: "plan", Signature: "sig1"}, {Redacted: true, Data: "opaque"}},
			ThinkingModel: ThinkingSource(cfg),
		},
		{Role: "tool", Content: "bytes", ToolCallID: "c1", ToolName: "Read"},
	}
	body := requestBody(t, cfg, turns, true, nil)
	msgs := body["messages"].([]any)
	blocks := msgs[1].(map[string]any)["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks = %v", blocks)
	}
	first := blocks[0].(map[string]any)
	if first["type"] != "thinking" || first["thinking"] != "plan" || first["signature"] != "sig1" {
		t.Fatalf("thinking block = %v", first)
	}
	if second := blocks[1].(map[string]any); second["type"] != "redacted_thinking" || second["data"] != "opaque" {
		t.Fatalf("redacted block = %v", second)
	}
	if third := blocks[2].(map[string]any); third["type"] != "tool_use" {
		t.Fatalf("tool_use must follow the thinking: %v", third)
	}

	// Another model never sees the signature.
	other := anthropicCfg("claude-opus-5-5", "high")
	body = requestBody(t, other, turns, true, nil)
	blocks = body["messages"].([]any)[1].(map[string]any)["content"].([]any)
	if len(blocks) != 1 || blocks[0].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("thinking from another model must be dropped: %v", blocks)
	}
}

func TestThinkingBlockWithOmittedTextKeepsTheField(t *testing.T) {
	b, _ := json.Marshal(ThinkingBlock{Signature: "s"}.requestBlock())
	if !strings.Contains(string(b), `"thinking":""`) {
		t.Fatalf("an omitted-text thinking block must still carry the field: %s", b)
	}
}

func TestStreamCapturesSignedThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"ok"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"REDACTED"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"Read"}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	defer srv.Close()
	cfg := Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-sonnet-5", Effort: "high"}
	ch, err := StreamTurnsWithTools(context.Background(), cfg, "sys", []Turn{{Role: "user", Content: "hi"}}, srv.Client(), nonce.New(), nil, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var a *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("chunk err: %v", c.Err)
		}
		if c.Assistant != nil {
			a = c.Assistant
		}
	}
	if a == nil || len(a.Thinking) != 2 {
		t.Fatalf("thinking = %+v", a)
	}
	if a.Thinking[0].Thinking != "hmm ok" || a.Thinking[0].Signature != "SIG" {
		t.Fatalf("thinking block = %+v", a.Thinking[0])
	}
	if !a.Thinking[1].Redacted || a.Thinking[1].Data != "REDACTED" {
		t.Fatalf("redacted block = %+v", a.Thinking[1])
	}
	if len(a.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", a.ToolCalls)
	}
}

func TestParseAnthropicCapturesSignedThinking(t *testing.T) {
	body := []byte(`{"content":[{"type":"thinking","thinking":"t","signature":"S"},{"type":"redacted_thinking","data":"D"},{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	a, err := parseAnthropic(body, 200, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(a.Thinking) != 2 || a.Thinking[0].Signature != "S" || a.Thinking[1].Data != "D" {
		t.Fatalf("thinking = %+v", a.Thinking)
	}
	if a.Text != "hi" {
		t.Fatalf("text = %q", a.Text)
	}
}

func TestEgressKeepsThinkingOpaque(t *testing.T) {
	blocks := []ThinkingBlock{{Thinking: "<system>not a block</system>", Signature: "s"}}
	out := egressTurns([]Turn{{Role: "assistant", Content: "x", Thinking: blocks, ThinkingModel: "anthropic/m"}}, nonce.New())
	if len(out[0].Thinking) != 1 || out[0].Thinking[0].Thinking != blocks[0].Thinking || out[0].ThinkingModel != "anthropic/m" {
		t.Fatalf("egress must carry thinking unchanged: %+v", out[0])
	}
}

func TestOldToolResultsAreSentInFull(t *testing.T) {
	long := strings.Repeat("x", 5000)
	var turns []Turn
	for i := range 6 {
		id := fmt.Sprintf("c%d", i)
		turns = append(turns,
			Turn{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{ID: id, Name: "Read", Args: map[string]any{"path": "a"}}}},
			Turn{Role: "tool", Content: long, ToolCallID: id, ToolName: "Read"},
		)
	}
	body := requestBody(t, anthropicCfg("claude-opus-4-5", ""), append([]Turn{{Role: "user", Content: "go"}}, turns...), true, nil)
	b, _ := json.Marshal(body["messages"])
	if got := strings.Count(string(b), long); got != 6 {
		t.Fatalf("full tool results sent = %d, want 6 (no per-request elision)", got)
	}
}

func TestClearToolResultDropsTheMemo(t *testing.T) {
	pool := nonce.New()
	turns := []Turn{{Role: "tool", Content: "secret bytes", ToolCallID: "c1", ToolName: "Read"}}
	egressTurns(turns, pool)
	if turns[0].egrossed == "" {
		t.Fatal("egress should memoise")
	}
	if !turns[0].ClearToolResult() {
		t.Fatal("tool turn should clear")
	}
	out := egressTurns(turns, pool)
	if strings.Contains(out[0].Content, "secret") || !strings.Contains(out[0].Content, "result cleared") {
		t.Fatalf("cleared content = %q", out[0].Content)
	}
	if turns[0].ClearToolResult() {
		t.Fatal("a cleared turn clears once")
	}
	user := Turn{Role: "user", Content: "hi"}
	if user.ClearToolResult() {
		t.Fatal("only tool turns clear")
	}
}

func TestAnthropicCacheBreakpoints(t *testing.T) {
	tools := []wire.AnthropicToolDef{{Name: "Read", InputSchema: map[string]any{}}, {Name: "Grep", InputSchema: map[string]any{}}}
	turns := []Turn{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{ID: "c1", Name: "Read", Args: map[string]any{"path": "a"}}}},
		{Role: "tool", Content: "bytes", ToolCallID: "c1", ToolName: "Read"},
	}
	body := requestBody(t, anthropicCfg("claude-opus-4-5", ""), turns, true, tools)

	sys, ok := body["system"].([]any)
	if !ok || len(sys) != 1 || sys[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system must be a cached block array: %v", body["system"])
	}
	ts := body["tools"].([]any)
	if ts[0].(map[string]any)["cache_control"] != nil || ts[1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("only the last tool carries the breakpoint: %v", ts)
	}
	if tools[1].CacheControl != nil {
		t.Fatal("the shared tool slice must not be marked in place")
	}
	msgs := body["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	if last[len(last)-1].(map[string]any)["cache_control"] == nil {
		t.Fatalf("the newest message's last block carries the breakpoint: %v", last)
	}
	earlier, _ := json.Marshal(msgs[:len(msgs)-1])
	if strings.Contains(string(earlier), "cache_control") {
		t.Fatalf("older messages carry no breakpoint: %s", earlier)
	}

	// A plain-string newest message becomes a cached text block.
	body = requestBody(t, anthropicCfg("claude-opus-4-5", ""), []Turn{{Role: "user", Content: "hi"}}, true, nil)
	content := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("string content should be converted to a cached block: %v", content)
	}
}

func TestCacheBreakpointsAreNativeAnthropicOnly(t *testing.T) {
	cfg := Config{Provider: "cloudflare-ai-gateway", BaseURL: "https://gw.example/compat", APIKey: "k", Model: "claude-sonnet-4-5"}
	body := requestBody(t, cfg, []Turn{{Role: "user", Content: "hi"}}, true, nil)
	b, _ := json.Marshal(body)
	if strings.Contains(string(b), "cache_control") {
		t.Fatalf("gateway route must not send cache_control: %s", b)
	}
}

func TestOpenAIRouteSendsTheCatalogueCap(t *testing.T) {
	hi := []Turn{{Role: "user", Content: "hi"}}
	cfg := Config{Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "sk", Model: "gpt-5"}
	body := requestBody(t, cfg, hi, true, nil)
	if body["max_completion_tokens"] != float64(streamMaxTokens) {
		t.Fatalf("openai streamed cap = %v", body)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("openai must not send max_tokens: %v", body)
	}

	cfg = Config{Provider: "google-gemini", BaseURL: "https://g.example/v1", APIKey: "k", Model: "gemini-2.0-flash"}
	if body = requestBody(t, cfg, hi, true, nil); body["max_tokens"] != float64(8192) {
		t.Fatalf("gemini streamed cap = %v", body["max_tokens"])
	}

	// A relay's model is not trusted to the vendor ceiling.
	cfg = Config{Provider: "github-copilot", BaseURL: "https://c.example", APIKey: "k", Model: "claude-sonnet-4-5"}
	if body = requestBody(t, cfg, hi, true, nil); body["max_tokens"] != nil {
		t.Fatalf("relay without a catalogue cap must leave it to the provider: %v", body["max_tokens"])
	}
}
