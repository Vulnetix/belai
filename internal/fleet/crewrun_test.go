package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sessionctl"
)

func members(pairs ...any) []agentprofile.Member {
	var out []agentprofile.Member
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, agentprofile.Member{Profile: pairs[i].(string), Replicas: pairs[i+1].(int)})
	}
	return out
}

func TestParseScopeDir(t *testing.T) {
	for in, want := range map[string]ScopeDir{
		"/a":     {Path: "/a", Weight: 1},
		"/a:3":   {Path: "/a", Weight: 3},
		"C:/x":   {Path: "C:/x", Weight: 1},
		"/a:b:2": {Path: "/a:b", Weight: 2},
	} {
		got, err := ParseScopeDir(in)
		if err != nil || got != want {
			t.Errorf("ParseScopeDir(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	if _, err := ParseScopeDir("/a:11"); err == nil {
		t.Error("a weight over the maximum was taken")
	}
}

// The crew goes where the work is: replicas follow weight times demand, and a
// profile with no work anywhere is spread by weight alone.
func TestAllocateFollowsWeightAndDemand(t *testing.T) {
	run := CrewRun{Crew: "c", Scope: []ScopeDir{{Path: "/a", Weight: 1}, {Path: "/b", Weight: 3}, {Path: "/c", Weight: 1, Paused: true}},
		Members: members("builder", 4, "scout", 1)}
	demand := map[Slot]int{{"builder", "/a"}: 10, {"builder", "/b"}: 10, {"builder", "/c"}: 50}
	got := Allocate(run, func(p, r string) int { return demand[Slot{p, r}] }, 0)
	if got[Slot{"builder", "/a"}] != 1 || got[Slot{"builder", "/b"}] != 3 || got[Slot{"builder", "/c"}] != 0 {
		t.Fatalf("builders: %v", got)
	}
	if got[Slot{"scout", "/b"}] != 1 || got[Slot{"scout", "/a"}] != 0 {
		t.Fatalf("a scout with no work goes to the heaviest repository: %v", got)
	}
	// Work only in /a takes every builder there, whatever /b weighs.
	demand = map[Slot]int{{"builder", "/a"}: 2}
	got = Allocate(run, func(p, r string) int { return demand[Slot{p, r}] }, 0)
	if got[Slot{"builder", "/a"}] != 4 || got[Slot{"builder", "/b"}] != 0 {
		t.Fatalf("builders follow the work: %v", got)
	}
}

func TestAllocateHonoursTheCap(t *testing.T) {
	run := CrewRun{Crew: "c", Scope: []ScopeDir{{Path: "/a", Weight: 1}, {Path: "/b", Weight: 1}}, Members: members("builder", 4)}
	got := Allocate(run, func(p, r string) int {
		if r == "/a" {
			return 9
		}
		return 1
	}, 2)
	n := 0
	for _, c := range got {
		n += c
	}
	if n != 2 || got[Slot{"builder", "/a"}] != 2 {
		t.Fatalf("cut to the cap keeping the most work per worker: %v", got)
	}
}

func TestLaunchCrewRunsOnceAndUpdatesBumpTheSeq(t *testing.T) {
	_, reg := testEnv(t)
	run := CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)}
	started := 0
	launched, err := reg.LaunchCrew(run, func(CrewRun) (int, error) { started++; return os.Getpid(), nil })
	if err != nil || started != 1 || launched.State != RunRunning || launched.Supervisor != os.Getpid() || !strings.HasPrefix(launched.ID, "run-c-") {
		t.Fatalf("launch: %+v, %v", launched, err)
	}
	if _, err := reg.LaunchCrew(run, nil); !errors.Is(err, ErrRunLive) {
		t.Fatalf("a second launch of the same crew: %v", err)
	}
	up, err := reg.UpdateRun(launched.ID, "human", func(cr *CrewRun) error {
		cr.Scope[0].Weight = 7
		return nil
	})
	if err != nil || up.Seq != launched.Seq+1 || up.UpdatedBy != "human" || up.Scope[0].Weight != 7 {
		t.Fatalf("update: %+v, %v", up, err)
	}
	if _, err := reg.UpdateRun(launched.ID, "human", func(cr *CrewRun) error { cr.Scope = nil; return nil }); err == nil {
		t.Fatal("an empty scope was saved")
	}
}

// A run whose supervisor died is no longer live, so the crew can launch again.
func TestARunWithADeadSupervisorIsFailed(t *testing.T) {
	_, reg := testEnv(t)
	run := CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)}
	launched, err := reg.LaunchCrew(run, func(CrewRun) (int, error) { return 1 << 30, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.LiveRun("c"); ok {
		t.Fatal("a run with a dead supervisor is still live")
	}
	got, _ := reg.GetRun(launched.ID)
	if got.State != RunFailed {
		t.Fatalf("state %s", got.State)
	}
}

// The supervisor starts what the allocation lacks and drains the surplus,
// idle workers first.
func TestSupervisorReconciles(t *testing.T) {
	store, reg := testEnv(t)
	a, b := t.TempDir(), t.TempDir()
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: a}, {Path: b}}, Members: members("builder", 2)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var spawned []SpawnOptions
	demand := map[string]int{a: 5}
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, Max: 8, MinTenure: time.Nanosecond,
		Demand: func(_, repo string) int { return demand[repo] },
		Spawn: func(o SpawnOptions) (string, error) {
			spawned = append(spawned, o)
			id := NewID(o.Profile)
			return id, reg.Save(Record{ID: id, Profile: o.Profile, Crew: o.Crew, Run: o.Run, Repo: o.Repo, PID: os.Getpid(), State: StateIdle, Started: time.Now().Add(-time.Hour).UnixMilli()})
		}}
	s.Reconcile(run)
	if len(spawned) != 2 || spawned[0].Repo != a || spawned[1].Repo != a || !spawned[0].Stay || spawned[0].Run != run.ID {
		t.Fatalf("spawned %+v", spawned)
	}
	// The work moves to b: both builders in a are drained, two start in b.
	demand = map[string]int{b: 5}
	spawned = nil
	s.Reconcile(run)
	drained := 0
	for _, rec := range reg.RunWorkers(run.ID) {
		if c, ok := reg.Control(rec.ID); ok && c.Drain {
			drained++
			if rec.Repo != a {
				t.Fatalf("drained a worker in %s", rec.Repo)
			}
		}
	}
	if drained != 2 || len(spawned) != 2 || spawned[0].Repo != b {
		t.Fatalf("drained %d, spawned %+v", drained, spawned)
	}
}

func TestSupervisorEndsARunThatRanDry(t *testing.T) {
	store, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("builder", 1)}, nil)
	now := time.Now()
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, Max: 8, now: func() time.Time { return now },
		Demand: func(string, string) int { return 0 },
		Spawn:  func(SpawnOptions) (string, error) { return "", errors.New("no") }}
	s.quiet = now.Add(-time.Hour)
	if why := s.Reconcile(run); !strings.HasPrefix(why, "done:") {
		t.Fatalf("reason %q", why)
	}
	run.Stay = true
	if why := s.Reconcile(run); why != "" {
		t.Fatalf("a staying run ended: %q", why)
	}
}

// fakeControls is worker controls whose resolver refuses one model.
func fakeControls() *WorkerControls {
	return NewWorkerControls(config.Settings{}, run.Config{Provider: "p1", Model: "m1"}, sessionctl.Env{}, func(st sessionctl.State) (run.Config, error) {
		if st.Model == "nope" {
			return run.Config{}, errors.New("no credentials for that provider")
		}
		return run.Config{Provider: st.Provider, Model: st.Model}, nil
	})
}

func TestAControlChangesTheModelOrIsRefused(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, builderProfile(), complete)
	w.Controls = fakeControls()
	w.Record.ID = "t-builder-aaaaaa"
	w.applyControl() // nothing written yet
	if _, err := reg.SetControl(w.Record.ID, "human", func(c *Control) { c.Target = Target{"p2", "m2"} }); err != nil {
		t.Fatal(err)
	}
	w.applyControl()
	if got := w.currentTarget(); got != (Target{"p2", "m2"}) || w.Record.Model != "m2" || !w.pinned {
		t.Fatalf("target %v, record %s, pinned %v", got, w.Record.Model, w.pinned)
	}
	if a, _ := reg.Ack(w.Record.ID); a.Status != AckAccepted || a.Seq != 1 {
		t.Fatalf("ack %+v", a)
	}
	_, _ = reg.SetControl(w.Record.ID, "human", func(c *Control) { c.Target = Target{"p3", "nope"} })
	w.applyControl()
	a, _ := reg.Ack(w.Record.ID)
	if a.Status != AckRefused || !strings.Contains(a.Reason, "no credentials") || w.currentTarget().Model != "m2" {
		t.Fatalf("a model the host cannot run: ack %+v, target %v", a, w.currentTarget())
	}
	// The same control is applied once.
	_, _ = reg.SetControl(w.Record.ID, "human", func(c *Control) { c.Target, c.Drain = Target{}, true })
	w.applyControl()
	if !w.Record.Draining {
		t.Fatal("drain not taken")
	}
}

// A drained worker finishes and leaves; a worker in a paused repository of
// its run leaves too.
func TestADrainedOrPausedWorkerLeaves(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, quickPoll(), complete)
	w.Once = false
	w.Stay = true
	_, _ = reg.SetControl(w.Record.ID, "supervisor", func(c *Control) { c.Drain = true })
	rec, took := runFor(t, w, 5*time.Second)
	if !strings.HasPrefix(rec.Reason, "drained") || took >= 5*time.Second {
		t.Fatalf("record %+v after %s", rec, took)
	}

	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: w.Repo, Paused: true}, {Path: t.TempDir()}}, Members: members("t-builder", 1)}, nil)
	w2 := newWorker(t, store, reg, quickPoll(), complete)
	w2.Once, w2.Stay = false, true
	w2.Repo = run.Scope[0].Path
	w2.Record.Run = run.ID
	rec, _ = runFor(t, w2, 5*time.Second)
	if rec.Reason != "this repository is paused in the crew run" {
		t.Fatalf("record %+v", rec)
	}
}

// After FallbackAfter outages in a row a run's worker moves to its fallback at
// once instead of backing off, and after the probe interval it tries the
// primary again; one more outage there sends it straight back.
func TestFallbackAndProbe(t *testing.T) {
	store, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("t-builder", 1),
		Fallbacks: []Target{{"p3", "nope"}, {"p2", "m2"}}, FallbackAfter: 2, ProbeSecs: 60}, nil)
	w := newWorker(t, store, reg, builderProfile(), complete)
	w.Controls = fakeControls()
	w.Repo, w.Record.Run = run.Scope[0].Path, run.ID
	now := time.Now()
	w.now = func() time.Time { return now }
	w.syncTarget()
	w.primary = w.currentTarget()
	if w.refreshRun() != "" {
		t.Fatal("the run refused the worker")
	}
	w.outages = 1
	if w.fallbackStep() {
		t.Fatal("moved before the streak")
	}
	w.outages = 2
	if !w.fallbackStep() || w.currentTarget() != (Target{"p2", "m2"}) || w.Record.Fallback != "p2/m2" || w.outages != 0 {
		t.Fatalf("fallback skipped the unrunnable target to p2/m2: %v, %q, %d", w.currentTarget(), w.Record.Fallback, w.outages)
	}
	w.probeStep()
	if w.Record.Fallback == "" {
		t.Fatal("probed before the interval")
	}
	now = now.Add(2 * time.Minute)
	w.probeStep()
	if w.currentTarget() != (Target{"p1", "m1"}) || w.Record.Fallback != "" || w.outages != 1 {
		t.Fatalf("probe: %v, %q, outages %d", w.currentTarget(), w.Record.Fallback, w.outages)
	}
	w.outages++
	if !w.fallbackStep() || w.Record.Fallback != "p2/m2" {
		t.Fatal("one outage on the probe did not go back to the fallback")
	}
	// A human pin holds against the policy.
	w.pinned = true
	w.outages = 5
	if w.fallbackStep() {
		t.Fatal("the policy moved a pinned worker")
	}
}

// The run's primary set in the control room moves its workers at the next
// loop.
func TestARunPrimaryChangeMovesTheWorker(t *testing.T) {
	store, reg := testEnv(t)
	run, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("t-builder", 1)}, nil)
	w := newWorker(t, store, reg, builderProfile(), complete)
	w.Controls = fakeControls()
	w.Repo, w.Record.Run = run.Scope[0].Path, run.ID
	w.primary = w.currentTarget()
	_, _ = reg.UpdateRun(run.ID, "human", func(cr *CrewRun) error { cr.Primary = Target{"p9", "m9"}; return nil })
	w.refreshRun()
	if w.currentTarget() != (Target{"p9", "m9"}) || w.primary != (Target{"p9", "m9"}) {
		t.Fatalf("target %v", w.currentTarget())
	}
}

func TestClassifyFault(t *testing.T) {
	pe := func(status int, body string) error { return &run.ProviderError{Status: status, Body: body} }
	cases := []struct {
		err    error
		cause  error
		stop   run.StopReason
		code   string
		outage bool
	}{
		{pe(503, "unavailable"), nil, run.StopError, FaultUnavailable, true},
		{fmt.Errorf("goal: %w", pe(502, "")), nil, "", FaultUnavailable, true},
		{pe(429, "slow down"), nil, "", FaultRateLimit, true},
		{pe(429, "insufficient_quota"), nil, "", FaultQuota, false},
		{pe(401, "bad key"), nil, "", FaultAuth, false},
		{pe(400, "maximum context length exceeded"), nil, "", FaultOverflow, false},
		{pe(422, "nope"), nil, "", FaultRejected, false},
		{&url.Error{Op: "Post", URL: "https://x/quota", Err: syscall.ECONNRESET}, nil, "", FaultTransport, true},
		{errors.New("provider returned 504: gateway"), nil, "", FaultUnavailable, true},
		{errors.New("something else"), nil, "", FaultError, false},
		{context.Canceled, errWallBudget, "", FaultWallBudget, false},
		{nil, errTokenBudget, "", FaultTokenBudget, false},
		{nil, errLeaseLost, "", FaultLeaseLost, false},
		{nil, nil, run.StopStalled, FaultStalled, false},
		{nil, nil, run.StopMaxPasses, FaultMaxPasses, false},
		{nil, nil, "", FaultIncomplete, false},
	}
	for i, c := range cases {
		got := classifyFault(c.err, c.cause, c.stop)
		if got.Code != c.code || got.Outage != c.outage {
			t.Errorf("%d: %v / %v → %+v, want %s outage=%v", i, c.err, c.cause, got, c.code, c.outage)
		}
	}
}

// A transport failure that outlasts the retries is an outage too, not a
// failed attempt.
func TestATransportFailureIsNotAnAttempt(t *testing.T) {
	store, reg := testEnv(t)
	w := newWorker(t, store, reg, roundsPatcher(1), func(context.Context, Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopError}, &url.Error{Op: "Post", URL: "https://api/x", Err: syscall.ECONNRESET}
	})
	w.Repo = gitRepo(t)
	it := findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(it.ID)
	rec, _ := reg.Get(w.Record.ID)
	if got.Attempts != 0 || rec.LastFault != FaultTransport || rec.Outages != 1 {
		t.Fatalf("attempts %d, fault %q, outages %d", got.Attempts, rec.LastFault, rec.Outages)
	}
}

func TestDemandCountsClaimableCards(t *testing.T) {
	store, _ := testEnv(t)
	for i, l := range [][]string{{"build"}, {"build"}, {"other"}} {
		if _, _, err := store.Add(kanban.ItemInput{Title: fmt.Sprint("card ", i), Labels: l}, kanban.Provenance{}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.Demand(kanban.ClaimRequest{Labels: []string{"build"}, Profile: "p", Worker: "w", Lease: time.Minute})
	if err != nil || n != 2 {
		t.Fatalf("demand %d, %v", n, err)
	}
}

// A run's worker records each fault in the run's crew log: a code, and the
// harness note as its one cleaned detail; the audit mirror carries the code
// only, never the note.
func TestARunWorkerFaultReachesTheCrewLog(t *testing.T) {
	store, reg := testEnv(t)
	repo := gitRepo(t)
	cr, _ := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: repo}}, Members: members("t-patcher", 1)}, nil)
	w := newWorker(t, store, reg, roundsPatcher(1), func(context.Context, Turn) (run.Result, error) {
		return run.Result{StopReason: run.StopError}, &run.ProviderError{Status: 401, Body: "bad key \x1b[31msecret-ish"}
	})
	w.Repo, w.Record.Run, w.Record.Crew = repo, cr.ID, "c"
	findingCardOn(t, store, w, kanban.Backlog, "vuln")
	if err := w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	gd, _ := config.GlobalDir()
	dir := crewlog.Dir(gd, cr.ID)
	faults, err := crewlog.ReadFaults(dir, crewlog.Filter{})
	if err != nil || len(faults) != 1 || faults[0].Class != FaultAuth || faults[0].Status != 401 || strings.ContainsRune(faults[0].Detail, '\x1b') {
		t.Fatalf("faults %+v, %v", faults, err)
	}
	entries, _ := crewlog.ReadEntries(dir, crewlog.Filter{Kind: "worker.fault"})
	if len(entries) != 1 || entries[0].Data["class"] != FaultAuth {
		t.Fatalf("entries %+v", entries)
	}
	for _, e := range entries {
		for _, v := range e.Data {
			if strings.Contains(v, "bad key") {
				t.Fatal("the audit mirror carries error text")
			}
		}
	}
	if bad, err := crewlog.Verify(dir); bad != -1 || err != nil {
		t.Fatalf("chain broken at %d: %v", bad, err)
	}
}
