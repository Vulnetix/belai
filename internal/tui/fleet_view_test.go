package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

func TestFleetTabListsWorkers(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{})
	a.width, a.height = 120, 40
	reg, err := a.fleetRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	reg.Save(fleet.Record{ID: "belai-builder-aaaaaa", Profile: "belai:builder", PID: os.Getpid(), State: fleet.StateWorking, Item: "K-123456", Done: 2, Started: now, Beat: now, Crew: "belai:delivery"})
	reg.Save(fleet.Record{ID: "belai-scout-bbbbbb", Profile: "belai:scout", PID: os.Getpid(), State: fleet.StateStopped, Reason: "stopped", Started: now - 1000, Stopped: now})

	a.agentState.tab = agentTabFleet
	a.view = viewAgent
	out := a.agentView()
	for _, want := range []string{"workers 1", "belai-builder-aaaaaa", "K-123456", "✓2", "belai:delivery", "belai-scout-bbbbbb"} {
		if !strings.Contains(out, want) {
			t.Fatalf("fleet tab lacks %q:\n%s", want, out)
		}
	}
	a.handleFleetKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if !a.agentState.fleet.showLog {
		t.Fatal("l did not toggle the log")
	}
}

func TestKanbanRoutingBadges(t *testing.T) {
	now := time.Now().UnixMilli()
	it := kanban.Item{Labels: []string{"build"}, Priority: 2, Assignee: "belai:reviewer", ClaimedBy: "w-1", LeaseUntil: now + int64(10*time.Minute/time.Millisecond)}
	got := kanbanRouting(it, now)
	for _, want := range []string{"#build", "▲2", "@belai:reviewer", "⚙ w-1 10m"} {
		if !strings.Contains(got, want) {
			t.Fatalf("routing %q lacks %q", got, want)
		}
	}
	it.LeaseUntil = now - 1
	if !strings.Contains(kanbanRouting(it, now), "lapsed") {
		t.Fatal("a lapsed claim is not marked")
	}
}
