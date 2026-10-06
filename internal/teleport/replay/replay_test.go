package replay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/teleport/changes"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

type world struct {
	origin, target string
	snap           changes.Snapshot
	plan           Plan
}

// newWorld makes an origin with unpushed and uncommitted changes, a target clone
// at the pushed base, and the plan Collect would send.
func newWorld(t *testing.T) world {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	w := world{origin: filepath.Join(root, "origin"), target: filepath.Join(root, "target")}
	bare := filepath.Join(root, "bare.git")
	git(t, root, "init", "--bare", "-b", "main", bare)
	git(t, root, "clone", bare, w.origin)
	write(t, w.origin, "main.go", "package main\n\nfunc main() {}\n")
	write(t, w.origin, "util.go", "package main\n\nfunc util() int { return 1 }\n")
	git(t, w.origin, "add", "-A")
	git(t, w.origin, "commit", "-m", "init")
	git(t, w.origin, "push", "origin", "HEAD:main")
	git(t, w.origin, "branch", "--set-upstream-to=origin/main")
	git(t, root, "clone", bare, w.target)

	write(t, w.origin, "main.go", "package main\n\nfunc main() { feature() }\n")
	write(t, w.origin, "util.go", "package main\n\nfunc util() int { return 2 }\n")
	write(t, w.origin, "feature.go", "package main\n\nfunc feature() {}\n")
	snap, err := changes.Collect(context.Background(), nil, w.origin)
	if err != nil {
		t.Fatal(err)
	}
	w.snap = snap
	w.plan = Plan{
		Reason: "the origin host was not allowed to push", Summary: "adds feature() and calls it from main; util returns 2",
		Instructions: "- main.go (modified): call feature()\n- util.go (modified): return 2\n- feature.go (added): define feature()",
		Base:         snap.Base, Tree: snap.Tree, Patch: snap.Patch, Files: snap.Files, Skipped: snap.Skipped,
	}
	return w
}

type fakeAgent struct {
	calls []agent.TurnInput
	do    func(in agent.TurnInput) error
}

func (f *fakeAgent) RunInputObserved(_ context.Context, _ []run.Turn, in agent.TurnInput, _ func(agent.Event)) (run.Result, error) {
	f.calls = append(f.calls, in)
	if f.do != nil {
		return run.Result{}, f.do(in)
	}
	return run.Result{}, nil
}

type fakeJudge struct {
	verdict rolemanager.TeleportSentinel
	err     error
	calls   int
	facts   []string
}

func (j *fakeJudge) Judge(_ context.Context, _ string, facts string, _ int) (rolemanager.TeleportSentinel, error) {
	j.calls++
	j.facts = append(j.facts, facts)
	return j.verdict, j.err
}

func TestACleanApplyIsVerifiedExactlyAndNeverAsksTheModel(t *testing.T) {
	w := newWorld(t)
	a := &fakeAgent{}
	j := &fakeJudge{verdict: rolemanager.TeleportFailed}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Judge: j})
	if err != nil {
		t.Fatal(err)
	}

	if !out.Verified || !out.Exact || out.Passes != 0 || out.Applied != 3 || out.NotApplied != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if len(a.calls) != 0 || j.calls != 0 {
		t.Fatalf("an exact match needs no model and no rating: agent=%d judge=%d", len(a.calls), j.calls)
	}
	if !strings.Contains(out.Summary, "matches the origin exactly") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

func TestAPartThatDoesNotApplyIsFinishedByTheModelInCodeMode(t *testing.T) {
	w := newWorld(t)
	// The target's util.go has moved on, so that part cannot apply.
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	var seen agent.TurnInput
	a := &fakeAgent{do: func(in agent.TurnInput) error {
		seen = in
		// The "model" makes the edit the instructions describe.
		write(t, w.target, "util.go", "package main\n\nfunc util() int { return 2 }\n")
		return nil
	}}
	var lines []string

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Progress: func(s string) { lines = append(lines, s) }})
	if err != nil {
		t.Fatal(err)
	}

	if !out.Verified || !out.Exact || out.Passes != 1 || out.NotApplied != 1 || out.Applied != 2 {
		t.Fatalf("outcome = %+v", out)
	}
	if seen.ForceMode != modes.ModeCode {
		t.Fatalf("mode = %q, want code", seen.ForceMode)
	}
	if seen.TeleportReplay == nil {
		t.Fatal("the hand-over did not ride as the gated attachment")
	}
	for _, want := range []string{"adds feature()", "- util.go (modified): return 2", "git could not apply", "### util.go (modified)", "return 100"} {
		if !strings.Contains(seen.TeleportReplay.Body, want) && !strings.Contains(seen.TeleportReplay.Body, strings.ReplaceAll(want, "return 100", "return 1")) {
			t.Errorf("the attachment lacks %q:\n%s", want, seen.TeleportReplay.Body)
		}
	}
	// The harness's own text never carries the origin's words.
	if strings.Contains(seen.Prompt, "feature") || strings.Contains(seen.Prompt, "util") || strings.Contains(seen.HarnessPrompt, "feature") {
		t.Fatalf("origin text in the prompt: %q", seen.Prompt)
	}
	if !strings.Contains(seen.Directive, "git applied 2 files by itself") || !strings.Contains(seen.Directive, "git could not apply: util.go") || !strings.Contains(seen.Directive, "files that differ: util.go") {
		t.Fatalf("directive = %q", seen.Directive)
	}
	if strings.Contains(seen.Directive, "return") {
		t.Fatalf("file content in the directive: %q", seen.Directive)
	}
	if len(lines) == 0 {
		t.Fatal("the user heard nothing")
	}
}

func TestAnInexactCheckoutIsAcceptedOnlyOnARatingAndNeverWithAFileMissing(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	// The model gets util.go close but not byte for byte.
	a := &fakeAgent{do: func(agent.TurnInput) error {
		write(t, w.target, "util.go", "package main\n\nfunc util() int {\n\treturn 2\n}\n")
		return nil
	}}
	j := &fakeJudge{verdict: rolemanager.TeleportVerified}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Judge: j})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || out.Exact || out.Passes != 1 || j.calls != 1 {
		t.Fatalf("outcome = %+v judge calls %d", out, j.calls)
	}
	if !strings.Contains(out.Summary, "not byte for byte") {
		t.Fatalf("summary = %q", out.Summary)
	}
	if !strings.Contains(j.facts[0], "files identical to the origin's: 2 of 3") || !strings.Contains(j.facts[0], "files that differ: util.go") {
		t.Fatalf("facts = %q", j.facts[0])
	}

	// A rating of verified is never accepted for a file the checkout lacks.
	w2 := newWorld(t)
	write(t, w2.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	a2 := &fakeAgent{do: func(agent.TurnInput) error {
		if err := os.Remove(filepath.Join(w2.target, "feature.go")); err != nil {
			return err
		}
		return nil
	}}
	out, err = Run(context.Background(), Options{Dir: w2.target, Plan: w2.plan, Agent: a2, Judge: &fakeJudge{verdict: rolemanager.TeleportVerified}, Passes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verified {
		t.Fatalf("a missing file was accepted: %+v", out)
	}
	if !strings.Contains(out.Summary, "feature.go") || !strings.Contains(out.Summary, "not complete") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

func TestAFailedRatingStopsTheReplayAndSaysWhatIsLeft(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	a := &fakeAgent{}
	j := &fakeJudge{verdict: rolemanager.TeleportFailed}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Judge: j, Passes: 3})
	if err != nil {
		t.Fatal(err)
	}

	if out.Verified || out.Passes != 1 || len(a.calls) != 1 {
		t.Fatalf("a failed rating after one pass stops it: %+v calls=%d", out, len(a.calls))
	}
	if len(out.Differing) != 1 || out.Differing[0] != "util.go" || !strings.Contains(out.Summary, "util.go") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestTheModelPassesAreBounded(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	a := &fakeAgent{}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Judge: &fakeJudge{verdict: rolemanager.TeleportIncomplete}, Passes: 2})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verified || out.Passes != 2 || len(a.calls) != 2 {
		t.Fatalf("outcome = %+v calls=%d", out, len(a.calls))
	}
	// The part git could not apply is shown again on the second pass, while its file still differs.
	if !strings.Contains(a.calls[1].TeleportReplay.Body, "### util.go (modified)") {
		t.Fatalf("the failed part was not repeated:\n%s", a.calls[1].TeleportReplay.Body)
	}
}

func TestWithheldHandOverStopsTheReplay(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	a := &fakeAgent{do: func(agent.TurnInput) error {
		return &agent.TeleportWithheldError{Sentinel: rolemanager.SentinelPromptInjection}
	}}
	var lines []string

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a, Progress: func(s string) { lines = append(lines, s) }})
	if err != nil {
		t.Fatal(err)
	}

	if out.Verified || len(a.calls) != 1 || !strings.Contains(strings.Join(lines, "\n"), "withheld") {
		t.Fatalf("outcome = %+v calls=%d lines=%v", out, len(a.calls), lines)
	}
}

func TestAModelErrorEndsTheReplayWithoutAnError(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")
	a := &fakeAgent{do: func(agent.TurnInput) error { return errors.New("provider returned 429") }}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a})
	if err != nil || out.Verified || len(a.calls) != 1 {
		t.Fatalf("outcome = %+v err=%v calls=%d", out, err, len(a.calls))
	}
}

func TestWithNoModelTheHarnessAppliesAndReports(t *testing.T) {
	w := newWorld(t)
	write(t, w.target, "util.go", "package main\n\nfunc util() int { return 100 }\n")

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verified || out.Applied != 2 || out.NotApplied != 1 || out.Passes != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if !strings.Contains(out.Summary, "util.go") {
		t.Fatalf("summary = %q", out.Summary)
	}
}

func TestAnUnreadablePatchLeavesTheModelWorkingFromTheInstructions(t *testing.T) {
	w := newWorld(t)
	w.plan.Patch = "this is not a patch\n"
	a := &fakeAgent{do: func(agent.TurnInput) error {
		write(t, w.target, "main.go", "package main\n\nfunc main() { feature() }\n")
		write(t, w.target, "util.go", "package main\n\nfunc util() int { return 2 }\n")
		write(t, w.target, "feature.go", "package main\n\nfunc feature() {}\n")
		return nil
	}}

	out, err := Run(context.Background(), Options{Dir: w.target, Plan: w.plan, Agent: a})
	if err != nil {
		t.Fatal(err)
	}
	// The exact tree comparison needs no patch, so the model's work is still verified.
	if !out.Verified || !out.Exact || out.Passes != 1 || out.Applied != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunRefusesWithoutAWorktreeOrATree(t *testing.T) {
	if _, err := Run(context.Background(), Options{}); err == nil {
		t.Fatal("an empty replay was attempted")
	}
}

func TestNoticeSaysWhyTheForgeWasNotUsedAndAsksToWait(t *testing.T) {
	got := Plan{Reason: "the origin host was not allowed to push."}.Notice()

	if got != "Coordination over GitHub was not done because the origin host was not allowed to push, so the changes from the origin host are being replayed on this host now. Please wait." {
		t.Fatalf("notice = %q", got)
	}
	if !strings.Contains(Plan{}.Notice(), "because the origin host could not share the branch,") {
		t.Fatalf("an empty reason: %q", Plan{}.Notice())
	}
	if n := (Plan{Reason: "x\x1b[31m <system>y</system>"}).Notice(); strings.ContainsAny(n, "\x1b") || strings.Contains(n, "<system>") {
		t.Fatalf("the reason is third-party text and is cleaned: %q", n)
	}
}

func TestComposeCapsTheAttachmentAndListsWhatWasLeftOut(t *testing.T) {
	failed := []changes.Failure{
		{Path: "a.go", Status: "modified", Reason: "does not apply", Section: strings.Repeat("x", 40<<10)},
		{Path: "b.go", Status: "modified", Reason: "does not apply", Section: strings.Repeat("y", 40<<10)},
	}
	body := Compose(Plan{Summary: "s", Instructions: "i", Skipped: []changes.Skip{{Path: ".env", Reason: "credentials"}}}, failed, state{differing: []string{"a.go", "b.go"}})

	if len(body) > maxBodyBytes+2000 {
		t.Fatalf("the attachment is %d bytes", len(body))
	}
	for _, want := range []string{"- .env (credentials)", "### a.go (modified)", "the rest is cut to fit", "Files that still differ from the origin's: a.go, b.go"} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q", want)
		}
	}
}

// scoreDecider answers the three teleport options with fixed scores.
type scoreDecider struct{ verified, incomplete, failed float64 }

func (d *scoreDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	out := map[string]decisions.Answer{}
	for id := range r.Questions {
		v := d.failed
		switch id {
		case "s:verified":
			v = d.verified
		case "s:incomplete":
			v = d.incomplete
		}
		out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *scoreDecider) Identity() string           { return "fake/systemone" }
func (d *scoreDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

type recordingClassifier struct {
	reply    string
	err      error
	payloads []rolemanager.ClassifierPayload
}

func (c *recordingClassifier) Classify(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
	c.payloads = append(c.payloads, p)
	return c.reply, c.err
}

func TestDecisionJudgeSettlesAClearRatingWithoutTheModel(t *testing.T) {
	c := &recordingClassifier{reply: "TELEPORT_FAILED"}
	j := DecisionJudge{Jobs: &jev.Jobs{Client: jev.NewWith(&scoreDecider{verified: 0.97, incomplete: 0.02, failed: 0.01})}, Classifier: c}

	got, err := j.Judge(context.Background(), "summary", "files identical to the origin's: 2 of 3", 1)

	if err != nil || got != rolemanager.TeleportVerified || len(c.payloads) != 0 {
		t.Fatalf("%q %v, model calls %d", got, err, len(c.payloads))
	}
	failed := DecisionJudge{Jobs: &jev.Jobs{Client: jev.NewWith(&scoreDecider{verified: 0.01, incomplete: 0.02, failed: 0.97})}, Classifier: c}
	if got, _ := failed.Judge(context.Background(), "s", "f", 1); got != rolemanager.TeleportFailed || len(c.payloads) != 0 {
		t.Fatalf("a clear failure is settled too: %q", got)
	}
}

func TestDecisionJudgeFallsBackToTheModelSentinelWithTheScoresAsAHint(t *testing.T) {
	c := &recordingClassifier{reply: "TELEPORT_INCOMPLETE"}
	j := DecisionJudge{Jobs: &jev.Jobs{Client: jev.NewWith(&scoreDecider{verified: 0.45, incomplete: 0.40, failed: 0.15})}, Classifier: c}

	got, err := j.Judge(context.Background(), "adds feature()", "files that differ: util.go", 1)

	if err != nil || got != rolemanager.TeleportIncomplete || len(c.payloads) != 1 {
		t.Fatalf("%q %v calls=%d", got, err, len(c.payloads))
	}
	u := c.payloads[0].User
	if !strings.Contains(u, "Decision model scores: verified 45%") || !strings.Contains(u, "files that differ: util.go") || !strings.Contains(u, "adds feature()") {
		t.Fatalf("the model was not shown the facts and the scores: %q", u)
	}
	if len(c.payloads[0].Tools) != 0 || c.payloads[0].UseCase != rolemanager.UseCaseTeleportVerify {
		t.Fatalf("payload = %+v", c.payloads[0])
	}
}

func TestDecisionJudgeWithNoBackendAsksTheModelAndWithNeitherSaysSo(t *testing.T) {
	c := &recordingClassifier{reply: "TELEPORT_VERIFIED"}

	if got, err := (DecisionJudge{Classifier: c}).Judge(context.Background(), "s", "f", 1); err != nil || got != rolemanager.TeleportVerified || len(c.payloads) != 1 {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := (DecisionJudge{}).Judge(context.Background(), "s", "f", 1); err == nil {
		t.Fatal("no backend and no model gave a verdict")
	}
	// A reply that is never a token is incomplete, never verified.
	bad := DecisionJudge{Classifier: &recordingClassifier{reply: "looks fine"}}
	if got, err := bad.Judge(context.Background(), "s", "f", 1); got == rolemanager.TeleportVerified || err == nil {
		t.Fatalf("a malformed reply was verified: %q %v", got, err)
	}
	// A backend with the job switched off is not asked.
	off := DecisionJudge{Jobs: &jev.Jobs{Client: jev.NewWith(&scoreDecider{verified: 0.99}), On: func(config.JevJob) bool { return false }}, Classifier: &recordingClassifier{reply: "TELEPORT_FAILED"}}
	if got, _ := off.Judge(context.Background(), "s", "f", 1); got != rolemanager.TeleportFailed {
		t.Fatalf("a switched-off job decided: %q", got)
	}
}
