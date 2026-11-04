package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
)

const modelDeniedBody = `{"error":{"message":"model m1 is denied by organisation policy","type":"policy_violation","code":"model_denied"}}`

func deniedRunner(calls *int) TurnRunner {
	return func(context.Context, Turn) (run.Result, error) {
		*calls++
		return run.Result{StopReason: run.StopError}, &run.ProviderError{Status: 403, Body: modelDeniedBody}
	}
}

// A model the policy refuses costs the card nothing: it goes back to the list
// it was claimed from with no attempt counted and no skip entry, and the worker
// stops instead of failing every other card on the board the same way.
func TestAConfigFaultReturnsTheCardUncountedAndStopsTheWorker(t *testing.T) {
	store, reg := testEnv(t)
	first, _, _ := store.Add(kanban.ItemInput{Title: "one", Labels: []string{"build"}}, kanban.Provenance{})
	second, _, _ := store.Add(kanban.ItemInput{Title: "two", Labels: []string{"build"}}, kanban.Provenance{})
	calls := 0
	w := newWorker(t, store, reg, builderProfile(), deniedRunner(&calls))
	w.Once = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil || calls != 1 {
		t.Fatalf("the worker ran %d turns (timed out: %v); it must stop after the first", calls, ctx.Err() != nil)
	}
	var worked, untouched kanban.Item
	for _, id := range []string{first.ID, second.ID} {
		it, _ := store.Get(id)
		if len(it.History) > 1 {
			worked = it
		} else {
			untouched = it
		}
	}
	if worked.ID == "" || untouched.ID == "" {
		t.Fatal("expected one card worked and one untouched")
	}
	if worked.List != kanban.Backlog || worked.Attempts != 0 || worked.ClaimedBy != "" || !strings.Contains(worked.History[len(worked.History)-1].Note, "not counted as an attempt") {
		t.Fatalf("released %+v", worked)
	}
	if untouched.ClaimedBy != "" || untouched.List != kanban.Backlog {
		t.Fatalf("the second card was claimed: %+v", untouched)
	}
	if len(w.failures) != 0 {
		t.Fatalf("a skip entry for a card that was never tried: %v", w.failures)
	}
	rec, _ := reg.Get(w.Record.ID)
	if rec.State != StateStopped || rec.Reason != "stopped: provider_policy" || rec.LastFault != FaultPolicy || rec.Failed != 0 || rec.Outages != 0 {
		t.Fatalf("record %+v", rec)
	}
}

// A crew worker records the stop on its run, with the refused target, and its
// fault reaches the crew log once, as provider_policy.
func TestACrewWorkersPolicyStopIsRecordedOnTheRun(t *testing.T) {
	store, reg := testEnv(t)
	repo := t.TempDir()
	cr, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: repo}}, Members: members("t-builder", 1), Pool: []Target{{"p1", "m1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Add(kanban.ItemInput{Title: "one", Labels: []string{"build"}}, kanban.Provenance{})
	calls := 0
	w := newWorker(t, store, reg, builderProfile(), deniedRunner(&calls))
	w.Controls = fakeControls()
	w.Repo, w.Record.Run, w.Record.Crew, w.Once = repo, cr.ID, "c", false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("%d turns", calls)
	}
	got, _ := reg.GetRun(cr.ID)
	stop, ok := got.PolicyStops["t-builder"]
	if !ok || stop.Target != (Target{"p1", "m1"}) || stop.Code != FaultPolicy || !got.PolicyHeld("t-builder") {
		t.Fatalf("policy stops %+v", got.PolicyStops)
	}
	rec, _ := reg.Get(w.Record.ID)
	if rec.Reason != "stopped: provider_policy on p1/m1" {
		t.Fatalf("reason %q", rec.Reason)
	}
	gd, _ := config.GlobalDir()
	entries, _ := crewlog.ReadEntries(crewlog.Dir(gd, cr.ID), crewlog.Filter{Kind: "worker.fault"})
	if len(entries) != 1 || entries[0].Data["class"] != FaultPolicy {
		t.Fatalf("entries %+v", entries)
	}
}

// The supervisor does not start a held member again on the refused target; a
// new model for it, or a person's edit of the run, starts it; and a run whose
// every member is held ends with the reason.
func TestSupervisorHoldsAMemberThePolicyStopped(t *testing.T) {
	store, reg := testEnv(t)
	a := t.TempDir()
	cr, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: a}}, Members: members("builder", 1, "reviewer", 1), Pool: []Target{{"p1", "m1"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hold := func(by string) CrewRun {
		up, err := reg.UpdateRun(cr.ID, by, func(r *CrewRun) error {
			r.PolicyStops = map[string]PolicyStop{"builder": {Target: Target{"p1", "m1"}, Code: FaultPolicy}}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return up
	}
	var spawned []SpawnOptions
	s := &Supervisor{Registry: reg, Store: store, RunID: cr.ID, Max: 8, MinTenure: time.Nanosecond,
		Demand: func(string, string) int { return 5 },
		Spawn: func(o SpawnOptions) (string, error) {
			spawned = append(spawned, o)
			return NewID(o.Profile), nil
		}}
	if why := s.Reconcile(hold("worker")); why != "" {
		t.Fatalf("ended: %q", why)
	}
	if len(spawned) != 1 || spawned[0].Profile != "reviewer" || spawned[0].Model != "m1" {
		t.Fatalf("spawned %+v", spawned)
	}
	// The member gets a model of its own: it starts on that.
	up, err := reg.UpdateRun(cr.ID, "launch", func(r *CrewRun) error {
		r.MemberModels = map[string]Target{"builder": {"p2", "m2"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	spawned = nil
	s.Reconcile(up)
	if len(spawned) != 2 || spawned[0].Profile != "builder" || spawned[0].Provider != "p2" || spawned[0].Model != "m2" {
		t.Fatalf("spawned %+v", spawned)
	}
	// A person's edit clears the hold.
	held := hold("worker")
	if held.PolicyHeld("builder") {
		t.Fatal("the builder starts on p2/m2 and is not held")
	}
	edited, _ := reg.UpdateRun(cr.ID, "human", func(r *CrewRun) error { r.Scope[0].Weight = 3; return nil })
	if len(edited.PolicyStops) != 0 {
		t.Fatalf("a person's edit kept %+v", edited.PolicyStops)
	}
	// Every member held: the run ends.
	all, _ := reg.UpdateRun(cr.ID, "worker", func(r *CrewRun) error {
		r.MemberModels = nil
		r.PolicyStops = map[string]PolicyStop{"builder": {Target: Target{"p1", "m1"}}, "reviewer": {Target: Target{"p1", "m1"}}}
		return nil
	})
	spawned = nil
	if why := s.Reconcile(all); !strings.HasPrefix(why, "stopped: provider_policy") || len(spawned) != 0 {
		t.Fatalf("reason %q, spawned %+v", why, spawned)
	}
	// Every member held on a spent quota: the run ends on provider_quota.
	quota, _ := reg.UpdateRun(cr.ID, "worker", func(r *CrewRun) error {
		r.PolicyStops = map[string]PolicyStop{"builder": {Target: Target{"p1", "m1"}, Code: FaultQuota}, "reviewer": {Target: Target{"p1", "m1"}, Code: FaultQuota}}
		return nil
	})
	if why := s.Reconcile(quota); !strings.HasPrefix(why, "stopped: provider_quota") {
		t.Fatalf("quota reason %q", why)
	}
}

// A member model is the member's first pool target, ahead of the run's pool;
// the worker starts on it and cycles through the rest.
func TestAMemberModelLeadsThatMembersPool(t *testing.T) {
	r := CrewRun{Pool: []Target{{"p1", "m1"}, {"p2", "m2"}}, MemberModels: map[string]Target{"t-builder": {"p2", "m2"}}}
	if got := r.PoolFor("t-builder"); len(got) != 2 || got[0] != (Target{"p2", "m2"}) || got[1] != (Target{"p1", "m1"}) {
		t.Fatalf("pool %v", got)
	}
	if got := r.PoolFor("other"); len(got) != 2 || got[0] != (Target{"p1", "m1"}) {
		t.Fatalf("other's pool %v", got)
	}
	if got := (CrewRun{MemberModels: map[string]Target{"a": {"p3", "m3"}}}).StartTarget("a"); got != (Target{"p3", "m3"}) {
		t.Fatalf("start %v", got)
	}

	store, reg := testEnv(t)
	cr, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: t.TempDir()}}, Members: members("t-builder", 1),
		Pool: []Target{{"p1", "m1"}}, MemberModels: map[string]Target{"t-builder": {"p2", "m2"}}, SwitchAfter: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := newWorker(t, store, reg, builderProfile(), complete)
	w.Controls = fakeControls()
	w.Repo, w.Record.Run = cr.Scope[0].Path, cr.ID
	if err := w.setTarget(Target{"p2", "m2"}, ""); err != nil {
		t.Fatal(err)
	}
	if w.refreshRun() != "" || w.currentTarget() != (Target{"p2", "m2"}) {
		t.Fatalf("the pool moved the member off its model: %v", w.currentTarget())
	}
	w.outages = 1
	if !w.poolStep() || w.currentTarget() != (Target{"p1", "m1"}) {
		t.Fatalf("after an outage on its model: %v", w.currentTarget())
	}
}

func TestParseMemberModel(t *testing.T) {
	crew := []string{"builder", "reviewer"}
	p, tg, err := ParseMemberModel("builder=openrouter/anthropic/claude-x", crew)
	if err != nil || p != "builder" || tg != (Target{"openrouter", "anthropic/claude-x"}) {
		t.Fatalf("%q %+v %v", p, tg, err)
	}
	for _, bad := range []string{"builder", "=p/m", "builder=", "builder=model", "builder=/m", "scout=p/m", "builder=-p/m", "builder=p/-m", "builder=p/m x", "builder=p/" + strings.Repeat("m", MaxTargetPart+1)} {
		if _, _, err := ParseMemberModel(bad, crew); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// The run itself refuses a member model for a profile outside the crew.
	r := CrewRun{Crew: "c", Scope: []ScopeDir{{Path: "/x"}}, Members: members("builder", 1), MemberModels: map[string]Target{"scout": {"p", "m"}}}
	if err := r.Validate(); err == nil {
		t.Fatal("a member model for a non-member was accepted")
	}
}
