package fleet

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/notify"
	"github.com/vulnetix/belai/internal/proc"
)

const deadPID = 1 << 30

// startSleeper starts a process that stands in for a supervisor or a worker.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("no sleep binary: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A run whose supervisor died is orphaned, not failed: it stays live, so a
// relaunch joins it, and adopting it starts a supervisor that allocates again.
func TestADeadSupervisorsRunIsOrphanedAndAdopted(t *testing.T) {
	store, reg := testEnv(t)
	repo := t.TempDir()
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: repo}}, Members: members("builder", 2)},
		func(CrewRun) (int, error) { return deadPID, nil })
	if err != nil {
		t.Fatal(err)
	}
	live, ok := reg.LiveRun("c")
	if !ok || live.State != RunOrphaned || live.Reason != ReasonOrphaned {
		t.Fatalf("a run with a dead supervisor: %+v, live %v", live, ok)
	}
	started := 0
	did, err := reg.AdoptRun(run.ID, "worker", func(CrewRun) (int, error) { started++; return os.Getpid(), nil })
	if err != nil || did != AdoptRestarted || started != 1 {
		t.Fatalf("adopt: %q, %v, started %d", did, err, started)
	}
	got, _ := reg.GetRun(run.ID)
	if got.State != RunRunning || got.Supervisor != os.Getpid() || got.Restarts != 1 || got.Reason != "" {
		t.Fatalf("adopted run %+v", got)
	}
	if did, _ := reg.AdoptRun(run.ID, "worker", func(CrewRun) (int, error) { started++; return 0, nil }); did != AdoptNone || started != 1 {
		t.Fatalf("a run with a live supervisor was adopted again: %q", did)
	}
	// The new supervisor keeps the crew allocated.
	var spawned int
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, Max: 8, PID: os.Getpid(),
		Demand: func(string, string) int { return 3 },
		Spawn:  func(SpawnOptions) (string, error) { spawned++; return NewID("builder"), nil }}
	if err := s.claim(); err != nil {
		t.Fatal(err)
	}
	s.Reconcile(got)
	if spawned != 2 {
		t.Fatalf("spawned %d workers after adoption", spawned)
	}
}

// kill -9 of the supervisor: a worker of the run notices and starts a new one
// within its watch period, while it would be busy on a card.
func TestAKilledSupervisorIsReplacedByItsWorker(t *testing.T) {
	_, reg := testEnv(t)
	sup := startSleeper(t)
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)},
		func(CrewRun) (int, error) { return sup.Process.Pid, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !reg.SupervisorAlive(run) {
		t.Fatal("a running supervisor is not alive")
	}
	_ = sup.Process.Kill()
	_ = sup.Wait()
	var adopted atomic.Int32
	w := &Worker{Registry: reg, Record: Record{ID: "t-builder-aaaaaa", Run: run.ID}, WatchEvery: 20 * time.Millisecond,
		AdoptSupervisor: func(CrewRun) (int, error) { adopted.Add(1); return os.Getpid(), nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watchSupervisor(ctx)
	waitFor(t, "a new supervisor", func() bool {
		cur, _ := reg.GetRun(run.ID)
		return cur.Supervisor == os.Getpid() && cur.State == RunRunning
	})
	time.Sleep(100 * time.Millisecond)
	if n := adopted.Load(); n != 1 {
		t.Fatalf("started %d supervisors", n)
	}
}

// A heartbeat that stopped means a gone supervisor, even when its pid is
// still taken (reused, or a zombie).
func TestAStaleHeartbeatIsADeadSupervisor(t *testing.T) {
	_, reg := testEnv(t)
	run := CrewRun{State: RunRunning, Supervisor: os.Getpid(), Started: time.Now().Add(-time.Hour).UnixMilli()}
	run.Beat = time.Now().UnixMilli()
	if !reg.SupervisorAlive(run) {
		t.Fatal("a beating supervisor is dead")
	}
	run.Beat = time.Now().Add(-2 * SupervisorStale).UnixMilli()
	if reg.SupervisorAlive(run) {
		t.Fatal("a supervisor that stopped beating is alive")
	}
}

// Two supervisors never run one crew: the second stands down, and leaves the
// workers alone.
func TestASecondSupervisorStandsDown(t *testing.T) {
	store, reg := testEnv(t)
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)},
		func(CrewRun) (int, error) { return os.Getpid(), nil })
	if err != nil {
		t.Fatal(err)
	}
	worker := startSleeper(t)
	_ = reg.Save(Record{ID: "t-builder-bbbbbb", Profile: "builder", Run: run.ID, PID: worker.Process.Pid, State: StateIdle})
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, PID: deadPID, Interval: 10 * time.Millisecond,
		Demand: func(string, string) int { return 0 }, Spawn: func(SpawnOptions) (string, error) { return "", errors.New("no") }}
	if err := s.Run(context.Background()); !errors.Is(err, ErrSupervised) {
		t.Fatalf("second supervisor: %v", err)
	}
	if got, _ := reg.GetRun(run.ID); got.Supervisor != os.Getpid() || got.State != RunRunning {
		t.Fatalf("run after a refused supervisor: %+v", got)
	}
	if !proc.Alive(worker.Process.Pid) {
		t.Fatal("the refused supervisor stopped a worker")
	}
}

// A supervisor that finds another named for its run (it hung, and was
// replaced) stands down without stopping the crew.
func TestASupersededSupervisorLeavesTheCrew(t *testing.T) {
	store, reg := testEnv(t)
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1), Stay: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := startSleeper(t)
	_ = reg.Save(Record{ID: "t-builder-cccccc", Profile: "builder", Run: run.ID, PID: worker.Process.Pid, State: StateIdle})
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, PID: deadPID, Interval: 10 * time.Millisecond,
		Demand: func(string, string) int { return 0 }, Spawn: func(SpawnOptions) (string, error) { return "", errors.New("no") }}
	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()
	waitFor(t, "the claim", func() bool { cur, _ := reg.GetRun(run.ID); return cur.Supervisor == deadPID })
	if _, err := reg.UpdateRun(run.ID, "worker", func(cr *CrewRun) error { cr.Supervisor = os.Getpid(); return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the superseded supervisor did not stand down")
	}
	got, _ := reg.GetRun(run.ID)
	if got.State != RunRunning || !proc.Alive(worker.Process.Pid) {
		t.Fatalf("the superseded supervisor ended the crew: %+v", got)
	}
}

// A run that was stopping when its supervisor died is finished by whoever
// adopts it.
func TestAStoppingOrphanIsFinished(t *testing.T) {
	_, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)},
		func(CrewRun) (int, error) { return deadPID, nil })
	if _, err := reg.UpdateRun(run.ID, "human", func(cr *CrewRun) error { cr.State = RunStopping; return nil }); err != nil {
		t.Fatal(err)
	}
	did, err := reg.AdoptRun(run.ID, "rc", func(CrewRun) (int, error) { t.Fatal("started a supervisor for a stopping run"); return 0, nil })
	if err != nil || did != AdoptStopped {
		t.Fatalf("adopt: %q, %v", did, err)
	}
	if got, _ := reg.GetRun(run.ID); got.State != RunStopped {
		t.Fatalf("state %s", got.State)
	}
}

// Supervisors that keep dying end the run after MaxSupervisorRestarts, with a
// supervisor_failed fault in the crew log.
func TestASupervisorThatKeepsDyingFailsTheRun(t *testing.T) {
	_, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)},
		func(CrewRun) (int, error) { return deadPID, nil })
	dies := func(CrewRun) (int, error) { return deadPID, nil }
	for i := range MaxSupervisorRestarts {
		if did, err := reg.AdoptRun(run.ID, "rc", dies); err != nil || did != AdoptRestarted {
			t.Fatalf("restart %d: %q, %v", i+1, did, err)
		}
	}
	if did, err := reg.AdoptRun(run.ID, "rc", dies); err != nil || did != AdoptFailed {
		t.Fatalf("after the cap: %q, %v", did, err)
	}
	got, _ := reg.GetRun(run.ID)
	if got.State != RunFailed || got.State.Live() {
		t.Fatalf("state %s", got.State)
	}
	gd, _ := config.GlobalDir()
	faults, _ := crewlog.ReadFaults(crewlog.Dir(gd, run.ID), crewlog.Filter{Class: FaultSupervisor})
	if len(faults) != 1 {
		t.Fatalf("faults %+v", faults)
	}
	// The window resets: a run whose supervisor lived long enough starts over.
	run2, _ := reg.LaunchCrew(CrewRun{Crew: "d", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)}, dies)
	_, _ = reg.UpdateRun(run2.ID, "rc", func(cr *CrewRun) error {
		cr.Restarts, cr.RestartWindow = MaxSupervisorRestarts, time.Now().Add(-2*SupervisorRestartWindow).UnixMilli()
		return nil
	})
	if did, _ := reg.AdoptRun(run2.ID, "rc", dies); did != AdoptRestarted {
		t.Fatalf("an old window still counted: %q", did)
	}
}

// A worker that finds its crew's supervisor beyond restarting tells the user.
func TestAWorkerNotifiesWhenItsCrewFails(t *testing.T) {
	_, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)},
		func(CrewRun) (int, error) { return deadPID, nil })
	_, _ = reg.UpdateRun(run.ID, "rc", func(cr *CrewRun) error {
		cr.Restarts, cr.RestartWindow = MaxSupervisorRestarts, time.Now().UnixMilli()
		return nil
	})
	got := make(chan string, 4)
	w := &Worker{Registry: reg, Record: Record{ID: "t-builder-dddddd", Run: run.ID}, WatchEvery: 10 * time.Millisecond,
		AdoptSupervisor: func(CrewRun) (int, error) { return deadPID, nil },
		CrewNotify:      func(e string) { got <- e }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.watchSupervisor(ctx)
	select {
	case e := <-got:
		if e != notify.EventCrewStopped {
			t.Fatalf("notified %q", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
}
