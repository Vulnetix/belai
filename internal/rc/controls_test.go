package rc

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionctl"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
)

// builds counts agent sessions built for each state.
type builds struct {
	mu     sync.Mutex
	states []sessionctl.State
	runner *fakeRunner
}

func (b *builds) build(st sessionctl.State) (Runner, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.states = append(b.states, st)
	return b.runner, nil
}

func (b *builds) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.states)
}

func (b *builds) last() sessionctl.State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.states[len(b.states)-1]
}

func testState() sessionctl.State {
	return sessionctl.FromSettings(config.Settings{}, "auto", "anthropic", "claude-x", "")
}

type cmdAcks struct {
	mu   sync.Mutex
	acks []string
	last json.RawMessage
}

func (c *cmdAcks) ack(id, status, reason string, state json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.acks = append(c.acks, id+":"+status)
	c.last = state
}

func (c *cmdAcks) list() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.acks, "|")
}

func eventuallyRC(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestControllerAppliesDisplayAtOnceAndRebuildsForModel(t *testing.T) {
	b := &builds{runner: &fakeRunner{}}
	c, err := NewController(testState(), sessionctl.Env{}, b.build)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Key: "ctrl+r"}, true); err != nil {
		t.Fatal(err)
	}
	if b.count() != 1 || c.State().Reasoning != "shown" {
		t.Fatalf("a display change rebuilt or did not apply: %d %+v", b.count(), c.State())
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/caveman on"}, true); err != nil {
		t.Fatal(err)
	}
	if b.count() != 2 || !b.last().Caveman {
		t.Fatalf("caveman did not rebuild: %d", b.count())
	}
	// While a turn runs the rebuild waits for the boundary.
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/caveman off"}, false); err != nil {
		t.Fatal(err)
	}
	if b.count() != 2 {
		t.Fatal("rebuilt mid-turn")
	}
	if err := c.Settle(); err != nil || b.count() != 3 || b.last().Caveman {
		t.Fatalf("settle: %v %d", err, b.count())
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/help"}, true); err == nil {
		t.Fatal("a non-control line was accepted")
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Line: "/caveman on", Key: "f2"}, true); err == nil {
		t.Fatal("a command with both a line and a key was accepted")
	}
	if _, err := c.Apply(sessionsync.RemoteCommand{Key: "f3"}, true); err == nil {
		t.Fatal("guardrails off without the host's flag")
	}
	if c.Mode() != "" {
		t.Fatalf("auto mode forced %q", c.Mode())
	}
}

func TestRunSessionTakesCommandsBetweenAndDuringTurns(t *testing.T) {
	r := &fakeRunner{block: make(chan struct{})}
	b := &builds{runner: r}
	ctl, err := NewController(testState(), sessionctl.Env{}, b.build)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	cmds := make(chan sessionsync.RemoteCommand, 4)
	acks := &cmdAcks{}
	var changed int
	var mu sync.Mutex
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error)
	go func() {
		done <- RunSession(ctx, SessionOptions{
			Log: turnlog.New(nil), Mirror: m, Prompt: "first", Idle: time.Hour,
			Controls: ctl, Commands: cmds, AckCommand: acks.ack,
			Changed: func(json.RawMessage, bool) { mu.Lock(); changed++; mu.Unlock() },
		})
	}()
	// During the first turn: the mode applies to the next turn, caveman waits.
	cmds <- sessionsync.RemoteCommand{ID: "c1", Key: "shift+tab"}
	cmds <- sessionsync.RemoteCommand{ID: "c2", Line: "/caveman on"}
	cmds <- sessionsync.RemoteCommand{ID: "c3", Line: "!rm -rf /"}
	eventuallyRC(t, "three acks", func() bool { return strings.Count(acks.list(), ":") == 3 })
	if b.count() != 1 {
		t.Fatal("rebuilt during the turn")
	}
	close(r.block)
	eventuallyRC(t, "rebuild at the boundary", func() bool { return b.count() == 2 })
	if got := acks.list(); got != "c1:accepted|c2:accepted|c3:refused" {
		t.Fatalf("acks = %s", got)
	}
	if !b.last().Caveman || ctl.State().Mode != "agent" {
		t.Fatalf("state = %+v", ctl.State())
	}
	// Between turns a rebuild happens at once.
	cmds <- sessionsync.RemoteCommand{ID: "c4", Key: "f2"}
	eventuallyRC(t, "rebuild between turns", func() bool { return b.count() == 3 })
	m.prompts <- sessionsync.RemotePrompt{ID: "p1", Content: "second"}
	eventuallyRC(t, "second turn", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.prompts) == 2 })
	mu.Lock()
	if changed < 4 {
		t.Errorf("Changed called %d times", changed)
	}
	mu.Unlock()
	cancel()
	<-done
}

// answerMirror is a fake AnswerMirror.
type answerMirror struct {
	mu      sync.Mutex
	answers chan sessionsync.RemoteAnswer
	acks    []string
}

func (a *answerMirror) Answers() <-chan sessionsync.RemoteAnswer { return a.answers }
func (a *answerMirror) Nudge()                                   {}
func (a *answerMirror) AckAnswer(id, status, reason, entryID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acks = append(a.acks, id+":"+status)
}

func testLog(t *testing.T) *turnlog.Log {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	store, err := session.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	key, err := session.KeyFor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, err := session.NewWriter(store, key, session.MustID(), session.Meta{})
	if err != nil {
		t.Fatal(err)
	}
	return turnlog.New(w)
}

// openAsk waits for the newest ask entry and returns its id.
func openAsk(t *testing.T, log *turnlog.Log, n int) string {
	t.Helper()
	var id string
	eventuallyRC(t, "ask entry", func() bool {
		es, _ := log.Writer().Entries()
		seen := 0
		for _, e := range es {
			if e.Type == "ask" {
				seen++
				if seen == n {
					id = e.ID
				}
			}
		}
		return id != ""
	})
	return id
}

func TestAskBridgeTakesOnlyTheOpenAsk(t *testing.T) {
	log := testLog(t)
	m := &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 4)}
	b := newAskBridge(log, m, time.Second)
	ask := &agent.AskRequest{Name: "Bash", Subject: "make", Args: map[string]any{"command": "make"}}

	got := make(chan bool)
	go func() { got <- b.permission(context.Background(), ask) }()
	id := openAsk(t, log, 1)
	m.answers <- sessionsync.RemoteAnswer{ID: "a0", AskID: "other", Kind: "permission", Payload: json.RawMessage(`{"decision":"allow_once"}`)}
	m.answers <- sessionsync.RemoteAnswer{ID: "a1", AskID: id, Kind: "clarify", Payload: json.RawMessage(`{}`)}
	m.answers <- sessionsync.RemoteAnswer{ID: "a2", AskID: id, Kind: "permission", Payload: json.RawMessage(`{"decision":"allow_always"}`)}
	if !<-got {
		t.Fatal("allow_always denied")
	}
	m.mu.Lock()
	if strings.Join(m.acks, "|") != "a0:refused|a1:refused|a2:accepted" {
		t.Fatalf("acks = %v", m.acks)
	}
	m.mu.Unlock()
	// Allow-always holds for this session without asking again.
	if !b.permission(context.Background(), ask) {
		t.Fatal("allow_always not remembered")
	}
	// Nobody answers: denied after the wait.
	other := &agent.AskRequest{Name: "Write", Subject: "x.go"}
	if b.permission(context.Background(), other) {
		t.Fatal("an unanswered ask was allowed")
	}
}

func TestRunSessionDeniesAsksNobodyCanAnswer(t *testing.T) {
	r := &askingRunner{}
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = RunSession(ctx, SessionOptions{Agent: r, Log: turnlog.New(nil), Mirror: m, Prompt: "go", Idle: time.Hour})
	}()
	eventuallyRC(t, "the ask", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.answered })
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.allowed {
		t.Fatal("an ask with nobody to answer was allowed")
	}
}

type askingRunner struct {
	mu                sync.Mutex
	answered, allowed bool
}

func (a *askingRunner) RunInputObserved(ctx context.Context, _ []run.Turn, _ agent.TurnInput, emit func(agent.Event)) (run.Result, error) {
	reply := make(chan agent.PermissionAskReply, 1)
	emit(agent.Event{Kind: agent.EventPermissionAskKind, Ask: &agent.AskRequest{Name: "Bash"}, AskReply: reply})
	got := <-reply
	a.mu.Lock()
	a.answered, a.allowed = true, got.Allow
	a.mu.Unlock()
	return run.Result{}, nil
}

// Turning ask off while a permission is open allows it and closes the ask in
// the transcript, instead of holding the turn for the whole wait with nobody
// able to answer (web answers go off with ask).
func TestAskBridgeAllowsTheOpenPermissionWhenAskIsTurnedOff(t *testing.T) {
	log := testLog(t)
	m := &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 4)}
	b := newAskBridge(log, m, time.Hour)
	ask := &agent.AskRequest{Name: "Bash", Subject: "curl", Args: map[string]any{"command": "curl https://example.com"}}

	got := make(chan bool, 1)
	go func() { got <- b.permission(context.Background(), ask) }()
	id := openAsk(t, log, 1)

	select {
	case <-got:
		t.Fatal("the ask resolved before ask was turned off")
	case <-time.After(50 * time.Millisecond):
	}
	b.releaseOpen()

	select {
	case allow := <-got:
		if !allow {
			t.Fatal("ask off must allow the open permission, as AskDisabled does")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the open ask was not released")
	}
	es, _ := log.Writer().Entries()
	closed := false
	for _, e := range es {
		if e.Type == "ask_answer" && e.Meta["ask_id"] == id && e.Meta["decision"] == "allow_once" && e.Meta["source"] == "host" {
			closed = true
		}
	}
	if !closed {
		t.Fatal("the ask was not closed in the transcript")
	}

	// A later ask waits again: the release is for the ask open then, not a standing allow.
	later := make(chan bool, 1)
	go func() { later <- b.permission(context.Background(), &agent.AskRequest{Name: "Write", Subject: "x.go"}) }()
	openAsk(t, log, 2)
	select {
	case <-later:
		t.Fatal("a later ask was resolved without an answer")
	case <-time.After(100 * time.Millisecond):
	}
	b.releaseOpen()
	<-later
}

func TestAskBridgeDismissesAnOpenQuestionnaireWhenAskIsTurnedOff(t *testing.T) {
	log := testLog(t)
	m := &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 4)}
	b := newAskBridge(log, m, time.Hour)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if got := b.clarify(context.Background(), &clarify.Questionnaire{}, false); len(got.Items) != 0 {
			t.Errorf("a dismissed questionnaire answered %v", got)
		}
	}()
	openAsk(t, log, 1)
	b.releaseOpen()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the open questionnaire was not released")
	}
}

// The screenshot case: a permission is open (ask was on), the person turns ask
// off, and the turn goes on instead of waiting out the answer nobody can give.
func TestRunSessionAskTurnedOffWhileAPermissionIsOpenLetsTheTurnGoOn(t *testing.T) {
	r := &askingRunner{}
	st := testState()
	st.Ask = true
	ctl, err := NewController(st, sessionctl.Env{}, func(sessionctl.State) (Runner, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	log := testLog(t)
	m := &fakeMirror{prompts: make(chan sessionsync.RemotePrompt, 4)}
	answers := &answerMirror{answers: make(chan sessionsync.RemoteAnswer, 4)}
	cmds := make(chan sessionsync.RemoteCommand, 4)
	acks := &cmdAcks{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = RunSession(ctx, SessionOptions{
			Log: log, Mirror: m, Prompt: "go", Idle: time.Hour, AskWait: time.Hour,
			Controls: ctl, Commands: cmds, AckCommand: acks.ack, Answers: answers,
		})
	}()
	openAsk(t, log, 1)
	r.mu.Lock()
	stuck := !r.answered
	r.mu.Unlock()
	if !stuck {
		t.Fatal("the ask resolved before anyone answered or turned ask off")
	}

	cmds <- sessionsync.RemoteCommand{ID: "c1", Line: "/ask off"}
	eventuallyRC(t, "the turn to go on", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.answered })
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.allowed {
		t.Fatal("ask off must allow the open permission")
	}
	if ctl.Asks() {
		t.Fatal("ask is still on")
	}
}
