package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// A worker started from a prompt alone, with no stored profile, reaches the same
// board state as the stored builder profile in TestFleetBuilderThenReviewer: it
// claims, works on the item's own branch in a worktree outside the repository,
// commits, and hands the card to review. The harness supplies everything the
// profile would have, so the prompt is only the persona.
func TestFleetPromptOnlyWorkerReachesTheSameOutcome(t *testing.T) {
	e := newFleetEnv(t)
	short := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build"))

	// The usage errors the flags guard.
	if _, errOut, code := e.belai("agent", "run", "-once", "-prompt", "x", "t-builder"); code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatalf("a name and a prompt together: exit %d %s", code, errOut)
	}
	if _, errOut, code := e.belai("agent", "run", "-once", "-tools", "Read"); code != 2 || !strings.Contains(errOut, "-prompt") && !strings.Contains(errOut, "usage") {
		t.Fatalf("-tools without a prompt: exit %d %s", code, errOut)
	}

	e.mustBelai("agent", "run", "-trust-dir", "-once", "-provider", "openai", "-model", "test",
		"-prompt=WRITER-PERSONA. Implement the attached item.", "-tools=Read,Write,update_plan", "-claim=backlog:build", "-to=review")

	it := e.item(short)
	if it.List != kanban.Review || it.ClaimedBy != "" || it.Branch == "" || !strings.HasPrefix(it.Branch, "belai/"+short) {
		t.Fatalf("after the prompt-only worker: %+v", it)
	}
	if got := e.git("show", it.Branch+":hello.txt"); got != "hello" {
		t.Fatalf("branch content %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.repo, "hello.txt")); err == nil {
		t.Fatal("the worker wrote into the main checkout")
	}
	if strings.Contains(e.git("worktree", "list"), e.wt) {
		t.Fatal("the worktree was not removed after release")
	}
	out := e.mustBelai("agent", "ps", "-all", "-json")
	if !strings.Contains(out, `"profile": "adhoc-`) && !strings.Contains(out, `"profile":"adhoc-`) {
		t.Fatalf("the worker is registered under its ad hoc name: %s", out)
	}
}

// agent start -prompt forwards the whole description of the worker to the
// detached child, which registers under its ad hoc name and waits for work.
func TestFleetDetachedPromptOnlyStart(t *testing.T) {
	e := newFleetEnv(t)
	out := e.mustBelai("agent", "start", "-trust-dir", "-stay", "-provider", "openai", "-model", "test",
		"-prompt=-REVIEWER-PERSONA. Check the branch.", "-tools=Read,Grep", "-claim=review:needs-review")
	id, state, _ := strings.Cut(strings.TrimSpace(out), "  ")
	if !strings.HasPrefix(id, "adhoc-") || !strings.Contains(state, "idle") {
		t.Fatalf("start printed %q", out)
	}
	if logs := e.mustBelai("agent", "logs", id); !strings.Contains(logs, "started in") || !strings.Contains(logs, "review") {
		t.Fatalf("the child did not take the claim description: %q", logs)
	}
	if _, errOut, code := e.belai("agent", "start", "-prompt", "x", "t-reviewer"); code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatalf("a name beside -prompt: exit %d %s", code, errOut)
	}
	if out := e.mustBelai("agent", "stop", "-all"); !strings.Contains(out, id+" stopped") {
		t.Fatalf("stop: %s", out)
	}
}
