package agent

import (
	"fmt"

	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/transcript"
)

// Tool-result clearing bounds a session that grows fast before compaction
// (compactThresholdPct, at a pass boundary) gets a chance to run. It replaces
// the old per-request elision, which truncated every result older than three
// iterations on every request: the model lost the exact bytes it had read,
// and because earlier messages changed on every request no provider could
// cache the history.
//
// Clearing is a single step that moves forward in large jumps. Below the
// threshold nothing changes; above it, every tool result except the newest
// keepRecentToolResults is replaced in place with run.ClearedToolResult. The
// replacement is written to the conversation itself, so it is memoised: the
// prefix before the newest results stays byte-identical until the next jump.
const (
	// clearThresholdPct: clear when the estimated context passes this share
	// of the model window.
	clearThresholdPct = 50
	// keepRecentToolResults: the newest tool results that are never cleared.
	keepRecentToolResults = 10
)

// clearStaleToolResults clears old tool results in place when the estimated
// context is past clearThresholdPct of the window. A jump happens only once at
// least keepRecentToolResults new results have accumulated since the last
// one, so a context held over the threshold by assistant text alone cannot
// turn this into a per-iteration rewrite. It returns the number of results
// cleared.
func (s *Session) clearStaleToolResults(turns []run.Turn) int {
	live := 0
	for i := range turns {
		if turns[i].Role == "tool" && turns[i].Content != run.ClearedToolResult {
			live++
		}
	}
	if live < 2*keepRecentToolResults {
		return 0
	}
	msgs := make([]transcript.Message, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, transcript.Message{Role: t.Role, Content: t.Content, ToolName: t.ToolName})
	}
	if transcript.EstimateContext(msgs).Tokens*100 < s.compactWindow()*clearThresholdPct {
		return 0
	}
	cleared, seen := 0, 0
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role != "tool" {
			continue
		}
		seen++
		if seen > keepRecentToolResults && turns[i].ClearToolResult() {
			cleared++
		}
	}
	if cleared > 0 {
		s.traceRecord("clear_tool_results", "", "", fmt.Sprintf("cleared=%d kept=%d", cleared, keepRecentToolResults), 0)
	}
	return cleared
}
