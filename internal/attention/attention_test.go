package attention

import (
	"slices"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

func card(id, title string, list kanban.List, updated int64, labels ...string) kanban.Item {
	return kanban.Item{ID: id + "-0000-0000-0000-000000000000", Title: title, List: list, Updated: updated, Labels: labels}
}

func titles(items []Item) []string {
	var out []string
	for _, i := range items {
		out = append(out, i.Title)
	}
	return out
}

func TestQueueOrdersCheckpointsFirstThenLongestWaiting(t *testing.T) {
	cards := []kanban.Item{
		card("c1", "blocked old", kanban.Blocked, 10),
		card("c2", "plain backlog", kanban.Backlog, 5),
		card("c3", "checkpoint newer", kanban.Backlog, 300, kanban.CheckpointLabel(kanban.CheckpointVisual)),
		card("c4", "checkpoint older", kanban.Backlog, 100, kanban.CheckpointLabel(kanban.CheckpointPlanApproval)),
		{ID: "c5-0000", Title: "for a person", List: kanban.Review, Updated: 50, Assignee: "person:ada"},
		card("c6", "blocked new", kanban.Blocked, 200),
	}
	got := titles(FromCards(cards))
	want := []string{"checkpoint older", "checkpoint newer", "for a person", "blocked old", "blocked new"}
	if !slices.Equal(got, want) {
		t.Fatalf("queue = %q, want %q", got, want)
	}
	if Count(cards) != 5 {
		t.Fatalf("count = %d", Count(cards))
	}
}

func TestQueueLeavesOutWhatNobodyIsWaitingOn(t *testing.T) {
	held := card("a1", "claimed", kanban.Backlog, 1, kanban.CheckpointLabel(kanban.CheckpointPrePR))
	held.ClaimedBy = "w1"
	gone := card("a2", "deleted", kanban.Blocked, 1)
	gone.Deleted = true
	done := kanban.Item{ID: "a3-0000", Title: "done for a person", List: kanban.Done, Assignee: "person:ada"}
	worker := kanban.Item{ID: "a4-0000", Title: "for a worker", List: kanban.Backlog, Assignee: "belai:builder"}
	if got := FromCards([]kanban.Item{held, gone, done, worker}); len(got) != 0 {
		t.Fatalf("queue = %v", titles(got))
	}
}

func TestItemsSayWhyTheyAreWaiting(t *testing.T) {
	cp := FromCards([]kanban.Item{card("b1", "t", kanban.Backlog, 1, kanban.CheckpointLabel(kanban.CheckpointRootCause))})[0]
	if cp.Kind != KindCheckpoint || cp.Reason != kanban.CheckpointRootCause || cp.Label() != "root cause" || !cp.Decidable() {
		t.Fatalf("checkpoint item %+v", cp)
	}
	newer := FromCards([]kanban.Item{card("b2", "t", kanban.Backlog, 1, "checkpoint:from_a_newer_belai")})[0]
	if newer.Label() != "from a newer belai" || !newer.Decidable() {
		t.Fatalf("an unknown checkpoint must still wait for a person: %+v", newer)
	}
	person := FromCards([]kanban.Item{{ID: "b3-0000", Title: "t", List: kanban.Review, Assignee: "person:ada"}})[0]
	if person.Kind != KindPerson || person.Label() != "person:ada" || person.Decidable() {
		t.Fatalf("person item %+v", person)
	}
	blocked := FromCards([]kanban.Item{card("b4", "t", kanban.Blocked, 1)})[0]
	if blocked.Kind != KindBlocked || blocked.Label() != "blocked" || blocked.Decidable() {
		t.Fatalf("blocked item %+v", blocked)
	}
}
