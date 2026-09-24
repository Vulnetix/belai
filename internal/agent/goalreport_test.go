package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
)

// runGoalReport runs one goal prompt against the scripted server and returns
// the result plus every event the loop emitted.
func runGoalReport(t *testing.T, opts goalPassOpts) (run.Result, []Event, error) {
	t.Helper()
	srv, _, _ := goalPassServer(t, opts)
	t.Cleanup(srv.Close)
	sess := newGoalPassSession(t, srv, true, 2)
	var events []Event
	res, err := sess.run(context.Background(), nil, TurnInput{Prompt: "ship the thing"}, false, func(e Event) {
		events = append(events, e)
	})
	return res, events, err
}

// A completed goal always ends on a report turn: the reply the user reads is
// the report, not the last words of the pass that earned GOAL_COMPLETE.
func TestGoalReportFollowsAcceptedCompletion(t *testing.T) {
	res, events, err := runGoalReport(t, goalPassOpts{
		eval:   []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_COMPLETE"},
		report: "REPORT: f.txt rewritten; tests pass.",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.GoalSentinel != rolemanager.GoalComplete {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	if res.Reply != "REPORT: f.txt rewritten; tests pass." {
		t.Fatalf("Reply = %q, want the report", res.Reply)
	}
	reportAt, evalAt := -1, -1
	for i, e := range events {
		switch {
		case e.Kind == EventReportKind:
			if e.GoalSentinel != rolemanager.GoalComplete {
				t.Fatalf("report event sentinel = %q, want GOAL_COMPLETE", e.GoalSentinel)
			}
			reportAt = i
		case e.Kind == EventGoalEvalKind && e.GoalSentinel == rolemanager.GoalComplete:
			evalAt = i
		}
	}
	if reportAt < 0 {
		t.Fatal("no EventReportKind emitted after the goal completed")
	}
	if reportAt < evalAt {
		t.Fatalf("report event (%d) must follow the completing verdict (%d)", reportAt, evalAt)
	}
	var streamed strings.Builder
	for _, e := range events[reportAt:] {
		if e.Kind == EventTextKind {
			streamed.WriteString(e.Text)
		}
	}
	if !strings.Contains(streamed.String(), "REPORT:") {
		t.Fatalf("report text was not streamed after the report event: %q", streamed.String())
	}
}

// A loop that stops without completion still reports, with the directive that
// asks what remains unfinished.
func TestGoalReportFollowsStoppedLoop(t *testing.T) {
	res, events, err := runGoalReport(t, goalPassOpts{
		eval:   []string{"garbage", "still garbage", "more garbage", "garbage again"},
		report: "REPORT: stopped early; the release step remains.",
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.GoalSentinel != rolemanager.GoalPartial {
		t.Fatalf("GoalSentinel = %q, want GOAL_PARTIAL", res.GoalSentinel)
	}
	if res.Reply != "REPORT: stopped early; the release step remains." {
		t.Fatalf("Reply = %q, want the stop report", res.Reply)
	}
	var sawReport bool
	for _, e := range events {
		if e.Kind == EventReportKind {
			sawReport = true
			if e.GoalSentinel != rolemanager.GoalPartial {
				t.Fatalf("report event sentinel = %q, want GOAL_PARTIAL", e.GoalSentinel)
			}
		}
	}
	if !sawReport {
		t.Fatal("no EventReportKind emitted when the loop stopped")
	}
}

// A failed report never costs the goal: the loop's own result is returned
// with a warning naming the failure.
func TestGoalReportFailureKeepsResult(t *testing.T) {
	res, events, err := runGoalReport(t, goalPassOpts{
		eval:   []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_COMPLETE"},
		report: "FAIL",
	})
	if err != nil {
		t.Fatalf("a failed report must not fail the goal: %v", err)
	}
	if res.GoalSentinel != rolemanager.GoalComplete {
		t.Fatalf("GoalSentinel = %q, want GOAL_COMPLETE", res.GoalSentinel)
	}
	if strings.TrimSpace(res.Reply) == "" {
		t.Fatal("the pass's own reply must survive a failed report")
	}
	var warned bool
	for _, e := range events {
		if e.Kind == EventWarningKind && strings.Contains(e.Warning, "final report failed") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("a failed report must surface a warning")
	}
}

// The report turn is tool-less in effect: a tool call the model makes anyway
// is never executed.
func TestGoalReportIgnoresToolCalls(t *testing.T) {
	var executed int
	res, events, err := runGoalReport(t, goalPassOpts{eval: []string{"GOAL_PARTIAL", "GOAL_PARTIAL", "GOAL_COMPLETE"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	reportAt := -1
	for i, e := range events {
		if e.Kind == EventReportKind {
			reportAt = i
		}
	}
	if reportAt < 0 {
		t.Fatal("no report event")
	}
	for _, e := range events[reportAt:] {
		if e.Kind == EventToolStartKind || e.Kind == EventToolResultKind {
			executed++
		}
	}
	if executed != 0 {
		t.Fatalf("report turn executed %d tool events, want 0", executed)
	}
	if !strings.Contains(res.Reply, "Ship the release") {
		t.Fatalf("Reply = %q, want the report turn's text", res.Reply)
	}
}

func TestReportDirectiveBySentinel(t *testing.T) {
	if got := reportDirective(rolemanager.GoalComplete); got != goalReportDirective {
		t.Fatalf("complete directive = %q", got)
	}
	for _, s := range []rolemanager.GoalSentinel{rolemanager.GoalPartial, rolemanager.GoalNotStarted, ""} {
		if got := reportDirective(s); got != goalStopReportDirective {
			t.Fatalf("directive for %q = %q, want the stop directive", s, got)
		}
	}
	for _, d := range []string{goalReportDirective, goalStopReportDirective} {
		if !strings.Contains(d, "Do not call any tools") {
			t.Fatalf("report directive must forbid tools: %q", d)
		}
	}
}

func TestWithReplyAppendsOnlyNonEmpty(t *testing.T) {
	base := []run.Turn{{Role: "user", Content: "go"}}
	if got := withReply(base, "  "); len(got) != 1 {
		t.Fatalf("blank reply appended: %+v", got)
	}
	got := withReply(base, "done")
	if len(got) != 2 || got[1].Role != "assistant" || got[1].Content != "done" {
		t.Fatalf("withReply = %+v", got)
	}
}

// A cancelled context skips the report entirely: the user asked to stop.
func TestGoalReportSkippedOnCancel(t *testing.T) {
	s := &Session{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := run.Result{Reply: "work", GoalSentinel: rolemanager.GoalComplete}
	var emitted int
	out := s.goalReport(ctx, "", nil, false, func(Event) { emitted++ }, rolemanager.GoalComplete, in)
	if out.Reply != "work" || emitted != 0 {
		t.Fatalf("cancelled report: reply=%q emitted=%d", out.Reply, emitted)
	}
}
