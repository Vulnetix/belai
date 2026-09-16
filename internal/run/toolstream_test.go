package run

import (
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
	if call.RawArgs != `{"path":"x.go"}` {
		t.Fatalf("raw args = %q, want %q", call.RawArgs, `{"path":"x.go"}`)
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
	if delta.completed[0].RawArgs != `{"path":"x.go"}` {
		t.Fatalf("raw args = %q", delta.completed[0].RawArgs)
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
	if len(delta.completed) != 1 || delta.completed[0].ID != "toolu_1" || delta.completed[0].RawArgs != `{"path":"x.go"}` {
		t.Fatalf("completed = %+v", delta.completed)
	}
}

func TestAccumulatorPreservesMalformedJSONForRepair(t *testing.T) {
	acc := newToolAccumulator()
	acc.open(0, "call_1", "Read", "")
	acc.appendArgs(0, `{"path":`)
	call, err := acc.complete(0)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if call.RawArgs != `{"path":` {
		t.Fatalf("raw args = %q", call.RawArgs)
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

func TestDecodeWorkersAIEvent(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindWorkersAI}

	delta, err := decodeStreamEvent(d, `{"response":"workers text"}`, acc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if delta.text != "workers text" {
		t.Fatalf("text = %q", delta.text)
	}
	if delta.reasoning != "" {
		t.Fatalf("unexpected reasoning %q", delta.reasoning)
	}

	// OpenAI-shaped payload delegates to the OpenAI decoder.
	delta, err = decodeStreamEvent(d, `{"choices":[{"delta":{"content":"gateway text"}}]}`, acc)
	if err != nil {
		t.Fatalf("decode gateway: %v", err)
	}
	if delta.text != "gateway text" {
		t.Fatalf("gateway text = %q", delta.text)
	}

	// keepalive with neither response nor tool_calls is an empty delta.
	delta, err = decodeStreamEvent(d, `{}`, acc)
	if err != nil {
		t.Fatalf("decode keepalive: %v", err)
	}
	if delta.text != "" || delta.reasoning != "" || delta.toolDelta != nil {
		t.Fatalf("keepalive delta = %+v", delta)
	}
}

func TestDecodeWorkersAIEventAccumulatesToolCalls(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindWorkersAI}

	delta, err := decodeStreamEvent(d, `{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Read","arguments":"{\"path\":\"a.go\"}"}}]}`, acc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if delta.toolDelta == nil || delta.toolDelta.ID != "call_1" {
		t.Fatalf("toolDelta = %+v", delta.toolDelta)
	}
	calls, err := acc.completeAll()
	if err != nil {
		t.Fatalf("completeAll: %v", err)
	}
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "Read" || calls[0].RawArgs != `{"path":"a.go"}` {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestDecodeWorkersAIReasoning(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindWorkersAI}
	delta, err := decodeStreamEvent(d, `{"reasoning_content":"thinking"}`, acc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if delta.reasoning != "thinking" {
		t.Fatalf("reasoning = %q", delta.reasoning)
	}
}

func TestDecodeOpenAIReasoningFields(t *testing.T) {
	d := dialect{kind: kindOpenAIChat}
	acc := newToolAccumulator()

	delta, err := decodeStreamEvent(d, `{"choices":[{"delta":{"reasoning_content":"deepseek reason"}}]}`, acc)
	if err != nil {
		t.Fatalf("decode reasoning_content: %v", err)
	}
	if delta.reasoning != "deepseek reason" {
		t.Fatalf("reasoning_content = %q", delta.reasoning)
	}

	delta, err = decodeStreamEvent(d, `{"choices":[{"delta":{"reasoning":"openrouter reason"}}]}`, acc)
	if err != nil {
		t.Fatalf("decode reasoning: %v", err)
	}
	if delta.reasoning != "openrouter reason" {
		t.Fatalf("reasoning = %q", delta.reasoning)
	}
}

func TestDecodeAnthropicThinking(t *testing.T) {
	acc := newToolAccumulator()
	d := dialect{kind: kindAnthropicMessages}

	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}`,
		`{"type":"content_block_stop","index":0}`,
	}
	for _, e := range events {
		if _, err := decodeStreamEvent(d, e, acc); err != nil {
			t.Fatalf("decode %s: %v", e, err)
		}
	}
	if len(acc.calls) != 0 {
		t.Fatalf("thinking block must not leave a pending tool call, got %+v", acc.calls)
	}
}

func TestCompleteAllSortsByIndex(t *testing.T) {
	acc := newToolAccumulator()
	acc.open(2, "call_3", "Bash", "")
	acc.appendArgs(2, "{}")
	acc.open(0, "call_1", "Read", "")
	acc.appendArgs(0, `{"path":"a.go"}`)

	calls, err := acc.completeAll()
	if err != nil {
		t.Fatalf("completeAll: %v", err)
	}
	if len(calls) != 2 || calls[0].ID != "call_1" || calls[1].ID != "call_3" {
		t.Fatalf("calls = %+v", calls)
	}
	if len(acc.calls) != 0 {
		t.Fatalf("accumulator not emptied: %+v", acc.calls)
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
