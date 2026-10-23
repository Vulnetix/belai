package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

func TestCheckStayIdle(t *testing.T) {
	for _, c := range []struct {
		d       time.Duration
		stay    bool
		wantErr string
	}{
		{0, false, ""},
		{0, true, ""},
		{time.Hour, true, ""},
		{1000 * time.Hour, true, ""},
		{time.Hour, false, "needs -stay"},
		{-time.Second, true, "negative"},
	} {
		err := checkStayIdle(c.d, c.stay)
		if (err == nil) != (c.wantErr == "") || (err != nil && !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("checkStayIdle(%s, %v) = %v, want %q", c.d, c.stay, err, c.wantErr)
		}
	}
	if got := stayIdleSecs(0); got != 0 {
		t.Errorf("zero is the default, got %d", got)
	}
	if got := stayIdleSecs(2 * time.Hour); got != 7200 {
		t.Errorf("got %d", got)
	}
	if got, want := stayIdleSecs(1000*time.Hour), int(fleet.MaxStayIdleMax/time.Second); got != want {
		t.Errorf("got %d, want the hard maximum %d", got, want)
	}
}

// A start that asks to stay upgrades a run already going that would have ended
// when the board ran dry, and never turns a staying run back.
func TestJoinCrewRunUpgradesToStay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	store := kanban.Open(filepath.Join(home, "kanban"))
	reg := fleet.NewRegistry(filepath.Join(home, "agents", "run"), store)
	dir := t.TempDir()
	run, err := reg.LaunchCrew(fleet.CrewRun{Crew: "c", Scope: []fleet.ScopeDir{{Path: dir}},
		Members: []agentprofile.Member{{Profile: "builder", Replicas: 1}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	l := crewLaunch{crew: "c", stay: true, stayIdle: 2 * time.Hour, fill: true, stdout: &out}
	if code, err := joinCrewRun(reg, run, []fleet.ScopeDir{{Path: dir}}, l); code != 0 || err != nil {
		t.Fatalf("join: %d, %v", code, err)
	}
	got, _ := reg.GetRun(run.ID)
	if !got.Stay || got.StayIdleMaxSecs != 7200 || !strings.Contains(out.String(), "stays for new work") {
		t.Fatalf("run %+v, output %q", got, out.String())
	}
	// A later start without stay leaves it staying.
	if code, err := joinCrewRun(reg, got, []fleet.ScopeDir{{Path: dir}}, crewLaunch{crew: "c", fill: true, stdout: &out}); code != 0 || err != nil {
		t.Fatalf("join: %d, %v", code, err)
	}
	if again, _ := reg.GetRun(run.ID); !again.Stay {
		t.Fatalf("a start without stay turned the run back: %+v", again)
	}
}
