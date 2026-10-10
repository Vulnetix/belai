package fleet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/kanban"
)

// coordPoll is how often a working worker looks for the forge coordinator's
// answer while it holds an item.
const coordPoll = 2 * time.Second

// coordinated reports whether this worker may hand a push to the forge
// coordinator: it mirrors to the website, keeps a registry (the spool lives
// beside it), and belai rc is there to file what it spools.
func (w *Worker) coordinated() bool {
	return w.Sync != nil && w.Registry != nil && w.Coordinator != nil && w.Coordinator()
}

// coordState is the coordinator's answer for the item in hand.
type coordState struct {
	item string
	spec CoordSpec
	// undelivered is a fact that arrived while no turn was running, given to
	// the next turn's loop when it starts.
	undelivered *agent.CoordFact
	// sink queues a fact on the running turn's goal loop; nil between turns.
	sink func(agent.CoordFact) bool
}

// beginCoord sets the workspace up to hand its branch to the coordinator and
// clears what an earlier item heard.
func (w *Worker) beginCoord(it kanban.Item, ws *Workspace) {
	w.coordMu.Lock()
	w.coord = coordState{item: it.ID}
	w.coordMu.Unlock()
	if ws == nil || !ws.Worktree || !w.coordinated() {
		return
	}
	ws.SetCoordinator(&Coordinator{
		Registry: w.Registry.Dir(), Worker: w.Record.ID, Item: it.ID, Title: it.Title,
		Note: func(note string) {
			_, _ = w.Store.Update(it.ID, kanban.Patch{Note: note}, w.Record.Session)
		},
	})
}

// watchCoord reads the coordination file every coordPoll until ctx ends.
func (w *Worker) watchCoord(ctx context.Context, it kanban.Item, ws *Workspace) {
	if w.Registry == nil {
		return
	}
	t := time.NewTicker(coordPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.takeCoord(it, ws)
		}
	}
}

// takeCoord reads and deletes the worker's coordination file, and accepts it
// only for the item in hand and a request this item's workspace filed. An
// accepted answer is remembered for the release and queued on the running
// turn's goal loop as a harness fact. It reports whether one was accepted.
func (w *Worker) takeCoord(it kanban.Item, ws *Workspace) bool {
	if w.Registry == nil {
		return false
	}
	spec, ok, err := w.Registry.TakeCoord(w.Record.ID)
	if err != nil {
		w.logf("%s: coordinator answer dropped: %v", it.Short(), err)
		return false
	}
	if !ok {
		return false
	}
	if spec.Item != it.ID || !ws.Filed(spec.Request) {
		w.logf("%s: coordinator answer for request %s dropped: not a request this item filed", it.Short(), spec.Request)
		return false
	}
	fact, err := spec.Fact()
	if err != nil {
		return false
	}
	w.logf("%s: coordinator %s (request %s)", it.Short(), spec.Event, spec.Request)
	w.coordMu.Lock()
	defer w.coordMu.Unlock()
	if w.coord.item != it.ID {
		return false
	}
	w.coord.spec = spec
	if w.coord.sink == nil || !w.coord.sink(fact) {
		w.coord.undelivered = &fact
	}
	return true
}

// dropCoord deletes a coordination file left while no item is in hand: an
// answer is only ever for the item a worker holds.
func (w *Worker) dropCoord() {
	if w.Registry == nil {
		return
	}
	if spec, ok, err := w.Registry.TakeCoord(w.Record.ID); ok || err != nil {
		w.logf("coordinator answer for request %s dropped: no item in hand", spec.Request)
	}
}

// attachCoord routes coordinator facts into a running turn's goal loop, first
// any that arrived between turns. The returned function detaches it.
func (w *Worker) attachCoord(sink func(agent.CoordFact) bool) func() {
	w.coordMu.Lock()
	w.coord.sink = sink
	if f := w.coord.undelivered; f != nil && sink(*f) {
		w.coord.undelivered = nil
	}
	w.coordMu.Unlock()
	return func() {
		w.coordMu.Lock()
		w.coord.sink = nil
		w.coordMu.Unlock()
	}
}

// coordFor is the coordinator's last answer for item id.
func (w *Worker) coordFor(id string) (CoordSpec, bool) {
	w.coordMu.Lock()
	defer w.coordMu.Unlock()
	if w.coord.item != id || w.coord.spec.Event == "" {
		return CoordSpec{}, false
	}
	return w.coord.spec, true
}

// coordTookOver reports whether the coordinator opened item id's pull request.
func (w *Worker) coordTookOver(id string) bool {
	s, ok := w.coordFor(id)
	return ok && s.Event == string(agent.CoordTookOver)
}

// Coordinator reasons by how the release treats them (contract section 3).
var (
	// coordWorkFaults are the branch's: the attempt failed.
	coordWorkFaults = map[string]bool{"non_fast_forward": true, "branch_mismatch": true, "empty_bundle": true}
	// coordBlockers need a person (install the app, attach the repository): the
	// item is blocked and no attempt is counted.
	coordBlockers = map[string]bool{"app_not_installed": true, "token_denied": true, "expired": true, "repo_not_attached": true, "bundle_missing": true}
)

// applyCoord settles the release by the coordinator's last answer for the item:
// a pull request it opened completes the item; a wait the wall budget ended is
// not an attempt; a failure that is the branch's fails the attempt, and one a
// person must fix blocks the item without one, unless the branch was published
// after all. Notes are harness facts: event, number, reason word.
func (w *Worker) applyCoord(ctx context.Context, o outcome, it kanban.Item, ws *Workspace, cause error) outcome {
	spec, ok := w.coordFor(it.ID)
	if !ok {
		return o
	}
	name := w.Profile.Name
	switch agent.CoordEvent(spec.Event) {
	case agent.CoordTookOver:
		if o.failed {
			o.failed, o.blocked, o.transient = false, false, false
			o.note = fmt.Sprintf("agent %s handed publishing to the coordinator, which opened draft pull request #%d (%d passes)", name, spec.PR, o.passes)
		} else {
			o.note += fmt.Sprintf("; the coordinator opened draft pull request #%d", spec.PR)
		}
		if url, ok := agent.CleanPRURL(spec.PRURL, spec.PR); ok {
			if _, err := w.Store.SetPR(it.ID, url, w.Record.Session); err != nil {
				w.logf("%s: record PR: %v", it.Short(), err)
			}
		}
	case agent.CoordWaiting:
		if o.failed && errors.Is(cause, errWallBudget) {
			return outcome{failed: true, transient: true, stop: o.stop, passes: o.passes, branch: o.branch,
				note: fmt.Sprintf("agent %s was waiting on the coordinator for the GitHub rate limit when %s; not counted as an attempt", name, errWallBudget)}
		}
	case agent.CoordFailed:
		if ws.PublishedCurrent(context.WithoutCancel(ctx)) {
			return o
		}
		switch {
		case coordWorkFaults[spec.Reason]:
			return outcome{failed: true, stop: o.stop, passes: o.passes, branch: o.branch,
				note: fmt.Sprintf("agent %s: the coordinator could not publish the branch: %s", name, spec.Reason)}
		case coordBlockers[spec.Reason]:
			return outcome{failed: true, blocked: true, transient: true, stop: o.stop, passes: o.passes, branch: o.branch,
				note: fmt.Sprintf("the coordinator could not publish the branch: %s; a person must fix that, so this is not counted as an attempt", spec.Reason)}
		}
	}
	return o
}
