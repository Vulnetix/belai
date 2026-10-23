package fleet

import (
	"context"
	"slices"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// The lint sweep of a worker (kanban.lint). It mirrors the quality sweep: the
// harness runs the linters whose rule files the repository checked in
// (internal/lintdetect), through the same runner as the test suites (a fixed
// argv, no shell, the user's permission rules, the OS sandbox and the scrubbed
// environment), ties what they show to the commit they ran on, and files a
// card for each linter that reports findings. No model runs a linter, and no
// linter output reaches a card or a record: only the tool, its argv and a
// count.
//
// A lint record for HEAD means the linters already ran on this commit, so they
// do not run again. A card is filed once for each finding (the tool and its
// count), so a crew launched again, on the same commit or a later one, never
// doubles the board, and a card is closed once its linter runs clean.

// lintStep runs the lint sweep once for each HEAD this worker sees.
func (w *Worker) lintStep(ctx context.Context) {
	if w.Profile.Kanban == nil || w.Profile.Kanban.Lint == nil || !w.Profile.Kanban.Lint.Sweep || w.Item != "" {
		return
	}
	head, err := w.headRef(ctx)
	if err != nil || head == w.lintRef {
		return
	}
	w.lintRef = head
	w.Record.State = StateWorking
	w.save()
	w.lintSweep(ctx, head)
	w.Record.State = StateIdle
	w.save()
}

func (w *Worker) lintSweep(ctx context.Context, head string) {
	rec, ok := quality.ReadLintRecord(w.Repo, head)
	if ok {
		w.logf("lint sweep: a record for %s already exists; not running the linters", head[:12])
	} else {
		rec = w.runLint(ctx, head)
	}
	w.seedLint(rec, head)
}

// lintersAt returns the linters detected at dir: the harness's own rule-file
// table, never a model's.
func (w *Worker) lintersAt(ctx context.Context, dir string) []lintdetect.Linter {
	if w.Lints != nil {
		return w.Lints(ctx)
	}
	return lintdetect.Detect(dir)
}

// lintPlan is the plan that runs linters: each linter's fixed argv, named for
// the linter.
func lintPlan(linters []lintdetect.Linter) testrun.Plan {
	var p testrun.Plan
	for _, l := range linters {
		p.Names = append(p.Names, l.Name)
		p.Argvs = append(p.Argvs, slices.Clone(l.Command))
	}
	return p
}

// runLint runs the plan and, when a linter really ran, writes the record.
func (w *Worker) runLint(ctx context.Context, head string) quality.LintRecord {
	linters := w.lintersAt(ctx, w.Repo)
	plan := lintPlan(linters)
	if len(plan.Argvs) == 0 {
		w.logf("lint sweep: no linter rule file detected at %s", head[:12])
		return quality.BuildLint(head, w.clock(), nil, nil)
	}
	w.logf("lint sweep: running %v at %s", plan.Names, head[:12])
	results := w.testResults(ctx, plan)
	rec := quality.BuildLint(head, w.clock(), linters, results)
	for _, r := range results {
		w.logf("lint sweep: %s %s (exit %d, %s)", r.Suite, r.Status, r.ExitCode, r.Duration.Round(time.Millisecond))
	}
	if !rec.Ran() {
		w.logf("lint sweep: no linter ran, so nothing is recorded for %s", head[:12])
		return rec
	}
	if err := quality.WriteLintRecord(w.Repo, rec); err != nil {
		w.logf("lint sweep: record: %v", err)
	}
	return rec
}

// seedLint files a card for each linter the record shows drifting, once for
// each finding, then closes the lint cards whose linter now runs clean.
func (w *Worker) seedLint(rec quality.LintRecord, head string) {
	prov := kanban.ProvenanceFor(w.Repo, w.Record.ID, headless.HostID())
	labels := slices.Clone(w.Profile.Kanban.Labels)
	for _, l := range []string{agentprofile.LintLabel, agentprofile.LintDriftLabel} {
		if !slices.Contains(labels, l) {
			labels = append(labels, l)
		}
	}
	created := 0
	for _, s := range quality.LintSeeds(rec) {
		_, ch, err := w.Store.UpsertFinding(kanban.FindingInput{
			Finding: s.Finding, Title: s.Title, Body: s.Body, Priority: s.Priority,
			Labels: labels, Ref: head, Once: true,
		}, prov)
		if err != nil {
			w.logf("lint sweep: card %s: %v", s.Finding, err)
			continue
		}
		if ch == kanban.FindingCreated {
			created++
		}
	}
	var closed int
	if rec.Ran() {
		gone, err := w.Store.CloseAbsent(prov, quality.PrefixLint, quality.LintPresent(rec), head, quality.LintCovered(rec))
		if err != nil {
			w.logf("lint sweep: close: %v", err)
		}
		closed = len(gone)
	}
	w.logf("lint sweep: %d new seed cards, %d closed as no longer reported, at %s", created, closed, head[:12])
}

// asGateSuite presents a linter as a suite a runnable gate may name. Its
// ecosystem is "lint", so a Go linter is never narrowed to a Go test run, and
// its argv is the linter's own.
func asGateSuite(l lintdetect.Linter) testdetect.Suite {
	return testdetect.Suite{Name: l.Name, Ecosystem: "lint", Command: slices.Clone(l.Command), Scope: testdetect.ScopeFull, Framework: l.Tool}
}
