package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/crewlog"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/trustgate"
)

// controlRoomApp is an App over a private board and registry, with a crew run
// of belai:delivery over the session's directory and one worker.
func controlRoomApp(t *testing.T) (*App, *fleet.Registry, fleet.CrewRun) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a, _ := kanbanApp(t)
	a.crewStarter = func(fleet.CrewRun) (int, error) { return os.Getpid(), nil }
	reg, err := a.fleetRegistry()
	if err != nil {
		t.Fatal(err)
	}
	c, err := agentprofile.LoadCrew("belai:delivery")
	if err != nil {
		t.Fatal(err)
	}
	run, err := reg.LaunchCrew(fleet.NewCrewRun(c, []fleet.ScopeDir{{Path: a.workdir, Weight: 2}}), a.crewStarter)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Save(fleet.Record{ID: "belai-builder-aaaaaa", Profile: "belai:builder", Crew: c.Name, Run: run.ID, Repo: run.Scope[0].Path,
		PID: os.Getpid(), State: fleet.StateWorking, Item: "K-000001", Provider: "openai", Model: "gpt-5", Outages: 2,
		OutageUntil: time.Now().Add(time.Minute).UnixMilli(), TokensLive: 12000, Started: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	return a, reg, run
}

func crewCard(t *testing.T, a *App, title string, l kanban.List) kanban.Item {
	t.Helper()
	it, _, err := a.kb.store.Add(kanban.ItemInput{Title: title, List: l, Assignee: "crew:belai:delivery"}, a.kb.src.Get())
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func roomText(a *App) string { return ansi.Strip(a.controlRoomView()) }

func TestControlRoomShowsTheRun(t *testing.T) {
	a, _, run := controlRoomApp(t)
	crewCard(t, a, "wire the login form", kanban.Backlog)
	a.handleCommand("/crew")
	if a.view != viewControlRoom {
		t.Fatalf("/crew opened view %v", a.view)
	}
	out := roomText(a)
	for _, want := range []string{run.ID, "belai:delivery", "workers 1/4", "openai/gpt-5 down ×1", "belai-builder-aaaaaa", "outages 2", "12k tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("the workers tab lacks %q:\n%s", want, out)
		}
	}
	a.handleControlRoomKey(kkey("2"))
	if out := roomText(a); !strings.Contains(out, "wire the login form") {
		t.Fatalf("the board tab lacks the crew's card:\n%s", out)
	}
	found := false
	for _, e := range screenEntries {
		found = found || (e.view == viewControlRoom && e.key == "o")
	}
	if !found {
		t.Fatal("no screen switcher entry for the control room")
	}
}

// Marking cards and blocking them moves every marked card and records one
// crew.control fact per card in the crew log.
func TestControlRoomBulkBlockAndUnblock(t *testing.T) {
	a, _, run := controlRoomApp(t)
	one := crewCard(t, a, "first", kanban.Backlog)
	two := crewCard(t, a, "second", kanban.Backlog)
	crewCard(t, a, "third", kanban.Backlog)
	a.push(viewControlRoom)
	a.handleControlRoomKey(kkey("2"))
	a.handleControlRoomKey(kkey(" "))
	a.handleControlRoomKey(kkey(" "))
	a.handleControlRoomKey(kkey("b"))
	for _, id := range []string{one.ID, two.ID} {
		if it, _ := a.kb.store.Get(id); it.List != kanban.Blocked {
			t.Fatalf("%s is in %s", it.Short(), it.List)
		}
	}
	if !strings.Contains(a.crew.status, "block: 2 of 2") {
		t.Fatalf("status %q", a.crew.status)
	}
	gd, _ := config.GlobalDir()
	entries, _ := crewlog.ReadEntries(crewlog.Dir(gd, run.ID), crewlog.Filter{Kind: "crew.control"})
	blocks := 0
	for _, e := range entries {
		if e.Outcome == "block" {
			blocks++
		}
	}
	if blocks != 2 {
		t.Fatalf("crew.control entries %+v", entries)
	}
	// Mark all and unblock: the blocked cards go back to the backlog.
	a.handleControlRoomKey(kkey("a"))
	a.handleControlRoomKey(kkey("u"))
	if it, _ := a.kb.store.Get(one.ID); it.List != kanban.Backlog {
		t.Fatalf("unblock left %s in %s", it.Short(), it.List)
	}
}

// Scope edits go to the run file at once: a weight, a paused directory.
func TestControlRoomEditsTheScope(t *testing.T) {
	a, reg, run := controlRoomApp(t)
	a.push(viewControlRoom)
	a.handleControlRoomKey(kkey("4"))
	a.handleControlRoomKey(kkey("+"))
	a.handleControlRoomKey(kkey(" "))
	got, _ := reg.GetRun(run.ID)
	if got.Scope[0].Weight != 3 || !got.Scope[0].Paused || got.UpdatedBy != "human" || got.Seq < run.Seq+2 {
		t.Fatalf("run %+v", got)
	}
	// Replicas of the first member.
	a.crew.sel[crScope] = len(got.Scope)
	a.handleControlRoomKey(kkey("+"))
	got, _ = reg.GetRun(run.ID)
	if got.Members[0].Replicas != run.Members[0].Count()+1 {
		t.Fatalf("replicas %+v", got.Members)
	}
}

// A model typed for a worker becomes its control file; the primary and the
// fallbacks become the run's.
func TestControlRoomSetsModels(t *testing.T) {
	a, reg, run := controlRoomApp(t)
	a.push(viewControlRoom)
	a.handleControlRoomKey(kkey("M"))
	if a.crew.mode != "model" {
		t.Fatalf("mode %q", a.crew.mode)
	}
	a.editor.SetValue("anthropic/claude-sonnet-5-5")
	a.handleControlRoomKey(kkey("enter"))
	c, ok := reg.Control("belai-builder-aaaaaa")
	if !ok || c.Target != (fleet.Target{Provider: "anthropic", Model: "claude-sonnet-5-5"}) || c.By != "human" {
		t.Fatalf("control %+v", c)
	}
	a.handleControlRoomKey(kkey("3"))
	a.handleControlRoomKey(kkey("P"))
	a.editor.SetValue("openai/gpt-5, google/gemini-3")
	a.handleControlRoomKey(kkey("enter"))
	a.handleControlRoomKey(kkey(">"))
	got, _ := reg.GetRun(run.ID)
	if len(got.Pool) != 2 || got.Pool[1] != (fleet.Target{Provider: "google", Model: "gemini-3"}) || got.SwitchAfter != fleet.DefaultSwitch+1 {
		t.Fatalf("run %+v", got)
	}
	// Every worker onto the selected pool target, still cycling the pool.
	a.handleControlRoomKey(kkey("down"))
	a.handleControlRoomKey(kkey("f"))
	if c, _ := reg.Control("belai-builder-aaaaaa"); c.Target.Model != "gemini-3" || !c.Auto {
		t.Fatalf("pool target wrote %+v", c)
	}
	if out := roomText(a); !strings.Contains(out, "pool openai/gpt-5 → google/gemini-3 ↺") {
		t.Fatalf("header lacks the pool:\n%s", out)
	}
}

// A crew asleep before its final retry says so, with the time left.
func TestControlRoomShowsThePoolAsleep(t *testing.T) {
	a, reg, run := controlRoomApp(t)
	_, _ = reg.UpdateRun(run.ID, "supervisor", func(r *fleet.CrewRun) error {
		r.Pool = []fleet.Target{{Provider: "openai", Model: "gpt-5"}}
		r.PoolPhase, r.SleepUntil = fleet.PoolSleeping, time.Now().Add(10*time.Minute).UnixMilli()
		return nil
	})
	a.push(viewControlRoom)
	if out := roomText(a); !strings.Contains(out, "the crew sleeps, final retry in") {
		t.Fatalf("no sleep shown:\n%s", out)
	}
}

// Stopping the run asks first.
func TestControlRoomStopRunConfirms(t *testing.T) {
	a, reg, run := controlRoomApp(t)
	a.push(viewControlRoom)
	a.handleControlRoomKey(kkey("S"))
	if a.crew.mode != "stop-run" {
		t.Fatalf("mode %q", a.crew.mode)
	}
	a.handleControlRoomKey(kkey("n"))
	if got, _ := reg.GetRun(run.ID); got.State != fleet.RunRunning {
		t.Fatal("a no stopped the run")
	}
	a.handleControlRoomKey(kkey("S"))
	_, cmd := a.handleControlRoomKey(kkey("y"))
	if cmd == nil {
		t.Fatal("no stop command")
	}
	// The supervisor is this test process, so the run is only marked stopping.
	cmd()
	if got, _ := reg.GetRun(run.ID); got.State != fleet.RunStopping {
		t.Fatalf("state %s", got.State)
	}
}

func TestControlRoomShowsFaults(t *testing.T) {
	a, _, run := controlRoomApp(t)
	gd, _ := config.GlobalDir()
	l, err := crewlog.Open(crewlog.Dir(gd, run.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = l.AppendFault(crewlog.Fault{Run: run.ID, Worker: "belai-builder-aaaaaa", Item: "K-000001", Class: fleet.FaultUnavailable, Status: 503, Outcome: "transient", Detail: "the provider was unavailable"})
	_ = l.AppendFault(crewlog.Fault{Run: run.ID, Worker: "belai-builder-aaaaaa", Item: "K-000002", Class: fleet.FaultAuth, Status: 401, Outcome: "failed"})
	l.Close()
	a.push(viewControlRoom)
	a.handleControlRoomKey(kkey("5"))
	out := roomText(a)
	if !strings.Contains(out, "provider_unavailable 503") || !strings.Contains(out, "provider_auth 401") {
		t.Fatalf("faults tab:\n%s", out)
	}
	a.handleControlRoomKey(kkey("c")) // the first class seen
	if a.crew.class == "" || len(a.crew.faults) != 1 {
		t.Fatalf("class %q, %d faults", a.crew.class, len(a.crew.faults))
	}
}

// With no crew running the room offers a launch, which writes a run the same
// way the CLI does.
func TestControlRoomLaunchesACrew(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a, _ := kanbanApp(t)
	started := 0
	a.crewStarter = func(fleet.CrewRun) (int, error) { started++; return os.Getpid(), nil }
	if err := trustgate.Grant(repoRootOf(a.workdir), nil); err != nil {
		t.Fatal(err)
	}
	a.push(viewControlRoom)
	if out := roomText(a); !strings.Contains(out, "no crew running") {
		t.Fatalf("empty room:\n%s", out)
	}
	a.handleControlRoomKey(kkey("n"))
	a.editor.SetValue("belai:delivery " + a.workdir + ":4")
	a.handleControlRoomKey(kkey("enter"))
	if a.crew.errMsg != "" {
		// A host where the members cannot preflight (no sandbox backend)
		// refuses the launch with the reason; that is the check working.
		if strings.Contains(a.crew.errMsg, "sandbox") || strings.Contains(a.crew.errMsg, "trusted") {
			t.Skip(a.crew.errMsg)
		}
		t.Fatal(a.crew.errMsg)
	}
	reg, _ := a.fleetRegistry()
	run, ok := reg.LiveRun("belai:delivery")
	if !ok || started != 1 || run.Scope[0].Weight != 4 {
		t.Fatalf("run %+v, started %d", run, started)
	}
}
