package fleet

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/repomap"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// Harness verification of a card's acceptance gates (kanban.gates.verify).
//
// After a worker's turn is committed, the harness runs each runnable gate's
// suite on the branch, from an argv built out of the detected-suite table, and
// decides the gate by the exit code (quality.JudgeGate). No model runs a
// suite, judges whether one ran, or writes a state onto a gate. The states and
// the run's facts (gate ids, suite names, test names, exit codes) reach the
// card and the next attempt as harness-composed identifiers, never as test
// output text.

// gateVerdict is what verifying one item on its branch found.
type gateVerdict struct {
	// ran is true when at least one suite ran.
	ran bool
	// blocked is true when a gate could not run at all (a deny or ask rule, no
	// binary): that says nothing about the work, so a person must look.
	blocked bool
	// ok is true when every runnable gate is met and nothing regressed.
	ok bool
	// note is the one-line, harness-composed account.
	note string
}

// suitesAt returns the suites detected at dir: the harness's own marker-file
// table, read in the item's worktree so a suite the work added is seen.
func (w *Worker) suitesAt(ctx context.Context, dir string) []testdetect.Suite {
	if w.Suites != nil {
		return w.Suites(ctx)
	}
	return repomap.Scan(ctx, dir).TestSuites
}

// short12 returns the first twelve characters of a commit id.
func short12(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// gatesMode is the profile's verification mode.
func (w *Worker) gatesMode() string {
	if w.Profile.Kanban == nil {
		return "off"
	}
	return w.Profile.Kanban.Gates.VerifyMode()
}

// verifyBranch runs the item's gates on the branch the worktree stands at and
// records each gate's state on the card. It is called with the claim still
// held, after the work is committed. A card with no runnable gate and no base
// record to compare against has nothing to verify and passes.
func (w *Worker) verifyBranch(ctx context.Context, it kanban.Item, ws *Workspace) gateVerdict {
	head, err := ws.Head(ctx)
	if err != nil {
		return gateVerdict{blocked: true, note: "the branch head could not be read, so its gates were not run"}
	}
	c12 := head[:12]
	suites := w.suitesAt(ctx, ws.Dir)
	byName := map[string]testdetect.Suite{}
	for _, s := range suites {
		byName[s.Name] = s
	}

	// One run per distinct argv: several gates, or a gate and a suite, that
	// resolve to the same command share its result.
	var plan testrun.Plan
	index := map[string]int{}
	want := func(name string, argv []string) int {
		key := testrun.Subject(argv)
		if i, ok := index[key]; ok {
			return i
		}
		plan.Names = append(plan.Names, name)
		plan.Argvs = append(plan.Argvs, argv)
		index[key] = len(plan.Argvs) - 1
		return index[key]
	}

	type gateRun struct {
		gate kanban.Gate
		at   int // index into plan, or -1 when the suite is not detected
	}
	var runs []gateRun
	for _, g := range it.Gates {
		if g.Kind != kanban.GateRunnable {
			continue
		}
		s, ok := byName[g.Suite]
		if !ok {
			runs = append(runs, gateRun{g, -1})
			continue
		}
		runs = append(runs, gateRun{g, want("gate:"+g.ID, quality.GateArgv(s, g))})
	}

	// The regression check needs the record of the base commit, written by the
	// quality sweep; without one there is nothing to compare against.
	var base quality.Record
	compared := false
	if ws.Base != "" {
		base, compared = quality.ReadRecord(w.Repo, ws.Base)
	}
	var suiteAt []int
	var suitePlan testrun.Plan
	if compared {
		suitePlan = testrun.BuildPlan(suites, w.Settings.TestsCommand(), "full", nil)
		for i, argv := range suitePlan.Argvs {
			suiteAt = append(suiteAt, want(suitePlan.Names[i], argv))
		}
	}
	if len(plan.Argvs) == 0 && len(runs) == 0 {
		return gateVerdict{ok: true}
	}

	var results []testrun.Result
	if len(plan.Argvs) > 0 {
		results = w.testResultsAt(ctx, ws.Dir, plan)
	}
	resultAt := func(i int) testrun.Result {
		if i >= 0 && i < len(results) {
			return results[i]
		}
		return testrun.Result{Status: testrun.Errored, Note: "the run did not finish"}
	}

	v := quality.NewVerification(it.Short(), head, ws.Base, w.clock())
	v.Compared = compared
	var failed []string
	allMet, blocked, ran := true, false, false
	for _, r := range runs {
		var (
			state kanban.GateState
			note  string
			did   bool
		)
		if r.at < 0 {
			state, note, did = kanban.GateUnmet, "suite "+r.gate.Suite+" is not detected in the workspace", true
		} else {
			res := resultAt(r.at)
			res.Suite = r.gate.Suite
			state, note, did = quality.JudgeGate(r.gate, res)
			if did && r.at >= 0 {
				ran = true
			}
		}
		if _, err := w.Store.SetGate(it.ID, w.Record.ID, r.gate.ID, state, head, note); err != nil {
			w.logf("%s: gate %s: %v", it.Short(), r.gate.ID, err)
		} else {
			// Facts only: the gate's id, suite and state, the exit code the
			// harness read, never the run's output or the gate's note.
			data := map[string]string{"gate": r.gate.ID, "suite": r.gate.Suite, "status": string(state)}
			if r.at >= 0 {
				data["exit"] = strconv.Itoa(resultAt(r.at).ExitCode)
			}
			w.auditItem(audit.GateVerified, it, audit.Fact{SessionID: w.Record.Session, Commit: head, BaseCommit: ws.Base,
				Branch: ws.Branch, Outcome: string(state), Data: data})
		}
		v.Gates = append(v.Gates, quality.GateResult{ID: r.gate.ID, Kind: string(r.gate.Kind), Suite: r.gate.Suite, State: string(state), Note: note})
		if state != kanban.GateMet {
			allMet = false
			failed = append(failed, fmt.Sprintf("%s unmet (%s)", r.gate.ID, note))
			if !did {
				blocked = true
			}
		}
	}

	var regressed []string
	if compared {
		var cmp []testrun.Result
		for i, at := range suiteAt {
			res := resultAt(at)
			res.Suite = suitePlan.Names[i]
			cmp = append(cmp, res)
			if res.Status == testrun.Passed || res.Status == testrun.Failed || res.Status == testrun.TimedOut {
				ran = true
			}
			v.Suites = append(v.Suites, quality.SuiteResults([]testrun.Result{res})...)
		}
		regressed = quality.Regressions(base, cmp)
	}
	v.Regressed = regressed
	if len(regressed) > 0 {
		w.auditItem(audit.GateVerified, it, audit.Fact{SessionID: w.Record.Session, Commit: head, BaseCommit: ws.Base, Branch: ws.Branch,
			Outcome: "regressed", Data: map[string]string{"gate": "regression", "regressed": strconv.Itoa(len(regressed))}})
	}
	v.AllRunnable = allMet
	if ran {
		if err := quality.WriteVerification(w.Repo, v); err != nil {
			w.logf("%s: verification record: %v", it.Short(), err)
		}
	}

	out := gateVerdict{ran: ran, blocked: blocked, ok: allMet && len(regressed) == 0}
	if len(regressed) > 0 {
		failed = append(failed, "regressed: "+strings.Join(regressed, ", "))
	}
	switch {
	case !out.ok:
		out.note = fmt.Sprintf("verification failed at %s: %s", c12, strings.Join(failed, "; "))
	case len(runs) == 0:
		out.note = fmt.Sprintf("verified at %s: no regression against %s", c12, short12(ws.Base))
	default:
		out.note = fmt.Sprintf("verified at %s: %d gate(s) met", c12, len(runs))
		if compared {
			out.note += "; no regression against " + short12(ws.Base)
		}
	}
	return out
}

// applyVerification folds a verification into the turn's outcome. In record
// mode the note is added and nothing else changes. In enforce mode a card
// whose gates are not all met is a failed attempt: the harness's result, not
// the model's word, decides it. A gate that could not run at all blocks the
// card for a person instead, like a permission the worker cannot ask for.
func (w *Worker) applyVerification(o outcome, v gateVerdict, mode string) outcome {
	if v.note == "" {
		return o
	}
	if mode != "enforce" || v.ok {
		o.note += "; " + v.note
		return o
	}
	if v.blocked {
		o.failed, o.blocked = true, true
		o.note = "the harness could not run the card's gates (" + v.note + "); a person must review it"
		return o
	}
	o.failed = true
	o.note = fmt.Sprintf("agent %s completed it, but %s", w.Profile.Name, v.note)
	return o
}

// resetManualGates returns a reviewer's manual gates to unmet before it works
// the card, so each review decides them afresh: a gate a reviewer met for an
// earlier version of the branch says nothing about this one. An abandoned gate
// stays abandoned, because that is a person's decision to make.
func (w *Worker) resetManualGates(it kanban.Item) {
	k := w.Profile.Kanban
	if k == nil || k.Gates == nil || !k.Gates.Review || w.gatesMode() != agentprofile.VerifyEnforce {
		return
	}
	for _, g := range it.Gates {
		if g.Kind != kanban.GateManual || g.State == kanban.GateUnmet || g.State == kanban.GateAbandoned {
			continue
		}
		if _, err := w.Store.SetGate(it.ID, w.Record.ID, g.ID, kanban.GateUnmet, "", "to be decided again by this review"); err != nil {
			w.logf("%s: gate %s: %v", it.Short(), g.ID, err)
		}
	}
}

// applyManualGates holds a reviewer to the card's manual gates. In enforce
// mode with review on, a turn the model completed is a failed attempt while a
// manual gate is unmet, and the card is blocked for a person while one is
// abandoned: abandonment is terminal and never success. The note names gate
// ids only; a gate's own evidence stays on the gate.
func (w *Worker) applyManualGates(o outcome, it kanban.Item) outcome {
	k := w.Profile.Kanban
	if o.failed || k == nil || k.Gates == nil || !k.Gates.Review || w.gatesMode() != agentprofile.VerifyEnforce {
		return o
	}
	cur, err := w.Store.Get(it.ID)
	if err != nil {
		return o
	}
	var unmet, abandoned []string
	for _, g := range cur.Gates {
		if g.Kind != kanban.GateManual {
			continue
		}
		switch g.State {
		case kanban.GateAbandoned:
			abandoned = append(abandoned, g.ID)
		case kanban.GateMet:
		default:
			unmet = append(unmet, g.ID)
		}
	}
	switch {
	case len(abandoned) > 0:
		o.failed, o.blocked = true, true
		o.note = "HANDOFF REQUIRED: manual gate " + strings.Join(abandoned, ", ") + " was abandoned as impossible; a person must decide, and the card is not done"
	case len(unmet) > 0:
		o.failed = true
		o.note = fmt.Sprintf("agent %s completed it, but manual gate %s is not met: the reviewer records each manual gate with KanbanGate", w.Profile.Name, strings.Join(unmet, ", "))
	}
	return o
}
