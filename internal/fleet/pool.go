package fleet

import "time"

// The provider pool of a crew run. The run's pool is a ring of provider and
// model targets, not a primary with fallbacks: a worker starts on the first,
// and after SwitchAfter items in a row come back unworked because the
// provider failed, it moves to the next target this host can run and claims
// again at once, wrapping round, so it returns to a target that failed before
// (an outage ends). Once every target failed since the worker's last success,
// it stops claiming and reports the pool exhausted. The supervisor then puts
// the whole crew to sleep for FinalRetry, and wakes it for one final pass over
// the pool; if that pass fails on every target too, the run ends
// (supervisor.go, poolStep). A model a human picked is pinned: the pool
// leaves that worker alone until the control hands it back.

// refreshRun re-reads the worker's crew run. It returns a reason to stop when
// the run ended, or the worker's repository left the run's scope or was
// paused there. It also follows the run's pool: a final retry the crew
// started, a success another worker had, and a pool edited in the control
// room.
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
	if len(run.Pool) == 0 || w.pinned {
		return ""
	}
	switch {
	case run.PoolEpoch != w.Record.PoolEpoch:
		// The crew's final retry: every target gets another chance, from the
		// first.
		w.Record.PoolEpoch = run.PoolEpoch
		w.forgetPool()
		w.moveTo(run, 0, "the crew's final retry")
	case w.Record.PoolExhausted && run.LastSuccess > w.Record.PoolExhaustedAt:
		// Another worker got a card through since: the pool is not down, so
		// this worker cycles on.
		w.forgetPool()
		w.moveTo(run, (w.Record.PoolIndex+1)%len(run.Pool), "another worker got through")
	case !run.InPool(w.currentTarget()):
		// The pool was edited and no longer holds this worker's target.
		w.moveTo(run, 0, "the pool changed")
	}
	return ""
}

// poolWait is how long the worker waits before it claims: until the crew's
// sleep ends, or one poll while its own pool is exhausted and the crew has
// not answered yet.
func (w *Worker) poolWait(poll time.Duration) time.Duration {
	run := w.crewRun
	if run == nil || len(run.Pool) == 0 {
		return 0
	}
	if run.PoolPhase == PoolSleeping {
		if until := time.UnixMilli(run.SleepUntil); until.After(w.clock()) {
			return min(until.Sub(w.clock()), max(poll, time.Second))
		}
		return 0
	}
	if w.Record.PoolExhausted && !w.pinned {
		return poll
	}
	return 0
}

// poolStep runs after an item came back unworked: once the streak reaches the
// run's SwitchAfter, the worker's target counts as failed and it moves to the
// next target, or reports the pool exhausted when every one has failed. It
// reports whether it handled the streak, so the loop skips its own backoff.
func (w *Worker) poolStep() bool {
	run := w.crewRun
	if run == nil || len(run.Pool) == 0 || w.pinned || w.Controls == nil || w.outages < run.SwitchAfterOr() {
		return false
	}
	if w.poolFailed == nil {
		w.poolFailed = map[Target]bool{}
	}
	w.poolFailed[w.currentTarget()] = true
	streak := w.outages
	w.outages, w.Record.Outages, w.Record.OutageUntil = 0, 0, 0
	for step := 1; step <= len(run.Pool); step++ {
		i := (w.Record.PoolIndex + step) % len(run.Pool)
		t := run.Pool[i]
		if w.poolFailed[t] {
			continue
		}
		if w.moveTo(*run, i, "") {
			w.logf("pool: %d outages in a row; moved to %s", streak, t)
			w.auditPoolSwitch(t, streak)
			return true
		}
		// A target this host cannot run fails this cycle too.
		w.poolFailed[t] = true
	}
	w.Record.PoolExhausted, w.Record.PoolExhaustedAt = true, w.clock().UnixMilli()
	w.logf("pool: every provider and model in the pool failed; waiting on the crew")
	w.auditPoolSwitch(Target{}, streak)
	w.save()
	return true
}

// poolSuccess runs after a card the provider answered (whatever became of
// the card): the pool works, so the targets that failed are forgotten.
func (w *Worker) poolSuccess() {
	w.forgetPool()
	w.Record.LastSuccess = w.clock().UnixMilli()
}

func (w *Worker) forgetPool() {
	w.poolFailed = nil
	w.Record.PoolExhausted, w.Record.PoolExhaustedAt = false, 0
}

// moveTo puts the worker on pool target i, reporting whether this host can
// run it.
func (w *Worker) moveTo(run CrewRun, i int, why string) bool {
	if i < 0 || i >= len(run.Pool) {
		return false
	}
	t := run.Pool[i]
	if t != w.currentTarget() {
		if err := w.setTarget(t, ""); err != nil {
			w.logf("pool: cannot run %s: %v", t, err)
			return false
		}
		if why != "" {
			w.logf("pool: %s; now on %s", why, t)
		}
	}
	w.Record.PoolIndex = i
	w.save()
	return true
}
