package agent

import (
	"slices"

	"github.com/vulnetix/belai/internal/run"
)

// noteSent records the turns a provider call sent and the reply it got, when
// the reply finishes the exchange (no tool calls). A reply with tool calls
// leaves nothing recorded: a history ending in an unanswered tool call is not
// one a caller can continue.
func (s *Session) noteSent(turns []run.Turn, a run.Assistant) {
	if s.exploreSubagent {
		return
	}
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	if len(a.ToolCalls) > 0 {
		s.sent = nil
		return
	}
	h := make([]run.Turn, len(turns), len(turns)+1)
	copy(h, turns)
	s.sent = append(h, run.Turn{
		Role:          "assistant",
		Content:       a.Text,
		Thinking:      a.Thinking,
		ThinkingModel: run.ThinkingSource(s.cfg),
	})
}

// clearSent forgets the recorded conversation; a turn starts with none.
func (s *Session) clearSent() {
	s.sentMu.Lock()
	s.sent = nil
	s.sentMu.Unlock()
}

// SentHistory returns the conversation exactly as the provider last saw it:
// every turn of the last call, with the harness's sealed directives and
// attachments, signed thinking, raw tool arguments and any results the loop
// cleared or compacted, followed by the reply that ended the turn. It is nil
// when the last call did not finish the exchange.
//
// A caller that sends this back as the history of its next prompt, rather
// than rebuilding one from what it displayed, sends the provider the bytes it
// already holds in its prompt cache, so only the new prompt is processed.
// The returned slice is a copy the caller may append to.
func (s *Session) SentHistory() []run.Turn {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return slices.Clone(s.sent)
}
