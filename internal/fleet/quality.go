package fleet

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/sandbox"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// The quality sweep of a worker (kanban.quality). The harness runs the
// repository's detected or configured test suites, ties what they show to the
// commit they ran on, and files seed cards from the facts. No model runs a
// suite or decides whether one ran, and no test output reaches a card: a card
// holds test and package names, percentages and harness constants.
//
// Its gates are the crew's (one live crew per repository) and the record: a
// quality record for HEAD means the suites already ran on this commit, so they
// do not run again. The seed cards are filed once per subject, so a crew
// launched again, on the same commit or a later one, never doubles the board.

// qualityStep runs the quality sweep once for each HEAD this worker sees.
func (w *Worker) qualityStep(ctx context.Context) {
	if w.Profile.Kanban == nil || w.Profile.Kanban.Quality == nil || !w.Profile.Kanban.Quality.Sweep || w.Item != "" {
		return
	}
	head, err := w.headRef(ctx)
	if err != nil || head == w.qualityRef {
		return
	}
	w.qualityRef = head
	// A working worker keeps its crew mates waiting while the suites run.
	w.Record.State = StateWorking
	w.save()
	w.qualitySweep(ctx, head)
	w.Record.State = StateIdle
	w.save()
}

func (w *Worker) qualitySweep(ctx context.Context, head string) {
	rec, ok := quality.ReadRecord(w.Repo, head)
	if ok {
		w.logf("quality sweep: a record for %s already exists; not running the suites", head[:12])
	} else {
		rec = w.runQuality(ctx, head)
	}
	w.seedQuality(rec, head)
}

// runQuality runs the plan and, when a suite really ran, writes the record.
func (w *Worker) runQuality(ctx context.Context, head string) quality.Record {
	var suites []testdetect.Suite
	if w.Suites != nil {
		suites = w.Suites(ctx)
	} else {
		suites = repomap.Scan(ctx, w.Repo).TestSuites
	}
	override := w.Settings.TestsCommand()
	plan := testrun.BuildPlan(suites, override, "full", nil)
	if len(override) == 0 {
		plan = withCoverage(plan, suites, w.Repo)
	}
	if len(plan.Argvs) == 0 {
		w.logf("quality sweep: no test suite detected at %s", head[:12])
		return quality.Build(head, w.clock(), nil, suites, w.Repo)
	}
	w.logf("quality sweep: running %v at %s", plan.Names, head[:12])
	results := w.testResults(ctx, plan)
	rec := quality.Build(head, w.clock(), results, suites, w.Repo)
	for _, r := range results {
		w.logf("quality sweep: %s %s (exit %d, %s)", r.Suite, r.Status, r.ExitCode, r.Duration.Round(time.Millisecond))
	}
	if !rec.Ran() {
		w.logf("quality sweep: no suite ran, so nothing is recorded for %s", head[:12])
		return rec
	}
	if err := quality.WriteRecord(w.Repo, rec); err != nil {
		w.logf("quality sweep: record: %v", err)
	}
	return rec
}

// testResults runs the plan under the user's permission rules, the OS sandbox
// and the scrubbed environment, exactly as the post-end test pass does.
func (w *Worker) testResults(ctx context.Context, plan testrun.Plan) []testrun.Result {
	return w.testResultsAt(ctx, w.Repo, plan)
}

// testResultsAt is testResults in another trusted directory, such as an item's
// worktree, which is also the one writable root of the sandbox policy.
func (w *Worker) testResultsAt(ctx context.Context, dir string, plan testrun.Plan) []testrun.Result {
	if w.RunTests != nil {
		return w.RunTests(ctx, plan)
	}
	opts := testrun.Options{
		Dir:     dir,
		Timeout: time.Duration(w.Settings.TestsTimeoutSeconds()) * time.Second,
		Perms:   permissions.From(w.Settings.Permissions.Allow, w.Settings.Permissions.Ask, w.Settings.Permissions.Deny),
	}
	pol := sandbox.FromSettings(w.Settings.Sandbox, []string{dir}, w.Posture)
	return testrun.RunPlan(sandbox.WithPolicy(ctx, pol), opts, plan)
}

// withCoverage has a Go suite report coverage: a plain `go test ./...` becomes
// `go test -cover ./...`, and a Go module whose suites are all something else
// (a just recipe, say) gets a coverage run of its own. Both argvs are fixed by
// the harness.
func withCoverage(plan testrun.Plan, suites []testdetect.Suite, root string) testrun.Plan {
	goSuite := false
	for i, argv := range plan.Argvs {
		if slices.Equal(argv, []string{"go", "test", "./..."}) {
			plan.Argvs[i] = []string{"go", "test", "-cover", "./..."}
		}
		if len(argv) >= 2 && argv[0] == "go" && argv[1] == "test" {
			goSuite = true
		}
	}
	if !goSuite && len(plan.Argvs) > 0 {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			plan.Names = append(plan.Names, "go-cover")
			plan.Argvs = append(plan.Argvs, []string{"go", "test", "-cover", "./..."})
		}
	}
	return plan
}

// seedQuality files a card for each seed the record calls for, once per
// subject, then closes the failure, coverage and untested cards whose subject
// is gone.
func (w *Worker) seedQuality(rec quality.Record, head string) {
	prov := kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID())
	labels := append(slices.Clone(w.Profile.Kanban.Labels), agentprofile.QualityLabel)
	created := 0
	for _, s := range quality.Seeds(rec) {
		_, ch, err := w.Store.UpsertFinding(kanban.FindingInput{
			Finding: s.Finding, Title: s.Title, Body: s.Body, Priority: s.Priority,
			Labels: labels, Ref: head, Once: true,
		}, prov)
		if err != nil {
			w.logf("quality sweep: card %s: %v", s.Finding, err)
			continue
		}
		if ch == kanban.FindingCreated {
			created++
		}
	}
	var closed int
	if rec.Ran() {
		gone, err := w.Store.CloseAbsent(prov, quality.Prefix, quality.Present(rec), head, quality.Covered(rec))
		if err != nil {
			w.logf("quality sweep: close: %v", err)
		}
		closed = len(gone)
	}
	w.logf("quality sweep: %d new seed cards, %d closed as no longer reported, at %s", created, closed, head[:12])
}
