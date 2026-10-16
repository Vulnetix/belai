package fleet

import "fmt"

// Provider fallback for a crew run's workers. The run names an ordered chain
// of fallback targets; after FallbackAfter items in a row come back unworked
// because the provider failed, a worker moves to the next target that this
// host can run, and claims again at once instead of backing off. Every Probe
// it goes back to the primary for one item: if the provider recovered it
// stays, and if not the first outage moves it straight back to the fallback.
// A model a human picked in the control room is pinned: the policy leaves it
// alone until the control hands the model back.

// refreshRun re-reads the worker's crew run. It returns a reason to stop when
// the run ended, or the worker's repository left the run's scope or was
// paused there; and it moves the worker to the run's primary target when the
// control room changed it.
func (w *Worker) refreshRun() string {
	if w.Record.Run == "" || w.Registry == nil {
		return ""
	}
	run, err := w.Registry.GetRun(w.Record.Run)
	if err != nil {
		// A run file that cannot be read is no reason to drop the card work;
		// the supervisor sweeps workers of a run that is gone.
		return ""
	}
	w.crewRun = &run
	if !run.State.Live() || run.State == RunStopping {
		return "the crew run stopped"
	}
	d, ok := run.Dir(w.Repo)
	switch {
	case !ok:
		return "this repository left the crew run's scope"
	case d.Paused:
		return "this repository is paused in the crew run"
	}
	if !run.Primary.Zero() && run.Primary != w.primary {
		w.primary = run.Primary
		if w.Record.Fallback == "" && !w.pinned {
			if err := w.setTarget(run.Primary, run.Effort); err != nil {
				w.logf("crew run: cannot run %s: %v", run.Primary, err)
			} else {
				w.logf("crew run: primary is now %s", run.Primary)
			}
		}
	}
	return ""
}

// fallbackStep moves the worker along its run's fallback chain when the
// provider failed FallbackAfter items in a row. It reports whether it moved.
func (w *Worker) fallbackStep() bool {
	run := w.crewRun
	if run == nil || w.pinned || w.Controls == nil || len(run.Fallbacks) == 0 || w.outages < run.FallbackAfterOr() {
		return false
	}
	for w.fbIdx < len(run.Fallbacks) {
		next := run.Fallbacks[w.fbIdx]
		w.fbIdx++
		if err := w.setTarget(next, ""); err != nil {
			w.logf("fallback: skipped %s: %v", next, err)
			continue
		}
		from := w.Record.Fallback
		if from == "" {
			from = w.primary.String()
		}
		w.Record.Fallback, w.fbSince = next.String(), w.clock()
		w.logf("fallback: %d outages in a row on %s; moved to %s", w.outages, from, next)
		w.auditFallback(next, w.outages)
		w.outages, w.Record.Outages, w.Record.OutageUntil = 0, 0, 0
		w.save()
		return true
	}
	return false
}

// probeStep sends a worker on a fallback back to its primary once the run's
// probe interval passed. One more outage there moves it back at once.
func (w *Worker) probeStep() {
	run := w.crewRun
	if run == nil || w.Record.Fallback == "" || w.pinned || w.primary.Zero() {
		return
	}
	if w.clock().Sub(w.fbSince) < run.Probe() {
		return
	}
	if err := w.setTarget(w.primary, ""); err != nil {
		w.logf("fallback: cannot probe %s: %v", w.primary, err)
		w.fbSince = w.clock()
		return
	}
	w.logf("fallback: probing the primary %s after %s on %s", w.primary, run.Probe(), w.Record.Fallback)
	w.auditFallback(w.primary, 0)
	w.Record.Fallback, w.fbIdx = "", 0
	// One outage on the probe is enough to go back.
	w.outages = max(run.FallbackAfterOr()-1, 0)
	w.save()
}

func (w *Worker) fallbackFacts() string {
	if w.Record.Fallback == "" {
		return ""
	}
	return fmt.Sprintf(" (fallback %s, primary %s)", w.Record.Fallback, w.primary)
}
