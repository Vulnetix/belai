package tui

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/fleet"
	"github.com/vulnetix/belai/internal/kanban"
)

// kanbanTabApp is kanbanApp with the runs panel open on the kanban tab and
// the fleet registry under a private BELAI_HOME.
func kanbanTabApp(t *testing.T) (*App, *kanban.Store) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	a, store := kanbanApp(t)
	a.runsOpen, a.runsFocus, a.runsTab = true, true, tabKanban
	return a, store
}

func TestKanbanTabListsThePaneRows(t *testing.T) {
	a, _ := kanbanTabApp(t)
	addItem(t, a, "look at the flaky test", kanban.Review)
	addItem(t, a, "wire the thing", kanban.Backlog)
	if !slices.Contains(a.runsTabNames(), "kanban") || !slices.Contains(a.runsTabNames(), "crew") {
		t.Fatalf("tabs %v lack kanban or crew", a.runsTabNames())
	}
	out := ansi.Strip(a.renderRunsPanel())
	for _, want := range []string{"[ kanban ]", "▤ kanban", "open", "in progress", "this project", "look at the flaky test", "wire the thing"} {
		if !strings.Contains(out, want) {
			t.Fatalf("kanban tab lacks %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(a.renderRunsPanel(), "\n") {
		if w := ansi.StringWidth(line); w > a.contentWidth() {
			t.Fatalf("line wider than the content (%d): %q", w, ansi.Strip(line))
		}
	}
	// The composer pane does not draw the same rows a second time.
	a.runsFocus = false
	if a.kanbanPaneVisible() {
		t.Fatal("the composer pane showed while the panel is on the kanban tab")
	}
}

func TestKanbanTabHiddenWhenBoardOff(t *testing.T) {
	a, _ := kanbanTabApp(t)
	a.kb = nil
	if slices.Contains(a.runsTabNames(), "kanban") {
		t.Fatal("the kanban tab is offered with the board off")
	}
	a.runsTab = tabProcesses
	if next := a.nextRunsTab(); next != tabCrew {
		t.Fatalf("processes → %d, want crew", next)
	}
}

func TestKanbanTabQuickActions(t *testing.T) {
	a, store := kanbanTabApp(t)
	it := addItem(t, a, "wire the thing", kanban.Backlog)

	a.handleRunsPanelKey(kkey("3"))
	got, _ := store.Get(it.ID)
	if got.List != kanban.InProgress {
		t.Fatalf("3 left the item in %s", got.List)
	}
	a.handleRunsPanelKey(kkey("+"))
	if got, _ = store.Get(it.ID); got.Priority != 1 {
		t.Fatalf("+ gave priority %d", got.Priority)
	}

	a.handleRunsPanelKey(kkey("o"))
	if !a.kanbanInputActive() || a.runsFocus {
		t.Fatal("o did not hand the composer to a note prompt")
	}
	if title, _, _ := a.kanbanComposerTitle(); !strings.Contains(title, it.Short()) {
		t.Fatalf("composer title %q does not name the item", title)
	}
	a.editor.SetValue("checked the logs")
	a.handleChatKey(kkey("enter"))
	if a.kanbanInputActive() || !a.runsFocus {
		t.Fatal("enter did not close the prompt and refocus the panel")
	}
	if got, _ = store.Get(it.ID); got.LastNote() != "checked the logs" {
		t.Fatalf("note %q", got.LastNote())
	}

	a.handleRunsPanelKey(kkey("L"))
	a.editor.SetValue("build, docs")
	a.handleChatKey(kkey("enter"))
	if got, _ = store.Get(it.ID); !slices.Equal(got.Labels, []string{"build", "docs"}) {
		t.Fatalf("labels %v", got.Labels)
	}

	a.handleRunsPanelKey(kkey("]")) // backlog
	a.handleRunsPanelKey(kkey("n"))
	a.editor.SetValue("a new request")
	a.handleChatKey(kkey("enter"))
	items, _ := store.Search(kanban.Query{Text: "a new request"})
	if len(items) != 1 || items[0].List != kanban.Backlog {
		t.Fatalf("n filed %v", items)
	}

	a.handleRunsPanelKey(kkey("n"))
	a.handleChatKey(kkey("esc"))
	if a.kanbanInputActive() {
		t.Fatal("esc left the prompt open")
	}

	a.handleRunsPanelKey(kkey("enter"))
	if a.runsFocus || !strings.Contains(a.editor.Value(), "a new request") {
		t.Fatalf("enter did not prefill the item's prompt: focus %v, %q", a.runsFocus, a.editor.Value())
	}
}

func TestKanbanTabReleasesAClaim(t *testing.T) {
	a, store := kanbanTabApp(t)
	it := addItem(t, a, "held item", kanban.Backlog)
	if _, err := store.ClaimID(it.ID, kanban.ClaimRequest{Worker: "w-1", Host: "h", Lease: time.Minute}); err != nil {
		t.Fatal(err)
	}
	a.handleRunsPanelKey(kkey("u"))
	if got, _ := store.Get(it.ID); got.ClaimedBy != "" {
		t.Fatalf("u left the claim with %s", got.ClaimedBy)
	}
}

func TestKanbanTabHandsAnItemToTheCrew(t *testing.T) {
	a, store := kanbanTabApp(t)
	// A live worker, so w does not start a real crew.
	reg, err := a.fleetRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	reg.Save(fleet.Record{ID: "belai-scout-aaaaaa", Profile: "belai:scout", PID: os.Getpid(), State: fleet.StateIdle, Started: now, Beat: now})
	it := addItem(t, a, "survey the parser", kanban.Review)

	a.handleRunsPanelKey(kkey("w"))
	got, _ := store.Get(it.ID)
	if got.List != kanban.Backlog || !slices.Contains(got.Labels, "scout") {
		t.Fatalf("w left %s with %v", got.List, got.Labels)
	}
}

func TestPaneOpensTheKanbanTabOnItsSelection(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a, _ := kanbanApp(t)
	addItem(t, a, "first", kanban.Review)
	second := addItem(t, a, "second", kanban.Review)
	if !a.focusKanbanPane() {
		t.Fatal("the pane did not focus")
	}
	items, _ := a.kanbanPaneItems()
	for i, it := range items {
		if it.ID == second.ID {
			a.kb.pane.sel = i
		}
	}
	a.kb.pane.filter = 2 // review
	a.openKanbanTabFromPane(items)
	if !a.runsOpen || a.runsTab != tabKanban || a.kb.pane.focus {
		t.Fatal("f9 from the pane did not open the kanban tab")
	}
	if kanbanTabFilters[a.kb.tab.filter].label != "review" {
		t.Fatalf("filter %q not carried over", kanbanTabFilters[a.kb.tab.filter].label)
	}
	if sel, ok := a.kanbanTabSelected(); !ok || sel.ID != second.ID {
		t.Fatalf("selection not carried over: %v", sel.Title)
	}
}

func TestCrewTabListsWorkersAndCyclesCrews(t *testing.T) {
	a, _ := kanbanTabApp(t)
	reg, err := a.fleetRegistry()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	reg.Save(fleet.Record{ID: "belai-builder-aaaaaa", Profile: "belai:builder", PID: os.Getpid(), State: fleet.StateWorking, Item: "K-123456", Done: 2, Started: now, Beat: now, Crew: "belai:delivery"})
	a.runsTab = tabCrew
	if cmd := a.enterRunsTab(); cmd == nil {
		t.Fatal("the crew tab did not start the fleet tick")
	}
	if !slices.Contains(a.runsTabNames(), "crew 1") {
		t.Fatalf("tabs %v lack the live count", a.runsTabNames())
	}
	out := ansi.Strip(a.renderRunsPanel())
	for _, want := range []string{"crew belai:delivery", "1 of", "belai-builder-aaaaaa", "K-123456", "✓2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("crew tab lacks %q:\n%s", want, out)
		}
	}
	a.handleRunsPanelKey(kkey("c"))
	if c, _ := a.chosenCrew(); c.Name != "belai:security" {
		t.Fatalf("c chose %s", c.Name)
	}
	a.agentState.fleet.ticking = false
	if a.handleFleetTick() == nil {
		t.Fatal("the fleet tick stopped while the crew tab is open")
	}
	a.runsOpen = false
	a.agentState.fleet.ticking = false
	if a.handleFleetTick() != nil {
		t.Fatal("the fleet tick kept going with the panel closed")
	}
}
