package rc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// steps records, in order, the turns a session ran and the branches it
// switched to, so a test can tell what happened before what.
type steps struct {
	mu   sync.Mutex
	list []string
	hist []int
}

func (s *steps) add(v string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.list = append(s.list, v)
}

func (s *steps) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.list, "|")
}

// ctxRunner is a turn that runs until its gate opens or its context ends.
type ctxRunner struct {
	s    *steps
	gate chan struct{}
	// started gets one value per turn as it begins.
	started chan string
}

func (r *ctxRunner) RunInputObserved(ctx context.Context, history []run.Turn, in agent.TurnInput, emit func(agent.Event)) (run.Result, error) {
	r.s.mu.Lock()
	r.s.list = append(r.s.list, "turn:"+in.Prompt)
	r.s.hist = append(r.s.hist, len(history))
	r.s.mu.Unlock()
	r.started <- in.Prompt
	select {
	case <-r.gate:
	case <-ctx.Done():
		return run.Result{}, ctx.Err()
	}
	emit(agent.Event{Kind: agent.EventTextKind, Text: "done: " + in.Prompt})
	return run.Result{Reply: "done: " + in.Prompt}, nil
}

// fakeCheckout switches nothing: it records the branch, and refuses "bad".
func fakeCheckout(s *steps) CheckoutRunner {
	return func(_ context.Context, branch string) (string, error) {
		if branch == "bad" {
			return "", errors.New("git refused to check out bad: it is checked out elsewhere")
		}
		s.add("checkout:" + branch)
		return "checked out " + branch, nil
	}
}

type checkoutAcks struct {
	mu   sync.Mutex
	list []string
}

func (a *checkoutAcks) ack(id, status, reason string, _ []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, id+":"+status+":"+reason)
}

func (a *checkoutAcks) String() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return strings.Join(a.list, "|")
}

// checkoutSession runs a session whose first prompt is "first", with a fake
// checkout, and returns its pieces.
func checkoutSession(t *testing.T, check func(context.Context, string) error) (*ctxRunner, *steps, *checkoutAcks, chan sessionsync.RemoteCommand, *fakeMirror, *turnlog.Log) {
	t.Helper()
	log := testLog(t)
	s := &steps{}
	r := &ctxRunner{s: s, gate: make(chan struct{}), started: make(chan string, 8)}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	cmds := make(chan sessionsync.RemoteCommand, 4)
	acks := &checkoutAcks{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- RunSession(ctx, SessionOptions{
			Agent: r, Log: log, Mirror: m, Dispatch: "d1", Prompt: "first", Idle: time.Hour,
			Commands: cmds, AckCommand: func(id, status, reason string, state json.RawMessage) { acks.ack(id, status, reason, state) },
			Checkout: fakeCheckout(s), CheckoutCheck: check,
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	return r, s, acks, cmds, m, log
}

func entries(t *testing.T, log *turnlog.Log) []session.Entry {
	t.Helper()
	es, err := log.Writer().Entries()
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// An interrupting switch stops the running turn, which ends "interrupted",
// checks the branch out and runs the turn's prompt again, recorded as a retry
// of it with the original's source and without the interrupted turn in the
// history. The session goes on.
func TestCheckoutInterruptsAndRetriesTheTurn(t *testing.T) {
	r, s, acks, cmds, m, log := checkoutSession(t, nil)
	<-r.started
	cmds <- sessionsync.RemoteCommand{ID: "c1", Checkout: "feat", Interrupt: true}
	if got := <-r.started; got != "first" {
		t.Fatalf("retried %q", got)
	}
	if got := s.String(); got != "turn:first|checkout:feat|turn:first" {
		t.Fatalf("steps = %s", got)
	}
	if got := acks.String(); got != "c1:accepted:checked out feat; retrying the turn" {
		t.Fatalf("acks = %s", got)
	}
	var interrupted string
	var users []session.Entry
	sawLine := false
	for _, e := range entries(t, log) {
		switch {
		case e.Type == "turn_state" && e.Meta["state"] == "interrupted":
			interrupted, _ = e.Meta["turn_id"].(string)
		case e.Type == "user":
			users = append(users, e)
		case e.Type == "system" && e.Content == "web: checked out feat":
			sawLine = true
		}
	}
	if interrupted == "" || !sawLine {
		t.Fatalf("interrupted turn %q, checkout line %v", interrupted, sawLine)
	}
	if len(users) != 2 || users[1].Content != "first" || users[1].Meta["retry_of"] != interrupted ||
		users[1].Meta["source"] != "web" || users[1].Meta["dispatch_id"] != "d1" {
		t.Fatalf("user entries = %+v", users)
	}
	if _, ok := users[0].Meta["retry_of"]; ok {
		t.Fatal("the original prompt is marked a retry")
	}
	s.mu.Lock()
	if s.hist[1] != 0 {
		t.Errorf("the retry saw the interrupted turn in its history (%d turns)", s.hist[1])
	}
	s.mu.Unlock()

	// The retry finishes normally and the session takes the next prompt.
	close(r.gate)
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "next"}
	if got := <-r.started; got != "next" {
		t.Fatalf("next turn %q", got)
	}
}

// A switch the pre-check refuses never interrupts the turn.
func TestCheckoutRefusedBeforeInterrupting(t *testing.T) {
	check := func(_ context.Context, branch string) error {
		return errors.New("the checkout has uncommitted changes; commit or stash them first")
	}
	r, s, acks, cmds, _, log := checkoutSession(t, check)
	<-r.started
	cmds <- sessionsync.RemoteCommand{ID: "c1", Checkout: "feat", Interrupt: true}
	eventuallyRC(t, "the refusal", func() bool { return acks.String() != "" })
	if got := acks.String(); got != "c1:refused:the checkout has uncommitted changes; commit or stash them first" {
		t.Fatalf("acks = %s", got)
	}
	close(r.gate)
	eventuallyRC(t, "the turn's end", func() bool {
		for _, e := range entries(t, log) {
			if e.Type == "turn_state" && e.Meta["state"] != "started" {
				return true
			}
		}
		return false
	})
	for _, e := range entries(t, log) {
		if e.Type == "turn_state" && e.Meta["state"] == "interrupted" {
			t.Fatal("a refused switch interrupted the turn")
		}
	}
	if got := s.String(); got != "turn:first" {
		t.Fatalf("steps = %s", got)
	}
}

// A switch git refuses after the interrupt leaves the turn interrupted and
// does not run it again.
func TestCheckoutRefusedByGitDoesNotRetry(t *testing.T) {
	r, s, acks, cmds, _, _ := checkoutSession(t, nil)
	<-r.started
	cmds <- sessionsync.RemoteCommand{ID: "c1", Checkout: "bad", Interrupt: true}
	eventuallyRC(t, "the refusal", func() bool { return acks.String() != "" })
	if got := acks.String(); !strings.HasPrefix(got, "c1:refused:git refused") {
		t.Fatalf("acks = %s", got)
	}
	select {
	case p := <-r.started:
		t.Fatalf("a refused switch retried %q", p)
	case <-time.After(100 * time.Millisecond):
	}
	if got := s.String(); got != "turn:first" {
		t.Fatalf("steps = %s", got)
	}
}

// Without interrupt the switch waits for the turn (the latest one wins) and
// runs before the next queued prompt; between turns it runs at once.
func TestCheckoutWaitsForTheTurnOrRunsWhenIdle(t *testing.T) {
	r, s, acks, cmds, m, log := checkoutSession(t, nil)
	<-r.started
	cmds <- sessionsync.RemoteCommand{ID: "c1", Checkout: "one"}
	cmds <- sessionsync.RemoteCommand{ID: "c2", Checkout: "two"}
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "second"}
	eventuallyRC(t, "queued acks", func() bool { return strings.Count(acks.String(), "|") == 2 })
	if got := acks.String(); got != "c1:queued:|c1:refused:a later branch switch replaced this one|c2:queued:" {
		t.Fatalf("acks = %s", got)
	}
	if got := s.String(); got != "turn:first" {
		t.Fatalf("checked out during the turn: %s", got)
	}
	r.gate <- struct{}{}
	if got := <-r.started; got != "second" {
		t.Fatalf("next turn %q", got)
	}
	if got := s.String(); got != "turn:first|checkout:two|turn:second" {
		t.Fatalf("steps = %s", got)
	}
	if !strings.HasSuffix(acks.String(), "|c2:accepted:checked out two") {
		t.Fatalf("acks = %s", acks.String())
	}

	r.gate <- struct{}{}
	eventuallyRC(t, "idle", func() bool {
		ended := 0
		for _, e := range entries(t, log) {
			if e.Type == "turn_state" && e.Meta["state"] == "ended" {
				ended++
			}
		}
		return ended == 2
	})
	// The loop reaches its idle wait just after the turn_state line.
	time.Sleep(20 * time.Millisecond)
	cmds <- sessionsync.RemoteCommand{ID: "c3", Checkout: "three", Interrupt: true}
	cmds <- sessionsync.RemoteCommand{ID: "c4", Checkout: "four", Line: "/caveman on"}
	eventuallyRC(t, "idle acks", func() bool { return strings.Count(acks.String(), "|") == 5 })
	if got := acks.String(); !strings.HasSuffix(got, "|c3:accepted:checked out three|c4:refused:a branch switch is one branch name") {
		t.Fatalf("acks = %s", got)
	}
	if got := s.String(); got != "turn:first|checkout:two|turn:second|checkout:three" {
		t.Fatalf("steps = %s", got)
	}
}
