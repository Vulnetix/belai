package run

import (
	"strings"
	"testing"
)

func TestToolAccumulatorOpenAI(t *testing.T) {
	acc := newToolAccumulator()
	acc.open(0, "call_1", "Read", "")
	acc.appendArgs(0, `{"pa`)
	acc.appendArgs(0, `th":"x.go"}`)

	call, err := acc.complete(0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if call.ID != "call_1" || call.Name != "Read" {
		t.Fatalf("call = %+v", call)
	}
	if call.Args["path"] != "x.go" {
		t.Fatalf("args = %+v", call.Args)
	}
}

func TestDecodeOpenAIAccumulatesSplitJSON(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindOpenAIChat}

	if _, err := decodeStreamEvent(d, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":"}}]}}]}`, acc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := decodeStreamEvent(d, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x.go\"}"}}]}}]}`, acc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	delta, err := decodeStreamEvent(d, `{"choices":[{"finish_reason":"tool_calls"}]}`, acc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(delta.completed) != 1 {
		t.Fatalf("completed = %+v", delta.completed)
	}
	if delta.completed[0].Args["path"] != "x.go" {
		t.Fatalf("args = %+v", delta.completed[0].Args)
	}
}

func TestDecodeAnthropicAccumulatesToolUse(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindAnthropicMessages}

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"x.go\"}"}}`,
	}
	for _, e := range events {
		if _, err := decodeStreamEvent(d, e, acc); err != nil {
			t.Fatalf("decode %s: %v", e, err)
		}
	}
	delta, err := decodeStreamEvent(d, `{"type":"content_block_stop","index":0}`, acc)
	if err != nil {
		t.Fatalf("decode stop: %v", err)
	}
	if len(delta.completed) != 1 || delta.completed[0].ID != "toolu_1" || delta.completed[0].Args["path"] != "x.go" {
		t.Fatalf("completed = %+v", delta.completed)
	}
}

func TestAccumulatorMalformedJSONFailsClosed(t *testing.T) {
	acc := newToolAccumulator()
	acc.open(0, "call_1", "Read", "")
	acc.appendArgs(0, `{"path":`)
	if _, err := acc.complete(0); err == nil || !strings.Contains(err.Error(), "malformed tool arguments") {
		t.Fatalf("expected malformed-tool-arguments error, got %v", err)
	}
}

func TestDecodeAnthropicTextStopDoesNotComplete(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindAnthropicMessages}

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, e := range events {
		if _, err := decodeStreamEvent(d, e, acc); err != nil {
			t.Fatalf("decode %s: %v", e, err)
		}
	}
	if len(acc.calls) != 0 {
		t.Fatalf("expected no pending tool calls, got %+v", acc.calls)
	}
}

func TestDecodeOpenAICompletesToolCallsInOrder(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindOpenAIChat}

	// two parallel tool-call fragments arriving out of index order
	if _, err := decodeStreamEvent(d, `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"Bash","arguments":"{}"}}]}}]}`, acc); err != nil {
		t.Fatalf("decode 1: %v", err)
	}
	if _, err := decodeStreamEvent(d, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"a.go\"}"}}]}}]}`, acc); err != nil {
		t.Fatalf("decode 0: %v", err)
	}
	delta, err := decodeStreamEvent(d, `{"choices":[{"finish_reason":"tool_calls"}]}`, acc)
	if err != nil {
		t.Fatalf("decode finish: %v", err)
	}
	if len(delta.completed) != 2 {
		t.Fatalf("completed count = %d", len(delta.completed))
	}
	if delta.completed[0].ID != "call_1" || delta.completed[1].ID != "call_2" {
		t.Fatalf("order = %+v", delta.completed)
	}
}
