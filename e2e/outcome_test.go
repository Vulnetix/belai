package e2e

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

const fleetRejecterJSON = `{
  "name": "t-rejecter",
  "description": "Reads the built branch and rejects it",
  "system_prompt": "REJECTER-PERSONA. Review the attached item's branch.",
  "tools": ["Read"],
  "mode": "worker",
  "autonomy": "supervised",
  "max_iterations": 2,
  "kanban": {
    "lists": ["review"],
    "labels": ["needs-review"],
    "on_success": {"list": "done", "drop_labels": ["needs-review"]},
    "on_failure": {"list": "backlog", "labels": ["build"], "drop_labels": ["needs-review"]},
    "lease": "5m"
  },
  "workspace": {"isolation": "worktree"},
  "budget": {"max_passes_per_item": 4}
}`

// A reviewer's rejection is a recorded decision. The goal evaluator in this
// mock calls every goal complete, so the card can only leave review for the
// backlog if the worker's own record routes it: the structured outcome beats an
// evaluator reading the worker's closing words.
func TestFleetReviewerRejectsWithARecordedOutcome(t *testing.T) {
	e := newFleetEnv(t)
	e.importProfiles()
	js := filepath.Join(t.TempDir(), "t-rejecter.json")
	os.WriteFile(js, []byte(fleetRejecterJSON), 0o600)
	e.mustBelai("agent", "import", js)
	short := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build"))

	e.mustBelai("agent", "run", "-trust-dir", "-once", "-provider", "openai", "-model", "test", "t-builder")
	if it := e.item(short); it.List != kanban.Review {
		t.Fatalf("after builder: %s", it.List)
	}

	e.mustBelai("agent", "run", "-once", "-provider", "openai", "-model", "test", "t-rejecter")
	it := e.item(short)
	if it.List != kanban.Backlog || !slices.Contains(it.Labels, "build") || slices.Contains(it.Labels, "needs-review") {
		t.Fatalf("after the rejecting reviewer the item is %s with labels %v, want backlog labelled build", it.List, it.Labels)
	}
	if it.Attempts != 0 || it.Bounces != 1 || it.ClaimedBy != "" {
		t.Fatalf("a rejection is a return, not an attempt: attempts %d bounces %d claimed by %q", it.Attempts, it.Bounces, it.ClaimedBy)
	}
	var notes []string
	for _, h := range it.History {
		notes = append(notes, h.Note)
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"outcome failure: hello.txt has no trailing newline", "recorded failure"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("history lacks %q:\n%s", want, joined)
		}
	}
}
