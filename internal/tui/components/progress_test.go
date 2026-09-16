package components

import (
	"strings"
	"testing"
	"time"
)

func runningBash(cmd string) Message {
	return Message{
		Role:       "tool",
		ToolName:   "Bash",
		ToolArgs:   `{"command":"` + cmd + `"}`,
		ToolCallID: "call-1",
		StartedAt:  time.Now(),
	}
}

// TestRunningToolRowShowsLiveTail: before this, a running command rendered as
// a bare header and a ticking clock for however long it took.
func TestRunningToolRowShowsLiveTail(t *testing.T) {
	msg := runningBash("make build")
	msg.AppendProgress("compiling a\ncompiling b\ncompiling c\ncompiling d")

	out, _ := toolRow(msg, 80, false)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + hint + 3 tail lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "1 earlier lines") {
		t.Fatalf("want a count of what scrolled past, got:\n%s", out)
	}
	for i, want := range []string{"compiling b", "compiling c", "compiling d"} {
		if strings.TrimSpace(lines[2+i]) != want {
			t.Fatalf("line %d = %q, want %q:\n%s", 2+i, lines[2+i], want, out)
		}
	}
}

// TestRunningToolRowKeepsElapsedStatus: the live tail must not make the row
// look finished — no ✓ until the result lands.
func TestRunningToolRowKeepsElapsedStatus(t *testing.T) {
	msg := runningBash("sleep 5")
	msg.AppendProgress("working")
	out, _ := toolRow(msg, 80, false)
	if strings.Contains(out, "✓") {
		t.Fatalf("running row must not show a success glyph:\n%s", out)
	}
}

// TestProgressRingIsBounded stops a runaway command costing unbounded memory,
// while the reported count still covers everything seen.
func TestProgressRingIsBounded(t *testing.T) {
	var msg Message
	for i := 0; i < progressRingLines*4; i++ {
		msg.AppendProgress("line")
	}
	if len(msg.progress) != progressRingLines {
		t.Fatalf("ring holds %d lines, want %d", len(msg.progress), progressRingLines)
	}
	if msg.progressN != progressRingLines*4 {
		t.Fatalf("counted %d lines, want %d", msg.progressN, progressRingLines*4)
	}
	lines, earlier := msg.ProgressTail(3)
	if len(lines) != 3 || earlier != progressRingLines*4-3 {
		t.Fatalf("tail=%d earlier=%d", len(lines), earlier)
	}
}

// TestAppendProgressSplitsCoalescedLines: the agent coalesces a run of
// progress events into one newline-joined payload, so the ring has to split it
// back into lines or the count and the tail would both be wrong.
func TestAppendProgressSplitsCoalescedLines(t *testing.T) {
	var msg Message
	msg.AppendProgress("a\nb\nc")
	if msg.progressN != 3 {
		t.Fatalf("counted %d lines, want 3", msg.progressN)
	}
	lines, _ := msg.ProgressTail(3)
	if strings.Join(lines, "|") != "a|b|c" {
		t.Fatalf("tail = %v", lines)
	}
}

// TestResultSupersedesProgress: once the authoritative result is in, the
// partial view of it is noise and must not linger.
func TestResultSupersedesProgress(t *testing.T) {
	msg := runningBash("seq 4")
	msg.AppendProgress("1\n2\n3")
	msg.SetContent("1\n2\n3\n4")

	if msg.HasProgress() {
		t.Fatal("progress survived the result")
	}
	// The row now renders the result, which is tail-anchored like any other
	// Bash row — so it shows line 4, which the progress tail never held.
	out, _ := toolRow(msg, 80, false)
	if !strings.Contains(out, "4") {
		t.Fatalf("row does not show the final result:\n%s", out)
	}
	if !strings.Contains(out, "1 earlier lines") {
		t.Fatalf("row should truncate the real result:\n%s", out)
	}
}

// TestRunningToolRowIsNeverCached: a running row's content changes on every
// frame, so the render memo must miss it or the tail would freeze.
func TestRunningToolRowIsNeverCached(t *testing.T) {
	msg := runningBash("make")
	msg.AppendProgress("one")
	if key := renderKeyFor(&msg, 80, false); !key.started {
		t.Fatal("a running tool row must be marked started so it is not cached")
	}
	msg.SetContent("done")
	if key := renderKeyFor(&msg, 80, false); key.started {
		t.Fatal("a finished tool row should be cacheable")
	}
}

// TestRunningToolRowInvariants runs the live-tail row through the same LineMap
// contract every other row has to satisfy.
func TestRunningToolRowInvariants(t *testing.T) {
	for _, profile := range renderProfiles {
		withProfile(t, profile, func() {
			for width := 30; width <= 120; width += 7 {
				msg := runningBash("make build")
				msg.AppendProgress("compiling alpha\ncompiling beta\ncompiling gamma\ncompiling delta")
				ml := MessageList{Width: width, ShowTools: true, Messages: []Message{msg}}
				rendered, lm := ml.Render()
				assertLineMapInvariant(t, profileName(profile), rendered, lm, max(width, messageMinWidth))
			}
		})
	}
}
