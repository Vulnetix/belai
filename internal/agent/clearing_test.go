package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

func clearingSession(window int) *Session {
	return &Session{
		cfg:      run.Config{Model: "test"},
		settings: config.Settings{ContextWindows: map[string]int{"test": window}},
		live:     posture.NewLive(posture.Defaults(), false),
	}
}

// toolHistory builds n Read iterations whose results are each size runes.
func toolHistory(n, size int) []run.Turn {
	turns := []run.Turn{{Role: "user", Content: "go"}}
	for i := range n {
		id := fmt.Sprintf("c%d", i)
		turns = append(turns,
			run.Turn{Role: "assistant", ToolCalls: []rolemanager.ToolCall{{ID: id, Name: "Read"}}},
			run.Turn{Role: "tool", Content: strings.Repeat("x", size), ToolCallID: id, ToolName: "Read"},
		)
	}
	return turns
}

func liveResults(turns []run.Turn) int {
	n := 0
	for _, t := range turns {
		if t.Role == "tool" && t.Content != run.ClearedToolResult {
			n++
		}
	}
	return n
}

func TestClearStaleToolResultsBelowThresholdKeepsEverything(t *testing.T) {
	turns := toolHistory(30, 400) // ~3000 tokens
	if n := clearingSession(1 << 20).clearStaleToolResults(turns); n != 0 {
		t.Fatalf("cleared %d below the threshold", n)
	}
	if liveResults(turns) != 30 {
		t.Fatal("nothing may change below the threshold")
	}
}

func TestClearStaleToolResultsJumpsAndKeepsTheNewest(t *testing.T) {
	turns := toolHistory(30, 400) // ~3000 tokens against a 4000 window: past 50%
	s := clearingSession(4000)
	if n := s.clearStaleToolResults(turns); n != 30-keepRecentToolResults {
		t.Fatalf("cleared %d, want %d", n, 30-keepRecentToolResults)
	}
	if liveResults(turns) != keepRecentToolResults {
		t.Fatalf("live = %d, want the newest %d", liveResults(turns), keepRecentToolResults)
	}
	last := turns[len(turns)-1]
	if last.Content == run.ClearedToolResult {
		t.Fatal("the newest result must survive")
	}

	// The prefix is stable until enough new results accumulate: a few more
	// iterations never move the boundary.
	prefix := fmt.Sprint(turns)
	turns = append(turns, toolHistory(5, 2000)[1:]...)
	if n := s.clearStaleToolResults(turns); n != 0 {
		t.Fatalf("cleared %d before a full jump accumulated", n)
	}
	if fmt.Sprint(turns[:61]) != prefix {
		t.Fatal("the cleared prefix changed between jumps")
	}
}

func TestClearStaleToolResultsNeedsEnoughResultsForAJump(t *testing.T) {
	turns := toolHistory(keepRecentToolResults+2, 4000)
	if n := clearingSession(1000).clearStaleToolResults(turns); n != 0 {
		t.Fatalf("cleared %d with too few results to make a jump worthwhile", n)
	}
}
