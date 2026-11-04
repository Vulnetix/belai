package main

import (
	"bytes"
	"context"
	"flag"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

func TestParseMemberModels(t *testing.T) {
	c := agentprofile.Crew{Name: "c", Members: []agentprofile.Member{{Profile: "builder", Replicas: 1}, {Profile: "reviewer", Replicas: 1}}}
	got, err := parseMemberModels([]string{"builder=anthropic/claude-sonnet-5-5", "reviewer=builtin/pix-fast"}, c)
	if err != nil || len(got) != 2 || got["builder"] != (fleet.Target{Provider: "anthropic", Model: "claude-sonnet-5-5"}) || got["reviewer"] != (fleet.Target{Provider: "builtin", Model: "pix-fast"}) {
		t.Fatalf("%+v, %v", got, err)
	}
	if got, err := parseMemberModels(nil, c); got != nil || err != nil {
		t.Fatalf("none: %+v, %v", got, err)
	}
	for _, bad := range [][]string{
		{"scout=p/m"},                  // not a member
		{"builder=p/m", "builder=q/n"}, // twice
		{"builder=model-only"},         // no provider
		{"builder=-p/m"},               // reads as a flag
		{"builder"},                    // no target
	} {
		if _, err := parseMemberModels(bad, c); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A crew start stores its member models on the run, where the supervisor
// starts each member on its own model.
func TestCrewStartStoresMemberModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	store := kanban.Open(filepath.Join(home, "kanban"))
	reg := fleet.NewRegistry(filepath.Join(home, "agents", "run"), store)
	c, err := agentprofile.LoadCrew("belai:delivery")
	if err != nil {
		t.Fatal(err)
	}
	member := c.Members[0].Profile
	var out bytes.Buffer
	l := crewLaunch{crew: c.Name, dirs: []string{t.TempDir()}, trust: true, memberModels: []string{member + "=builtin/pix-smart"},
		stdout: &out, registry: reg, startSupervisor: func(fleet.CrewRun) (int, error) { return 0, nil },
		checkDir: func(string, []agentprofile.Member) error { return nil }}
	if code, err := startCrewRun(l); code != 0 || err != nil {
		t.Fatalf("start: %d, %v (%s)", code, err, out.String())
	}
	run, ok := reg.LiveRun(c.Name)
	if !ok || run.MemberModels[member] != (fleet.Target{Provider: "builtin", Model: "pix-smart"}) || run.StartTarget(member) != (fleet.Target{Provider: "builtin", Model: "pix-smart"}) {
		t.Fatalf("run %+v", run)
	}
	l.memberModels = []string{"nobody=p/m"}
	if code, err := startCrewRun(l); code != 2 || err == nil || !strings.Contains(err.Error(), "not a member") {
		t.Fatalf("a non-member: %d, %v", code, err)
	}
}

func TestMemberModelNeedsCrew(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	var out bytes.Buffer
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(&out)
	code, err := agentStart(context.Background(), fs, []string{"-member-model", "builder=p/m", "builder"}, &out, &out)
	if code != 2 || err == nil || !strings.Contains(err.Error(), "-member-model") {
		t.Fatalf("%d, %v", code, err)
	}
}
