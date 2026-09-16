package run

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/wire"
)

// ToolCallDelta is a render-only fragment of a streamed tool call. It carries
// no execution authority: the agent executes only the completed set, after
// rolemanager.CheckToolCalls, once the assistant turn has fully arrived.
type ToolCallDelta struct {
	Index int
	ID    string
	Name  string
	Args  string // partial JSON text
}

// toolAccumulator assembles index-keyed tool-call fragments into completed
// rolemanager.ToolCall values. Arguments JSON is parsed only once a block
// closes; a partial fragment never produces a callable tool.
type toolAccumulator struct {
	calls     map[int]*toolBuilder
	openKinds map[int]string // Anthropic content block kind ("tool_use" or "text")
}

type toolBuilder struct {
	id   string
	name string
	args strings.Builder
}

func newToolAccumulator() *toolAccumulator {
	return &toolAccumulator{
		calls:     map[int]*toolBuilder{},
		openKinds: map[int]string{},
	}
}

func (a *toolAccumulator) open(index int, id, name, kind string) {
	b := a.calls[index]
	if b == nil {
		b = &toolBuilder{}
		a.calls[index] = b
	}
	if kind != "" {
		a.openKinds[index] = kind
	}
	if id != "" {
		b.id = id
	}
	if name != "" {
		b.name = name
	}
}

func (a *toolAccumulator) appendArgs(index int, fragment string) {
	b := a.calls[index]
	if b == nil {
		b = &toolBuilder{}
		a.calls[index] = b
	}
	b.args.WriteString(fragment)
}

// complete materialises the accumulated arguments and removes the builder.
// It intentionally does not parse the JSON here: semantic repair (including
// salvage of prefix-truncated JSON) happens later in the agent loop so that
// a malformed call becomes an isError result for that call only, leaving
// sibling calls free to execute.
func (a *toolAccumulator) complete(index int) (rolemanager.ToolCall, error) {
	b := a.calls[index]
	if b == nil {
		return rolemanager.ToolCall{}, fmt.Errorf("tool call %d closed without a start block", index)
	}
	delete(a.calls, index)
	delete(a.openKinds, index)
	return rolemanager.ToolCall{ID: b.id, Name: b.name, RawArgs: b.args.String()}, nil
}

// openedKind returns the kind of the block opened at index, if any.
func (a *toolAccumulator) openedKind(index int) string { return a.openKinds[index] }

// completeAll materialises every accumulated call in index order and empties
// the accumulator. Workers AI has no finish_reason: "tool_calls" on its native
// response shape, so the stream drains open calls on [DONE] instead.
func (a *toolAccumulator) completeAll() ([]rolemanager.ToolCall, error) {
	if len(a.calls) == 0 {
		return nil, nil
	}
	idxs := make([]int, 0, len(a.calls))
	for idx := range a.calls {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	out := make([]rolemanager.ToolCall, 0, len(idxs))
	for _, idx := range idxs {
		call, err := a.complete(idx)
		if err != nil {
			return nil, err
		}
		out = append(out, call)
	}
	return out, nil
}

// drop removes any partial builder at index, used when a non-tool content
// block (e.g. text) stops so the accumulator does not confuse it with a tool
// call.
func (a *toolAccumulator) drop(index int) {
	delete(a.calls, index)
	delete(a.openKinds, index)
}

// streamDelta is the decoded result of one SSE payload.
type streamDelta struct {
	text       string
	reasoning  string
	usage      *transcript.Usage
	toolDelta  *ToolCallDelta
	completed  []rolemanager.ToolCall
	stopReason string
}

// decodeStreamEvent decodes one SSE payload per dialect, updating the
// accumulator and returning any render-only delta or completed calls.
func decodeStreamEvent(d dialect, data string, acc *toolAccumulator) (streamDelta, error) {
	switch d.kind {
	case kindAnthropicMessages:
		return decodeAnthropicEvent(data, acc)
	case kindWorkersAI:
		return decodeWorkersAIEvent(data, acc)
	}
	return decodeOpenAIEvent(data, acc)
}

func decodeOpenAIEvent(data string, acc *toolAccumulator) (streamDelta, error) {
	var chunk wire.OpenAIChatStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return streamDelta{}, err
	}
	var out streamDelta
	if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
		out.usage = &transcript.Usage{
			PromptTokens:     chunk.Usage.PromptTokens,
			CompletionTokens: chunk.Usage.CompletionTokens,
			TotalTokens:      chunk.Usage.TotalTokens,
		}
	}
	if len(chunk.Choices) == 0 {
		return out, nil
	}
	choice := chunk.Choices[0]
	out.text = choice.Delta.Content
	out.reasoning = choice.Delta.ReasoningContent
	if out.reasoning == "" {
		out.reasoning = choice.Delta.Reasoning
	}
	if choice.FinishReason != "" {
		out.stopReason = choice.FinishReason
	}

	for _, tc := range choice.Delta.ToolCalls {
		if acc != nil {
			acc.open(tc.Index, tc.ID, tc.Function.Name, "")
			acc.appendArgs(tc.Index, tc.Function.Arguments)
		}
		out.toolDelta = &ToolCallDelta{
			Index: tc.Index,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Args:  tc.Function.Arguments,
		}
	}

	// finish_reason closes the whole tool-call set. OpenAI signals a tool turn
	// with finish_reason "tool_calls"; a stop reason with no tool fragments
	// leaves completed empty. Sort keys so tool order is deterministic.
	if choice.FinishReason == "tool_calls" && acc != nil {
		idxs := make([]int, 0, len(acc.calls))
		for idx := range acc.calls {
			idxs = append(idxs, idx)
		}
		sort.Ints(idxs)
		for _, idx := range idxs {
			call, err := acc.complete(idx)
			if err != nil {
				return streamDelta{}, err
			}
			out.completed = append(out.completed, call)
		}
	}
	return out, nil
}

// decodeWorkersAIEvent decodes one Workers AI SSE payload. OpenAI-shaped
// gateway payloads delegate to decodeOpenAIEvent; the native
// {"response":"…"} shape maps Response to text, tool_calls to the
// accumulator, and usage to the delta.
func decodeWorkersAIEvent(data string, acc *toolAccumulator) (streamDelta, error) {
	var chunk wire.OpenAIChatStreamChunk
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return streamDelta{}, err
	}
	if len(chunk.Choices) > 0 {
		return decodeOpenAIEvent(data, acc)
	}

	var wc wire.WorkersAIStreamChunk
	if err := json.Unmarshal([]byte(data), &wc); err != nil {
		return streamDelta{}, err
	}
	var out streamDelta
	out.text = wc.Response
	out.reasoning = wc.ReasoningContent
	if wc.Usage != nil && wc.Usage.TotalTokens > 0 {
		out.usage = &transcript.Usage{
			PromptTokens:     wc.Usage.PromptTokens,
			CompletionTokens: wc.Usage.CompletionTokens,
			TotalTokens:      wc.Usage.TotalTokens,
		}
	}
	for _, tc := range wc.ToolCalls {
		if acc != nil {
			acc.open(tc.Index, tc.ID, tc.Function.Name, "")
			acc.appendArgs(tc.Index, tc.Function.Arguments)
		}
		out.toolDelta = &ToolCallDelta{
			Index: tc.Index,
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Args:  tc.Function.Arguments,
		}
	}
	// A payload that populates neither Response nor ToolCalls is a keepalive.
	return out, nil
}

func decodeAnthropicEvent(data string, acc *toolAccumulator) (streamDelta, error) {
	var ev wire.AnthropicStreamEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return streamDelta{}, err
	}
	var out streamDelta

	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock != nil {
			idx := eventIndex(&ev)
			if acc != nil {
				acc.open(idx, ev.ContentBlock.ID, ev.ContentBlock.Name, ev.ContentBlock.Type)
			}
			if ev.ContentBlock.Type == "tool_use" {
				out.toolDelta = &ToolCallDelta{Index: idx, ID: ev.ContentBlock.ID, Name: ev.ContentBlock.Name}
			}
		}
	case "content_block_delta":
		idx := eventIndex(&ev)
		switch ev.Delta.Type {
		case "text_delta":
			out.text = ev.Delta.Text
		case "thinking_delta":
			out.reasoning = ev.Delta.Thinking
		case "input_json_delta":
			if acc != nil {
				acc.appendArgs(idx, ev.Delta.PartialJSON)
			}
			out.toolDelta = &ToolCallDelta{Index: idx, Args: ev.Delta.PartialJSON}
		}
	case "content_block_stop":
		idx := eventIndex(&ev)
		if acc == nil {
			break
		}
		if acc.openedKind(idx) == "tool_use" {
			call, err := acc.complete(idx)
			if err != nil {
				return streamDelta{}, err
			}
			out.completed = append(out.completed, call)
		} else {
			acc.drop(idx)
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			out.stopReason = ev.Delta.StopReason
		}
	}

	if u := anthropicEventUsage(&ev); u != nil {
		out.usage = u
	}
	return out, nil
}

func eventIndex(ev *wire.AnthropicStreamEvent) int {
	if ev.Index != nil {
		return *ev.Index
	}
	return 0
}
