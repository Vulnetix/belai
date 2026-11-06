package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/clijobs"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// The CLI job duty of a worker (kanban.cli, docs/cli-jobs.md). A card labelled
// cli-job:<id> is claimed by a worker whose profile lists the id. The harness,
// not a model, runs the job's fixed argv (internal/clijobs) through the same
// runner as the test suites (no shell, the user's permission rules, the OS
// sandbox and the scrubbed environment), reads the artifacts it leaves and
// records a result of ids, counts and enums on the card. No model turn runs,
// no scanner text reaches the card or a model, and the card's text reaches no
// argument.

// cliMode reports whether this worker works its cards as CLI jobs.
func (w *Worker) cliMode() bool {
	return w.Profile.Kanban != nil && w.Profile.Kanban.CLI != nil
}

// cliBinary is the vulnetix program to run.
func (w *Worker) cliBinary() (string, error) {
	if w.CLIPath != nil {
		return w.CLIPath()
	}
	c, err := vulnetixcli.Detect()
	if err != nil {
		return "", err
	}
	return c.Path, nil
}

// gatesAllMet reports whether the card carries gates and every one is met. A
// mutating job runs only then: the acceptance gate is the human's, or a
// detected suite's, agreement that the change may happen.
func gatesAllMet(it kanban.Item) bool {
	if len(it.Gates) == 0 {
		return false
	}
	for _, g := range it.Gates {
		if g.State != kanban.GateMet {
			return false
		}
	}
	return true
}

func cliBlocked(format string, args ...any) outcome {
	return outcome{blocked: true, note: fmt.Sprintf(format, args...)}
}

// workCLI works one claimed card as a CLI job and releases it.
func (w *Worker) workCLI(ctx, itemCtx context.Context, it kanban.Item, stopRenew func()) {
	o, ws := w.runCLIJob(itemCtx, it)
	discarded := false
	defer func() {
		if ws != nil && ws.Worktree && !discarded {
			w.discard(ctx, it, ws)
		}
	}()
	if ws != nil && ws.Worktree && !o.failed && !o.blocked && o.files > 0 {
		o = w.verifyCLI(itemCtx, it, ws, o)
	}
	stopRenew()
	if w.stopped(ctx, itemCtx, it, ws) {
		return
	}
	// A card released to review carries a branch the factory opens a pull
	// request from, so the branch must be on origin before the worktree goes.
	// Only a card released to done is published by the harness itself.
	if ws != nil && ws.Worktree && !o.failed && !o.blocked && o.files > 0 && w.publishes() && !w.routesToDone(o) {
		if branch, note := w.pushProgress(ctx, it, ws); branch != "" {
			o.note += note
		} else {
			o.note += "; " + ws.Branch + " could not be pushed to origin, so no pull request can be opened from it until it is"
		}
	}
	publish := ws != nil && ws.Worktree && !o.failed && !o.blocked && o.files > 0 && w.publishes() && w.routesToDone(o)
	if ws != nil && ws.Worktree && !publish {
		w.discard(ctx, it, ws)
		discarded = true
	}
	released := w.release(ctx, it, o)
	if publish && released.List == kanban.Done {
		w.publish(ctx, ws, released, o.files)
	}
}

// verifyCLI runs the card's gates on the branch a mutating job committed, as
// any other worker's branch is verified (kanban.gates.verify). The gates were
// all met before the job ran; this decides them again at the new head, so a
// change that breaks a suite is a failed attempt and a card whose gates cannot
// run is held for a person, whatever the job's exit code said.
func (w *Worker) verifyCLI(itemCtx context.Context, it kanban.Item, ws *Workspace, o outcome) outcome {
	mode := w.gatesMode()
	if mode == "off" || itemCtx.Err() != nil {
		return o
	}
	v := w.verifyBranch(itemCtx, it, ws)
	if c := context.Cause(itemCtx); c != nil {
		return outcome{failed: true, branch: o.branch, files: o.files, note: "the item's budget ended while its gates were verified: " + c.Error()}
	}
	return w.applyVerification(o, v, mode)
}

// runCLIJob runs the card's job and returns the outcome, with the worktree a
// mutating job ran in (nil for a read-only job).
func (w *Worker) runCLIJob(ctx context.Context, it kanban.Item) (outcome, *Workspace) {
	spec := w.Profile.Kanban.CLI
	id, ok := clijobs.JobOfLabels(it.Labels)
	if !ok {
		return cliBlocked("the card carries no %s<id> label, so there is no CLI job to run", clijobs.Label), nil
	}
	job, known := clijobs.Lookup(id)
	switch {
	case !known:
		return cliBlocked("%s%s is not a CLI job", clijobs.Label, sanitize.Line(id, 40)), nil
	case !slices.Contains(spec.Jobs, id):
		return cliBlocked("CLI job %s is not in this profile's kanban.cli.jobs", id), nil
	case job.Mutates && !spec.Write:
		return cliBlocked("CLI job %s changes things outside .vulnetix and this profile does not set kanban.cli.write", id), nil
	case job.Mutates && !gatesAllMet(it):
		return cliBlocked("CLI job %s is mutating and needs the card to carry acceptance gates that are all met; they are not", id), nil
	}
	bin, err := w.cliBinary()
	if err != nil {
		return cliBlocked("the vulnetix CLI is not available on this host"), nil
	}
	head, _ := w.headRef(ctx)

	dir := w.Repo
	var ws *Workspace
	if job.Mutates {
		ws, err = w.workspace(ctx, it)
		if err != nil {
			return outcome{failed: true, fault: Fault{Code: FaultWorkspace}, note: "the workspace could not be prepared: " + sanitize.Sanitize(err.Error())}, nil
		}
		if !ws.Worktree {
			return cliBlocked("mutating CLI job %s needs workspace.isolation: worktree", id), nil
		}
		dir = ws.Dir
		w.Record.Branch = ws.Branch
		w.save()
		head, _ = ws.Head(ctx)
	}

	for _, n := range job.Needs {
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(n)))
		if err != nil || !fi.Mode().IsRegular() {
			w.logf("%s: cli job %s: input %s is missing; recording exit %d", it.Short(), id, n, clijobs.ExitNotEvaluable)
			return w.cliSuccess(ctx, job, dir, head, clijobs.ExitNotEvaluable, nil, ws, it), ws
		}
	}

	job.ClearArtifacts(dir)
	plan := testrun.Plan{}
	for i, argv := range job.Argvs(bin) {
		plan.Names = append(plan.Names, fmt.Sprintf("%s#%d", id, i+1))
		plan.Argvs = append(plan.Argvs, argv)
	}
	w.logf("%s: running cli job %s (%d step(s))", it.Short(), id, len(plan.Argvs))
	results := w.testResultsAt(ctx, dir, plan)
	if len(results) != len(plan.Argvs) {
		return outcome{failed: true, fault: Fault{Code: FaultError}, note: fmt.Sprintf("CLI job %s was interrupted before every step ran", id)}, ws
	}
	var codes []int
	var outs clijobs.Outputs
	for _, r := range results {
		switch r.Status {
		case testrun.Denied, testrun.NeedsApproval:
			return cliBlocked("a permission rule refused CLI job %s (%s); add an allow rule for the vulnetix command to run it", id, r.Status), ws
		case testrun.Errored:
			return outcome{failed: true, fault: Fault{Code: FaultError}, note: fmt.Sprintf("CLI job %s could not be started: %s", id, r.Note)}, ws
		case testrun.TimedOut:
			return outcome{failed: true, fault: Fault{Code: FaultError}, note: fmt.Sprintf("CLI job %s timed out (%s)", id, r.Note)}, ws
		}
		codes = append(codes, r.ExitCode)
		outs = append(outs, []byte(r.Output))
		w.logf("%s: cli job %s step %s exit %d in %s", it.Short(), id, r.Suite, r.ExitCode, r.Duration.Round(1e6))
	}
	exit := clijobs.ExitCode(codes)
	if exit != clijobs.ExitOK && exit != clijobs.ExitGateBreach && exit != clijobs.ExitNotEvaluable {
		// A usage error or a missing binary says nothing about the repository,
		// and whatever the job left may predate the run.
		return outcome{failed: true, fault: Fault{Code: FaultError}, note: fmt.Sprintf("CLI job %s failed with exit %d; no result was recorded", id, exit)}, ws
	}
	return w.cliSuccess(ctx, job, dir, head, exit, outs, ws, it), ws
}

// removeUntrackedScanOutput deletes the untracked, unignored files the job left
// under .vulnetix, except the ones it lists in Commits. Tracked files are never
// touched, so a repository's own .vulnetix configuration is not shown as deleted.
func (w *Worker) removeUntrackedScanOutput(ctx context.Context, ws *Workspace, job clijobs.Job) {
	root := filepath.Join(ws.Dir, ".vulnetix")
	if fi, err := os.Lstat(root); err != nil || !fi.IsDir() {
		return
	}
	out, err := ws.run(ctx, ws.Dir, "git", "ls-files", "--others", "--exclude-standard", "-z", "--", ".vulnetix")
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, k := range job.Commits {
		keep[".vulnetix/"+k] = true
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || keep[p] || !strings.HasPrefix(p, ".vulnetix/") {
			continue
		}
		_ = os.Remove(filepath.Join(ws.Dir, filepath.FromSlash(p)))
	}
}

// cliSuccess builds the result and the outcome for a job that ran (or was not
// evaluable). The job's exit code is data on the card, not a failure of the
// worker: a gate breach is a result.
func (w *Worker) cliSuccess(ctx context.Context, job clijobs.Job, dir, head string, exit int, outs clijobs.Outputs, ws *Workspace, it kanban.Item) outcome {
	res := clijobs.Build(ctx, job, dir, head, exit, outs)
	doc, err := res.JSON()
	if err != nil {
		return outcome{failed: true, fault: Fault{Code: FaultError}, note: "the CLI job result could not be encoded"}
	}
	o := outcome{cliResult: doc, note: fmt.Sprintf("CLI job %s: exit %d, %d findings, %d signals", job.ID, res.ExitCode, len(res.Findings), len(res.Signals))}
	if ws != nil && ws.Worktree {
		// The scan output is not part of the branch; the job's own change is.
		w.removeUntrackedScanOutput(context.WithoutCancel(ctx), ws, job)
		bg := context.WithoutCancel(ctx)
		msg := fmt.Sprintf("belai: cli job %s for %s\n\nAgent %s, kanban item %s.", job.ID, it.Short(), w.Profile.Name, it.Short())
		if _, err := ws.Commit(bg, msg); err != nil {
			w.logf("%s: commit: %v", it.Short(), err)
			return outcome{failed: true, fault: Fault{Code: FaultError}, note: "the change could not be committed: " + sanitize.Line(err.Error(), 200)}
		}
		o.files = ws.FilesChanged(bg)
		o.branch = ws.Branch
		if o.files == 0 {
			o.note += "; no files changed"
		}
	}
	return o
}
