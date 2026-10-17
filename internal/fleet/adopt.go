package fleet

import (
	"errors"
	"strconv"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/crewlog"
)

// A crew run outlives its supervisor. When the supervisor dies (a crash, an
// OOM kill, kill -9) while the run is still meant to run, the run is
// orphaned: its workers carry on with the cards they hold, and the first
// thing that notices starts a new supervisor. The run's workers watch for it
// (Worker.AdoptSupervisor), and so do belai rc, a relaunch of the crew and
// the control room. The decision is taken under the runs lock, so one adopter
// wins, and a supervisor claims its run when it starts (Supervisor.Run), so
// two never run it at once.

// ReasonOrphaned is an orphaned run's reason.
const ReasonOrphaned = "the crew supervisor exited; starting a new one"

// ErrSupervised refuses a second supervisor for a run that has a live one.
var ErrSupervised = errors.New("fleet: this crew run already has a live supervisor")

// Adoption is what AdoptRun did.
type Adoption string

// Adoption outcomes.
const (
	// AdoptNone: the run is not live, or its supervisor is alive.
	AdoptNone Adoption = ""
	// AdoptRestarted: a new supervisor runs the run.
	AdoptRestarted Adoption = "supervisor_restarted"
	// AdoptStopped: the run was stopping when its supervisor died; its
	// workers were stopped and the run ended.
	AdoptStopped Adoption = "stopped"
	// AdoptFailed: the supervisor kept dying, so the run failed and its
	// workers were stopped.
	AdoptFailed Adoption = "supervisor_failed"
)

// AdoptRun starts a new supervisor for a live run whose supervisor is gone.
// A run that was stopping is finished instead, and a run whose supervisors
// keep dying (MaxSupervisorRestarts within SupervisorRestartWindow) fails.
// by says who noticed ("worker", "rc", "launch", "human").
func (r *Registry) AdoptRun(id, by string, start SupervisorStarter) (Adoption, error) {
	if start == nil {
		return AdoptNone, nil
	}
	cur, err := r.GetRun(id)
	if err != nil || !r.orphaned(cur) {
		return AdoptNone, err
	}
	var did Adoption
	var startErr error
	run, err := r.UpdateRun(id, by, func(cr *CrewRun) error {
		did = AdoptNone
		if !r.orphaned(*cr) {
			return nil
		}
		now := r.now()
		switch {
		case cr.State == RunStopping:
			did = AdoptStopped
		case cr.RestartWindow > 0 && now.Sub(time.UnixMilli(cr.RestartWindow)) < SupervisorRestartWindow &&
			cr.Restarts >= MaxSupervisorRestarts:
			did = AdoptFailed
			cr.State, cr.Reason, cr.Stopped = RunFailed, "the crew supervisor kept exiting", now.UnixMilli()
			return nil
		default:
			if cr.RestartWindow == 0 || now.Sub(time.UnixMilli(cr.RestartWindow)) >= SupervisorRestartWindow {
				cr.RestartWindow, cr.Restarts = now.UnixMilli(), 0
			}
			cr.Restarts++
			pid, err := start(*cr)
			if err != nil {
				// Counted: a supervisor that cannot start uses the budget too.
				startErr = err
				cr.State, cr.Reason = RunOrphaned, "a new crew supervisor did not start"
				return nil
			}
			did = AdoptRestarted
			cr.Supervisor, cr.State, cr.Reason, cr.Beat = pid, RunRunning, "", now.UnixMilli()
		}
		return nil
	})
	if err != nil {
		return AdoptNone, err
	}
	switch did {
	case AdoptRestarted:
		recordSupervisor(run, AdoptRestarted)
	case AdoptStopped:
		return did, r.StopRun(id, by, 20*time.Second)
	case AdoptFailed:
		recordSupervisor(run, AdoptFailed)
		if l := openCrewLog(run.ID); l != nil {
			_ = l.AppendFault(crewlog.Fault{Run: run.ID, Crew: run.Crew, Class: FaultSupervisor, Outcome: "terminated", Detail: run.Reason})
			l.Close()
		}
		r.stopRunWorkers(run.ID, 20*time.Second)
	}
	return did, startErr
}

// AdoptOrphans adopts every orphaned run (ListRuns marks them).
func (r *Registry) AdoptOrphans(by string, start SupervisorStarter) map[string]Adoption {
	runs, _ := r.ListRuns()
	out := map[string]Adoption{}
	for _, run := range runs {
		if !r.orphaned(run) {
			continue
		}
		if did, _ := r.AdoptRun(run.ID, by, start); did != AdoptNone {
			out[run.ID] = did
		}
	}
	return out
}

// recordSupervisor records a supervisor restart or the run failing on its
// supervisor (crew.control, by the harness), with the restart count.
func recordSupervisor(run CrewRun, did Adoption) {
	f := audit.Fact{Kind: audit.CrewControl, ActorKind: audit.ActorHarness, Crew: run.Crew, Outcome: string(did),
		Data: map[string]string{"retries": strconv.Itoa(run.Restarts)}}
	audit.Emit(f)
	appendEntry(run.ID, f, "", "")
}
