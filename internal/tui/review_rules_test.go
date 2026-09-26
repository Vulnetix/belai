package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/commands"
	"github.com/vulnetix/belai/internal/explore"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/scanartifacts"
)

// A review with no admitted report block ends without a triage turn.
func TestReviewNothingToTriage(t *testing.T) {
	a := New(Options{})
	r := testReview(a, "sast", "fix")
	r.scansDone = true
	a.maybeSendReview()
	if a.review != nil || a.pendingReview != nil {
		t.Fatal("a review with nothing admitted must end without triage")
	}
	last := a.messages[len(a.messages)-1].Content
	if !strings.Contains(last, "nothing to triage") {
		t.Fatalf("last line = %q", last)
	}
}

// The closing line counts actionable issues only, never inventories.
func TestReviewCloseCountsIssuesOnly(t *testing.T) {
	a := New(Options{})
	a.preSend = true
	testReview(a, "sbom", "sast", "fix")
	a.handleReviewScan(reviewScanMsg{outcome: commands.ScanOutcome{Name: "sbom", BOM: &scanartifacts.BOMFacts{Components: 558}}})
	a.handleReviewScan(reviewScanMsg{
		outcome: commands.ScanOutcome{Name: "sast", SARIF: &scanartifacts.RunFacts{Results: 2, Counts: scanartifacts.Counts{High: 2}}},
		reports: []explore.ReviewReport{{Scanner: "sast", Label: "sast report", Body: "S1"}},
		atts:    []run.Attachment{{Kind: "file", Label: "sast report", Body: "S1"}},
	})
	a.review.scansDone = true
	a.maybeSendReview()
	var closing string
	for _, m := range a.messages {
		if strings.Contains(m.Content, "vulnetix review done") {
			closing = m.Content
		}
	}
	if !strings.Contains(closing, "2 issues") {
		t.Fatalf("closing line = %q, want 2 issues (sbom's 558 packages are not issues)", closing)
	}
}

// A card lists every report block the classifier withheld and the agent
// reviewing the scanner.
func TestReviewCardWithheldAndAgentLines(t *testing.T) {
	o := commands.ScanOutcome{Name: "sast", Artifact: "sast.sarif", SARIF: &scanartifacts.RunFacts{Results: 1}}
	body := reviewScanCard(o, cardFor(o), []string{"sast report (Unsafe)"}, "belai:vulnetix-scanner@sast#1")
	for _, want := range []string{"withheld from the model: sast report (Unsafe)", "`belai:vulnetix-scanner@sast#1` · f8 to follow", "`.vulnetix/sast.sarif`"} {
		if !strings.Contains(body, want) {
			t.Errorf("card lacks %q:\n%s", want, body)
		}
	}
}

// A failed or timed-out scanner says so above its result.
func TestReviewCardFailureLines(t *testing.T) {
	failed := commands.ScanOutcome{Name: "iac", ExitCode: 2, Err: errors.New("exit status 2")}
	if body := reviewScanCard(failed, cardFor(failed), nil, ""); !strings.Contains(body, "**failed** · exit 2 · exit status 2") {
		t.Fatalf("failed card:\n%s", body)
	}
	timedOut := commands.ScanOutcome{Name: "iac", TimedOut: true}
	if body := reviewScanCard(timedOut, cardFor(timedOut), nil, ""); !strings.Contains(body, "**timed out**") {
		t.Fatalf("timed-out card:\n%s", body)
	}
	// Exit status 1 is a gate verdict: no failure line.
	gate := commands.ScanOutcome{Name: "iac", ExitCode: 1, Err: errors.New("exit status 1")}
	if body := reviewScanCard(gate, cardFor(gate), nil, ""); strings.Contains(body, "failed") {
		t.Fatalf("a gate verdict is not a failure:\n%s", body)
	}
}

// The post-scan fix gets one line naming the mode it ran in.
func TestReviewFixLine(t *testing.T) {
	ok := commands.ScanOutcome{Name: "fix", Duration: 12 * time.Second}
	if l := reviewFixLine(ok, false); !strings.Contains(l, "vulnetix fix --dry-run done") {
		t.Fatalf("dry-run line = %q", l)
	}
	if l := reviewFixLine(ok, true); !strings.Contains(l, "vulnetix fix --yes done") {
		t.Fatalf("autofix line = %q", l)
	}
	if l := reviewFixLine(commands.ScanOutcome{Name: "fix", ExitCode: 3, Err: errors.New("x")}, false); !strings.Contains(l, "failed (exit 3)") {
		t.Fatalf("failed line = %q", l)
	}
	if l := reviewFixLine(commands.ScanOutcome{Name: "fix", TimedOut: true}, false); !strings.Contains(l, "timed out") {
		t.Fatalf("timeout line = %q", l)
	}
}

// A scanner agent's report is capped at 16 KiB on a line boundary.
func TestCapReviewReport(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	long := strings.Repeat(line, 300) // 30,000 bytes
	got := capReviewReport(long)
	if len(got) > explore.ReviewReportBytes+len("\n… (report truncated)") || !strings.HasSuffix(got, "… (report truncated)") {
		t.Fatalf("capped to %d bytes, suffix %q", len(got), got[len(got)-30:])
	}
	// Every line is 100 bytes, so a cut on a line boundary keeps whole lines.
	if kept := strings.TrimSuffix(got, "\n… (report truncated)"); (len(kept)+1)%100 != 0 {
		t.Fatalf("cut mid-line: kept %d bytes", len(kept))
	}
	if capReviewReport("  short  ") != "short" {
		t.Fatal("a short report is trimmed and kept")
	}
}

// Names are flattened, stripped of markup and cut to 48 runes.
func TestCardNameSanitises(t *testing.T) {
	if got := cardName("a`b*c\x1b[1mbold\x1b[0m\nnext"); strings.ContainsAny(got, "`*\x1b\n") {
		t.Fatalf("cardName = %q", got)
	}
	long := strings.Repeat("é", 60)
	if got := cardName(long); len([]rune(got)) != 48 || !strings.HasSuffix(got, "…") {
		t.Fatalf("cut to %d runes: %q", len([]rune(got)), got)
	}
	if got := nameList([]string{"a", "b", "c", "d", "e"}, 9); got != "a, b, c, d +5 more" {
		t.Fatalf("nameList = %q", got)
	}
	if nounCount(1, "package", "packages") != "1 package" || nounCount(246709, "indicator", "indicators") != "246,709 indicators" {
		t.Fatal("nounCount")
	}
}
