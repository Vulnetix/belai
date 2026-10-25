package fleet

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
)

// -ask-off reaches the spawned worker's argv only when set.
func TestSpawnArgsAskOff(t *testing.T) {
	if got := spawnArgs("t-builder-aaaaaa", SpawnOptions{Profile: "builder"}); slices.Contains(got, "-ask-off") {
		t.Errorf("unset ask-off passed: %v", got)
	}
	if got := spawnArgs("t-builder-aaaaaa", SpawnOptions{Profile: "builder", AskOff: true}); !slices.Contains(got, "-ask-off") {
		t.Errorf("ask-off missing: %v", got)
	}
}

// A crew run launched with ask off starts every member with it, and the
// setting survives the run file.
func TestSupervisorPassesAskOff(t *testing.T) {
	store, reg := testEnv(t)
	a := t.TempDir()
	run, err := reg.LaunchCrew(CrewRun{Crew: "c", Scope: []ScopeDir{{Path: a}}, Members: members("builder", 2), AskOff: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reg.LiveRun("c"); !ok || !got.AskOff {
		t.Fatalf("run file lost ask_off: %+v", got)
	}
	var spawned []SpawnOptions
	s := &Supervisor{Registry: reg, Store: store, RunID: run.ID, Max: 8, MinTenure: time.Nanosecond,
		Demand: func(string, string) int { return 5 },
		Spawn: func(o SpawnOptions) (string, error) {
			spawned = append(spawned, o)
			id := NewID(o.Profile)
			return id, reg.Save(Record{ID: id, Profile: o.Profile, Crew: o.Crew, Run: o.Run, Repo: o.Repo, PID: os.Getpid(), State: StateIdle, Started: time.Now().UnixMilli()})
		}}
	s.Reconcile(run)
	if len(spawned) == 0 {
		t.Fatal("nothing spawned")
	}
	for _, o := range spawned {
		if !o.AskOff {
			t.Errorf("member spawned without ask off: %+v", o)
		}
	}
}

// A worker started with ask off and Bash is held to the autonomous worker's
// sandbox rule; without Bash there is nothing to confine.
func TestPreflightAskOff(t *testing.T) {
	bash := builderProfile()
	bash.Tools = []string{"Bash"}
	if err := PreflightAskOff(bash, config.Settings{}, posture.AllIgnore()); err == nil {
		t.Fatal("an ask-off Bash worker started with the sandbox off")
	}
	read := builderProfile()
	read.Tools = []string{"Read"}
	if err := PreflightAskOff(read, config.Settings{}, posture.AllIgnore()); err != nil {
		t.Fatalf("an ask-off worker without Bash refused: %v", err)
	}
}
