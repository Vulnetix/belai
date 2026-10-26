package fleet

import (
	"context"
	"strings"

	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/turnlog"
)

// The measures a worker's session records beyond its turns: the checks the
// harness ran on the item's branch and the commits it made (docs/session-
// measures.md). They go into the transcript of the session the item is on,
// the one the last turn of the claim wrote. A sweep that runs outside an item
// (the quality and lint sweeps) is not a session's work and records nothing.

// itemLog is the transcript log of the session the item in hand is on, or nil
// when there is none: no turn has run for this claim, or the log is another
// claim's, left from an earlier item.
func (w *Worker) itemLog() *turnlog.Log {
	l := w.sessLog.Load()
	if l == nil || l.Writer() == nil || l.Writer().ID() != w.Record.Session {
		return nil
	}
	return l
}

// gatePrefix starts the plan name of a run that decides a card's gate
// (verify.go), so its check_run is a gate and not a plain test.
const gatePrefix = "gate:"

// recordChecks writes a check_run for each run of plan that executed. kind is
// what the plan's runs are; a run named for a gate is a gate.
func (w *Worker) recordChecks(kind sessionmeasure.Kind, plan testrun.Plan, results []testrun.Result) {
	l := w.itemLog()
	if l == nil {
		return
	}
	for i, r := range results {
		k := kind
		if i < len(plan.Names) && strings.HasPrefix(plan.Names[i], gatePrefix) {
			k = sessionmeasure.KindGate
		}
		if c, ok := sessionmeasure.FromTestrun(r, k); ok {
			l.Check(c)
		}
	}
}

// itemResultsAt runs plan in dir like testResultsAt and records what ran as
// check_run entries of kind in the item's session.
func (w *Worker) itemResultsAt(ctx context.Context, dir string, plan testrun.Plan, kind sessionmeasure.Kind) []testrun.Result {
	results := w.testResultsAt(ctx, dir, plan)
	w.recordChecks(kind, plan, results)
	return results
}

// recordCommit writes a commit entry for a commit the harness made on the
// item's branch.
func (w *Worker) recordCommit(c sessionmeasure.Commit) {
	if l := w.itemLog(); l != nil {
		l.Commit(c)
	}
}

// noteCommit reads the commit HEAD names and hands it to OnCommit. A commit
// git cannot read back is not recorded; the work is committed either way.
func (w *Workspace) noteCommit(ctx context.Context) {
	if w.OnCommit == nil {
		return
	}
	if c, err := sessionmeasure.ReadHead(ctx, w.run, w.Dir); err == nil {
		w.OnCommit(c)
	}
}
