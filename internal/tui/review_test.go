package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/signet/internal/commands"
	"github.com/vulnetix/signet/internal/explore"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/run"
	"github.com/vulnetix/signet/internal/scanartifacts"
	"github.com/vulnetix/signet/internal/tui/components"
)

// testReview installs a running review the way startReview does, without
// launching the CLI.
func testReview(a *App, names ...string) *reviewRun {
	a.reviewSeq++
	a.review = &reviewRun{
		seq:     a.reviewSeq,
		started: time.Now(),
		names:   names,
		done:    map[string]bool{},
		agents:  map[string]string{},
		events:  make(chan tea.Msg, 4),
	}
	return a.review
}

func lastReportCard(a *App) (components.Message, bool) {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == components.ReportRole {
			return a.messages[i], true
		}
	}
	return components.Message{}, false
}

// A finished scanner shows up in the main thread at once, as a card of
// harness facts, and its admitted report is kept for the triage turn.
func TestReviewScanShowsCardImmediately(t *testing.T) {
	a := New(Options{})
	testReview(a, "sast", "secrets", "fix")
	o := commands.ScanOutcome{
		Name: "sast", Duration: 42 * time.Second, Artifact: "sast.sarif", Findings: 3,
		Counts: scanartifacts.Counts{High: 2, Low: 1},
	}
	rep := explore.ReviewReport{Scanner: "sast", Label: "sast report", Body: "S1 high a.go:1"}
	a.handleReviewScan(reviewScanMsg{outcome: o, reports: []explore.ReviewReport{rep}, atts: []run.Attachment{{Kind: "file", Label: "sast report", Body: rep.Body}}})

	card, ok := lastReportCard(a)
	if !ok {
		t.Fatal("a finished scanner must add a report card")
	}
	if card.ToolName != "vulnetix sast" || !strings.Contains(card.Content, "3 findings") || !strings.Contains(card.Content, "2 high · 1 low") || !strings.Contains(card.Content, ".vulnetix/sast.sarif") {
		t.Fatalf("card = %+v", card)
	}
	if strings.Contains(card.Content, "a.go:1") {
		t.Fatalf("the card must carry no finding text: %q", card.Content)
	}
	if a.review.total != 3 || len(a.review.reports) != 1 || len(a.review.atts) != 1 {
		t.Fatalf("review state = %+v", a.review)
	}
	p := a.reviewProgress()
	if p == nil || p.Done != 1 || p.Total != 3 || strings.Join(p.Pending, ",") != "secrets,fix" {
		t.Fatalf("progress = %+v", p)
	}
	if a.footer.Review == nil || a.footer.Review.Done != 1 {
		t.Fatalf("footer review = %+v", a.footer.Review)
	}
}

// A scanner that failed says so on its card; exit status 1 is a gate that
// found something, not a failure.
func TestReviewScanStatus(t *testing.T) {
	if s := reviewScanStatus(commands.ScanOutcome{ExitCode: 1, Err: errors.New("exit status 1")}); s != "" {
		t.Fatalf("a gate verdict is not a failure: %q", s)
	}
	if s := reviewScanStatus(commands.ScanOutcome{ExitCode: 2, Err: errors.New("exit status 2")}); s != "failed" {
		t.Fatalf("exit 2 = %q, want failed", s)
	}
	if s := reviewScanStatus(commands.ScanOutcome{TimedOut: true}); s != "failed" {
		t.Fatalf("timeout = %q, want failed", s)
	}
}

// The triage turn waits for every scanner agent, and carries their reports so
// the session does not run those scanners' subagents again.
func TestReviewTriageWaitsForScannerAgents(t *testing.T) {
	a := New(Options{})
	a.preSend = true // queue rather than send, so the review can be inspected
	r := testReview(a, "sast", "fix")
	r.done["sast"], r.done["fix"] = true, true
	r.reports = []explore.ReviewReport{{Scanner: "sast", Label: "sast report", Body: "S1"}}
	r.atts = []run.Attachment{{Kind: "file", Label: "sast report", Body: "S1"}}
	r.agents["signet:vulnetix-scanner@sast#1"] = "sast"

	a.handleReviewScansDone(reviewScansDoneMsg{err: errors.New("stop before the artifacts view")})
	if a.review == nil || a.pendingReview != nil {
		t.Fatal("the triage turn must wait for the running scanner agent")
	}
	var waiting bool
	for _, m := range a.messages {
		waiting = waiting || (m.Role == "system" && strings.Contains(m.Content, "waiting on the sast review"))
	}
	if !waiting {
		t.Fatal("the main thread must say what the review is waiting on")
	}

	a.handleReviewReport(reviewReportMsg{key: "signet:vulnetix-scanner@sast#1", scanner: "sast", body: "a.go:1 | S1 | real | fix: escape it"})
	card, ok := lastReportCard(a)
	if !ok || card.ToolName != "vulnetix sast review" || !strings.Contains(card.Content, "fix: escape it") {
		t.Fatalf("the agent's report must show in the main thread: %+v", card)
	}
	if a.review != nil {
		t.Fatal("the review must end once every agent reported")
	}
	rs := a.pendingReview
	if rs == nil || len(rs.findings) != 1 || rs.findings[0].Scanner != "sast" || len(rs.atts) != 1 {
		t.Fatalf("queued triage = %+v", rs)
	}
}

// A report the classifier withheld is recorded as reviewed, so the triage
// turn does not run the same review again to get round the verdict.
func TestReviewWithheldReportIsNotRerun(t *testing.T) {
	a := New(Options{})
	r := testReview(a, "sast", "fix")
	r.agents["k"] = "sast"
	a.handleReviewReport(reviewReportMsg{key: "k", scanner: "sast", withheld: rolemanager.Sentinel("UNSAFE")})
	if len(r.findings) != 1 || r.findings[0].Scanner != "sast" || r.findings[0].Body != "" {
		t.Fatalf("findings = %+v, want an empty sast finding", r.findings)
	}
	if _, ok := lastReportCard(a); ok {
		t.Fatal("a withheld report must not render")
	}
}

// Only one review runs at a time.
func TestReviewRefusesASecondRun(t *testing.T) {
	a := New(Options{})
	testReview(a, "sast", "fix")
	if cmd := a.startReview(); cmd != nil {
		t.Fatal("a second review must not start")
	}
	if last := a.messages[len(a.messages)-1]; !strings.Contains(last.Content, "already running") {
		t.Fatalf("last line = %q", last.Content)
	}
}

// Report cards are render-only: no model sees them through the transcript.
func TestReportCardsNeverReachTheModel(t *testing.T) {
	a := New(Options{})
	a.messages = append(a.messages,
		components.Message{Role: "user", Content: "hello"},
		components.Message{Role: components.ReportRole, ToolName: "vulnetix sast", Content: "- 3 findings"},
	)
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "3 findings") {
			t.Fatalf("report card promoted into turn %+v", turn)
		}
	}
}

// While a review runs and no turn is in flight, the composer is a working,
// steering composer: it shows the review's progress and enter steers.
func TestReviewComposerIsWorkingAndSteers(t *testing.T) {
	a := New(Options{})
	a.width, a.height = 160, 40
	a.relayout()
	a.mode = "agent" // no agent engaged: steering must not open the picker
	r := testReview(a, "sast", "secrets", "fix")
	r.done["sast"] = true

	plain := ansi.Strip(a.renderComposer())
	if !strings.Contains(plain, "vulnetix review") || !strings.Contains(plain, "1/3") || !strings.Contains(plain, "steer") {
		t.Fatalf("composer is not the review's working composer:\n%s", plain)
	}

	a.editor.SetValue("focus on the secrets findings")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if len(r.steer) != 1 || r.steer[0] != "focus on the secrets findings" {
		t.Fatalf("steer = %v", r.steer)
	}
	if a.agentPickerOpen {
		t.Fatal("steering a review must not open the agent picker")
	}
	var steered bool
	for _, m := range a.messages {
		steered = steered || (m.Role == "user" && m.Steering && m.Content == "focus on the secrets findings")
	}
	if !steered || a.editor.Value() != "" {
		t.Fatal("the steer must show as a user steering row and clear the composer")
	}

	// The steer rides on the triage prompt.
	a.preSend = true
	r.done["secrets"], r.done["fix"] = true, true
	r.atts = []run.Attachment{{Kind: "file", Label: "sast report", Body: "S1"}}
	r.scansDone = true
	a.maybeSendReview()
	if a.pendingReview == nil || len(a.pendingReview.steer) != 1 {
		t.Fatalf("queued triage = %+v, want the steer", a.pendingReview)
	}
	if p := reviewPromptWith(a.pendingReview.steer); !strings.Contains(p, reviewPrompt) || !strings.Contains(p, "focus on the secrets findings") {
		t.Fatalf("triage prompt = %q", p)
	}
}

// A turn in flight keeps enter for itself: the review is not steered.
func TestReviewSteerYieldsToARunningTurn(t *testing.T) {
	a := New(Options{})
	testReview(a, "sast", "fix")
	a.cancel = func() {}
	if a.reviewSteerable() {
		t.Fatal("a running turn takes the steer, not the review")
	}
}

// esc cancels a review nobody is steering past: the composer and footer drop
// it at once, killed scanners add no cards, and no triage turn runs.
func TestReviewEscCancels(t *testing.T) {
	a := New(Options{})
	r := testReview(a, "sast", "fix")
	cancelled := false
	r.cancel = func() { cancelled = true }
	r.agents["k"] = "sast"

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc})
	if !cancelled || !r.cancelled || a.reviewActive() || len(r.agents) != 0 {
		t.Fatalf("esc must cancel the review: cancelled %v, review %+v", cancelled, r)
	}
	if a.reviewProgress() != nil {
		t.Fatal("a cancelled review must leave the footer")
	}
	if strings.Contains(ansi.Strip(a.renderComposer()), "vulnetix review") {
		t.Fatal("a cancelled review must leave the composer")
	}

	n := len(a.messages)
	a.handleReviewScan(reviewScanMsg{outcome: commands.ScanOutcome{Name: "sast", Err: errors.New("killed")}})
	if len(a.messages) != n {
		t.Fatal("a scanner killed by the cancel must not add a card")
	}
	a.handleReviewReport(reviewReportMsg{key: "k", scanner: "sast", body: "late"})
	if len(a.messages) != n {
		t.Fatal("a stopped agent's late report must not render")
	}
	a.handleReviewScansDone(reviewScansDoneMsg{})
	if a.review != nil || a.pendingReview != nil {
		t.Fatal("a cancelled review must clear without a triage turn")
	}
}
