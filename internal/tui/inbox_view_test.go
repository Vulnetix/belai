package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/kanban"
)

func addHeld(t *testing.T, a *App, title, reason string) kanban.Item {
	t.Helper()
	it, _, err := a.kb.store.Add(kanban.ItemInput{
		Title: title, Body: "the plan for " + title, List: kanban.Backlog,
		Labels: []string{"build", kanban.CheckpointLabel(reason)},
	}, a.kb.src.Get())
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestInboxListsWhatWaitsOnYouAndApproves(t *testing.T) {
	a, _ := kanbanApp(t)
	held := addHeld(t, a, "add the csv export", kanban.CheckpointPlanApproval)
	blocked := addItem(t, a, "worker gave up", kanban.Blocked)
	addItem(t, a, "ordinary work", kanban.Backlog)

	if n := a.inboxCount(); n != 2 {
		t.Fatalf("count = %d, want the checkpoint and the blocked card", n)
	}
	a.pollInbox(true)
	if got := a.inboxFooterLabel(); got != "inbox 2" {
		t.Fatalf("footer label %q", got)
	}

	a.enterInbox()
	out := ansi.Strip(a.inboxView())
	for _, want := range []string{"Waiting on you", "plan approval", held.Short(), "add the csv export", "blocked", blocked.Short()} {
		if !strings.Contains(out, want) {
			t.Fatalf("inbox lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ordinary work") {
		t.Fatalf("an ordinary card is in the inbox:\n%s", out)
	}
	if first := a.inboxState.items[0]; first.ID != held.ID {
		t.Fatalf("the checkpoint is not first: %+v", first)
	}

	a.handleInboxKey(kkey("a"))
	got, _ := a.kb.store.Get(held.ID)
	if _, still := kanban.HeldFor(got); still || got.List != kanban.Backlog {
		t.Fatalf("approve left the card held or moved it: %s %q", got.List, got.Labels)
	}
	if len(a.inboxState.items) != 1 || a.inboxState.items[0].ID != blocked.ID {
		t.Fatalf("after approve the queue is %+v", a.inboxState.items)
	}
}

func TestInboxSendBackNeedsAReasonAndReturnsTheCardToTheBacklog(t *testing.T) {
	a, _ := kanbanApp(t)
	held := addHeld(t, a, "fix the parser", kanban.CheckpointRootCause)
	a.enterInbox()

	a.handleInboxKey(kkey("x"))
	if a.inboxState.mode != "reject" {
		t.Fatal("x did not ask for a reason")
	}
	a.handleInboxKey(kkey("enter"))
	if !a.inboxState.noteErr || a.inboxState.mode != "reject" {
		t.Fatalf("an empty reason was accepted: %+v", a.inboxState)
	}
	if got, _ := a.kb.store.Get(held.ID); got.List != kanban.Backlog || len(got.History) != 1 {
		t.Fatalf("the card moved without a reason: %+v", got)
	}

	a.editor.SetValue("the cause is the lexer, not the parser")
	a.handleInboxKey(kkey("enter"))
	got, _ := a.kb.store.Get(held.ID)
	if _, still := kanban.HeldFor(got); still || got.List != kanban.Backlog || got.Bounces != 1 {
		t.Fatalf("send back: %s bounces %d labels %q", got.List, got.Bounces, got.Labels)
	}
	if note := got.LastNote(); !strings.Contains(note, "rejected: the cause is the lexer") {
		t.Fatalf("the reason is not on the card: %q", note)
	}
}

func TestInboxOnlyDecidesCheckpointsAndSaysWhenEmptyOrOff(t *testing.T) {
	a, _ := kanbanApp(t)
	blocked := addItem(t, a, "worker gave up", kanban.Blocked)
	a.enterInbox()
	a.handleInboxKey(kkey("a"))
	if got, _ := a.kb.store.Get(blocked.ID); got.List != kanban.Blocked {
		t.Fatalf("approve moved a blocked card to %s", got.List)
	}
	if !strings.Contains(a.inboxState.note, "Only a checkpoint") {
		t.Fatalf("no explanation: %q", a.inboxState.note)
	}

	if _, err := a.kb.store.Move(blocked.ID, kanban.Done, "", ""); err != nil {
		t.Fatal(err)
	}
	a.handleInboxKey(kkey("r"))
	if out := ansi.Strip(a.inboxView()); !strings.Contains(out, "Nothing is waiting on you") {
		t.Fatalf("empty inbox:\n%s", out)
	}

	a.kb = nil
	a.enterInbox()
	if out := ansi.Strip(a.inboxView()); !strings.Contains(out, "kanban board is off") {
		t.Fatalf("off inbox:\n%s", out)
	}
	if a.inboxCount() != 0 {
		t.Fatal("the board is off but the inbox counts cards")
	}
}
