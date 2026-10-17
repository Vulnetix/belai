package rc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// crewControlRig is a host with one crew run started with web controls and
// one live worker in it.
func crewControlRig(t *testing.T, webControls bool) (crewControlEnv, fleet.CrewRun, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	reg := fleet.NewRegistry(filepath.Join(home, "agents", "run"), nil)
	a, b := t.TempDir(), t.TempDir()
	run, err := reg.LaunchCrew(fleet.CrewRun{Crew: "delivery", Scope: []fleet.ScopeDir{{Path: a, Weight: 2}},
		Members: []agentprofile.Member{{Profile: "builder", Replicas: 2}}, WebControls: webControls}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := "builder-aaaaaa"
	if err := reg.Save(fleet.Record{ID: worker, Profile: "builder", Run: run.ID, Repo: a, PID: os.Getpid(), State: fleet.StateIdle}); err != nil {
		t.Fatal(err)
	}
	env := crewControlEnv{Registry: reg, Controls: true, Grace: time.Second,
		Dirs:     []Dir{{Path: a}, {Path: b}},
		CheckDir: func(string, []agentprofile.Member) error { return nil }}
	return env, run, worker
}

func TestCrewControlChangesTheRun(t *testing.T) {
	env, run, _ := crewControlRig(t, true)
	b := env.Dirs[1].Path
	steps := []sessionsync.RCCrewControl{
		{Op: OpScopeAdd, Dir: b, Weight: 3},
		{Op: OpScopeWeight, Dir: run.Scope[0].Path, Weight: 9},
		{Op: OpScopePause, Dir: b, Paused: true},
		{Op: OpReplicas, Profile: "builder", Replicas: 4},
		{Op: OpPool, Pool: []sessionsync.RCTarget{{Provider: "openai", Model: "gpt-5"}, {Provider: "anthropic", Model: "claude-x"}}},
		{Op: OpSwitchAfter, SwitchAfter: 5},
		{Op: OpFinalRetry, FinalRetrySecs: 120},
		{Op: OpMaxWorkers, MaxWorkers: 6},
	}
	for _, c := range steps {
		c.Run = run.ID
		if _, err := applyCrewControl(env, &c); err != nil {
			t.Fatalf("%s: %v", c.Op, err)
		}
	}
	got, _ := env.Registry.GetRun(run.ID)
	if len(got.Scope) != 2 || got.Scope[0].Weight != 9 || !got.Scope[1].Paused || got.Scope[1].Weight != 3 {
		t.Fatalf("scope %+v", got.Scope)
	}
	if got.Members[0].Replicas != 4 || len(got.Pool) != 2 || got.SwitchAfter != 5 || got.FinalRetrySecs != 120 || got.MaxWorkers != 6 {
		t.Fatalf("run %+v", got)
	}
	if got.UpdatedBy != "web" {
		t.Fatalf("updated by %q", got.UpdatedBy)
	}
	c := sessionsync.RCCrewControl{Run: run.ID, Op: OpScopeRemove, Dir: b}
	if _, err := applyCrewControl(env, &c); err != nil {
		t.Fatal(err)
	}
	c = sessionsync.RCCrewControl{Run: run.ID, Op: OpScopeRemove, Dir: run.Scope[0].Path}
	if _, err := applyCrewControl(env, &c); err == nil {
		t.Fatal("the last directory was removed")
	}
}

func TestCrewControlWritesAWorkersControl(t *testing.T) {
	env, run, worker := crewControlRig(t, true)
	for _, c := range []sessionsync.RCCrewControl{
		{Op: OpWorkerTarget, Worker: worker, Target: &sessionsync.RCTarget{Provider: "openai", Model: "gpt-5"}},
		{Op: OpWorkerEffort, Worker: worker, Effort: "high"},
		{Op: OpWorkerDrain, Worker: worker},
	} {
		c.Run = run.ID
		if _, err := applyCrewControl(env, &c); err != nil {
			t.Fatalf("%s: %v", c.Op, err)
		}
	}
	ct, ok := env.Registry.Control(worker)
	if !ok || ct.Target.Model != "gpt-5" || ct.Effort != "high" || !ct.Drain || ct.By != "web" || ct.Auto {
		t.Fatalf("control %+v", ct)
	}
	c := sessionsync.RCCrewControl{Run: run.ID, Op: OpWorkerAuto, Worker: worker}
	if _, err := applyCrewControl(env, &c); err != nil {
		t.Fatal(err)
	}
	if ct, _ = env.Registry.Control(worker); !ct.Auto || !ct.Target.Zero() || ct.Effort != "" {
		t.Fatalf("auto: %+v", ct)
	}
}

// Everything from the website is untrusted: a request out of shape, out of
// range or outside the run is refused, and nothing changes.
func TestCrewControlRefusesWhatItCannotCheck(t *testing.T) {
	env, run, worker := crewControlRig(t, true)
	before, _ := env.Registry.GetRun(run.ID)
	for name, c := range map[string]sessionsync.RCCrewControl{
		"unknown op":       {Op: "rm_rf"},
		"bad run id":       {Run: "../../etc", Op: OpStop},
		"no such run":      {Run: "run-nope-000000", Op: OpStop},
		"weight too big":   {Op: OpScopeWeight, Dir: run.Scope[0].Path, Weight: 11},
		"weight too small": {Op: OpScopeWeight, Dir: run.Scope[0].Path, Weight: 0},
		"unknown dir":      {Op: OpScopeWeight, Dir: "/elsewhere", Weight: 3},
		"dir not offered":  {Op: OpScopeAdd, Dir: "/etc"},
		"replicas":         {Op: OpReplicas, Profile: "builder", Replicas: agentprofile.MaxReplicas + 1},
		"not a member":     {Op: OpReplicas, Profile: "stranger", Replicas: 1},
		"profile shape":    {Op: OpReplicas, Profile: "-flag", Replicas: 1},
		"pool too big":     {Op: OpPool, Pool: make([]sessionsync.RCTarget, fleet.MaxPool+1)},
		"model shape":      {Op: OpPool, Pool: []sessionsync.RCTarget{{Provider: "openai", Model: "gpt 5; rm"}}},
		"provider shape":   {Op: OpPool, Pool: []sessionsync.RCTarget{{Provider: "--key=x", Model: "gpt-5"}}},
		"switch":           {Op: OpSwitchAfter, SwitchAfter: 0},
		"final retry":      {Op: OpFinalRetry, FinalRetrySecs: 1},
		"max workers":      {Op: OpMaxWorkers, MaxWorkers: -1},
		"worker shape":     {Op: OpWorkerDrain, Worker: "../x"},
		"foreign worker":   {Op: OpWorkerDrain, Worker: "builder-zzzzzz"},
		"no target":        {Op: OpWorkerTarget, Worker: worker},
		"effort shape":     {Op: OpWorkerEffort, Worker: worker, Effort: "MAX!"},
	} {
		if c.Run == "" {
			c.Run = run.ID
		}
		if _, err := applyCrewControl(env, &c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := applyCrewControl(env, nil); err == nil {
		t.Error("an empty request was accepted")
	}
	after, _ := env.Registry.GetRun(run.ID)
	if after.Seq != before.Seq {
		t.Fatalf("a refused request changed the run: seq %d to %d", before.Seq, after.Seq)
	}
	if _, ok := env.Registry.Control(worker); ok {
		t.Fatal("a refused request wrote a control")
	}
}

func TestCrewControlNeedsWebControlsOnBothSides(t *testing.T) {
	env, run, _ := crewControlRig(t, false)
	c := sessionsync.RCCrewControl{Run: run.ID, Op: OpMaxWorkers, MaxWorkers: 3}
	if _, err := applyCrewControl(env, &c); err == nil || !strings.Contains(err.Error(), "web controls") {
		t.Fatalf("a run without web controls: %v", err)
	}
	env2, run2, _ := crewControlRig(t, true)
	env2.Controls = false
	c = sessionsync.RCCrewControl{Run: run2.ID, Op: OpMaxWorkers, MaxWorkers: 3}
	if _, err := applyCrewControl(env2, &c); err == nil || !strings.Contains(err.Error(), "--web-controls") {
		t.Fatalf("belai rc without --web-controls: %v", err)
	}
}

func TestCrewControlNeverTrustsADirectory(t *testing.T) {
	env, run, _ := crewControlRig(t, true)
	env.CheckDir = func(string, []agentprofile.Member) error { return errors.New("not trusted") }
	c := sessionsync.RCCrewControl{Run: run.ID, Op: OpScopeAdd, Dir: env.Dirs[1].Path}
	if _, err := applyCrewControl(env, &c); err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("an untrusted directory: %v", err)
	}
}

// The daemon hands a crew_control request to CrewControl, and by default
// refuses it while belai rc runs without --web-controls.
func TestDaemonAnswersACrewControl(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	client, err := sessionsync.NewClient("http://127.0.0.1:1", func() (string, error) { return "ApiKey o:k", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got *sessionsync.RCCrewControl
	d, err := New(Options{Client: client, HostID: testHost,
		CrewControl: func(c *sessionsync.RCCrewControl) (string, error) { got = c; return "ok", nil }})
	if err != nil {
		t.Fatal(err)
	}
	want := &sessionsync.RCCrewControl{Run: "run-delivery-abcdef", Op: OpWorkerDrain, Worker: "builder-aaaaaa"}
	d.handle(context.Background(), sessionsync.Dispatch{ID: "d1", Kind: "crew_control", Control: want})
	if got != want {
		t.Fatalf("crew control got %+v", got)
	}
	d, err = New(Options{Client: client, HostID: testHost})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.o.CrewControl(want); err == nil || !strings.Contains(err.Error(), "--web-controls") {
		t.Fatalf("default crew control without --web-controls: %v", err)
	}
}

// The heartbeat carries live runs and those that ended lately, settings and
// state only, with the reason cleaned.
func TestReportRunsCarriesLiveAndRecentRuns(t *testing.T) {
	now := time.Now()
	runs := []fleet.CrewRun{
		{ID: "run-a-000001", Crew: "a", State: fleet.RunRunning, Scope: []fleet.ScopeDir{{Path: "/w/a", Weight: 3, Paused: true}},
			Members: []agentprofile.Member{{Profile: "builder", Replicas: 2}}, Pool: []fleet.Target{{Provider: "openai", Model: "gpt-5"}},
			PoolPhase: fleet.PoolSleeping, WebControls: true, Seq: 4},
		{ID: "run-b-000002", Crew: "b", State: fleet.RunStopped, Reason: "stopped\x1b[31m", Stopped: now.Add(-time.Minute).UnixMilli()},
		{ID: "run-c-000003", Crew: "c", State: fleet.RunStopped, Stopped: now.Add(-time.Hour).UnixMilli()},
		{ID: "run-d-000004", Crew: "d", State: fleet.RunOrphaned},
	}
	got := reportRuns(runs, now)
	if len(got) != 3 || got[0].ID != "run-a-000001" || got[1].ID != "run-b-000002" || got[2].State != "orphaned" {
		t.Fatalf("runs %+v", got)
	}
	a := got[0]
	if len(a.Scope) != 1 || a.Scope[0].Weight != 3 || !a.Scope[0].Paused || a.Members[0].Replicas != 2 ||
		a.Pool[0].Model != "gpt-5" || a.PoolPhase != "sleeping" || !a.WebControls || a.Seq != 4 {
		t.Fatalf("run %+v", a)
	}
	if strings.ContainsRune(got[1].Reason, 0x1b) {
		t.Fatalf("reason not cleaned: %q", got[1].Reason)
	}
}
