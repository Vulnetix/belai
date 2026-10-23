package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/gitsync"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Node states, as the status bar shows them.
const (
	// StateLocalOnly is same-host messaging with no relay (no Vulnetix login).
	StateLocalOnly = "local only"
	// StateRelay is same-host messaging plus the relay through Vulnetix.
	StateRelay = "relay"
	// StateBlackout is the relay stopped by the organisation's A2A blackout.
	StateBlackout = "blackout"
)

// Options configures a Node.
type Options struct {
	// Dir is the A2A directory (Dir(config.GlobalDir())).
	Dir       string
	HostID    string
	Hostname  string
	SessionID string
	Profile   string
	// Filter is the a2a.filter mode.
	Filter string
	// Git returns the session's latest repository reading (gitsync.Info as
	// JSON), or nil. It is polled; it must not block.
	Git func() json.RawMessage
	// PollEvery is how often the bus is read (1s).
	PollEvery time.Duration
	// Debounce is the broadcaster's (DefaultDebounce).
	Debounce time.Duration
	Now      func() time.Time
}

// Event is something the session should record or act on: an envelope it
// received that passed the filter, or one it sent itself (Out), or a change
// of relay state (State).
type Event struct {
	Env Envelope
	// Out is an envelope this session sent.
	Out bool
	// Tell is set on a received broadcast the model has not been told.
	Tell bool
	// Overlap is a received broadcast's files that this session also changed.
	Overlap []string
	// Question is the message a received reply answers, when this session
	// still holds it.
	Question *Envelope
	// State is set on a relay state event: StateLocalOnly, StateRelay or
	// StateBlackout.
	State string
}

// Node is one process's A2A endpoint for its live session.
type Node struct {
	opts    Options
	bus     *Bus
	audit   *Audit
	mailbox *Mailbox
	bc      *Broadcaster
	events  chan Event
	now     func() time.Time

	mu      sync.Mutex
	self    AgentCard
	relay   *Relay
	last    *Envelope // the last broadcast sent
	closed  bool
	lastGit string
}

// Errors the message tools report to the model.
var (
	ErrNoPeer    = errors.New("no live agent has that id; agents you can message are the ones whose broadcasts you received")
	ErrNoMessage = errors.New("no A2A message or broadcast with that messageId reached this session")
	ErrSelf      = errors.New("that id is this session's own")
)

// Open opens the bus and audit in opts.Dir and returns the Node. Call Run.
func Open(opts Options) (*Node, error) {
	if !uuidShape.MatchString(opts.HostID) || !uuidShape.MatchString(opts.SessionID) {
		return nil, invalid("host or session id")
	}
	bus, err := OpenBus(opts.Dir)
	if err != nil {
		return nil, err
	}
	au, err := OpenAudit(opts.Dir)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	bus.now = now
	if opts.PollEvery <= 0 {
		opts.PollEvery = time.Second
	}
	opts.Filter = NormalizeFilter(opts.Filter)
	n := &Node{opts: opts, bus: bus, audit: au, mailbox: NewMailbox(), bc: &Broadcaster{Debounce: opts.Debounce},
		events: make(chan Event, 256), now: now}
	n.self = n.card(opts.SessionID, opts.Profile)
	_ = n.putCard()
	n.mailbox.SetLocal(bus.Cards(), n.self.ID, now())
	return n, nil
}

func (n *Node) card(sessionID, profile string) AgentCard {
	c, _ := AgentCard{HostID: n.opts.HostID, SessionID: sessionID, Profile: profile, Hostname: n.opts.Hostname,
		Capabilities: []string{CapMessage}}.clean()
	return c
}

// Self is the session's Agent Card.
func (n *Node) Self() AgentCard {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.self
}

// Bus is the node's bus.
func (n *Node) Bus() *Bus { return n.bus }

// Audit is the node's local audit.
func (n *Node) Audit() *Audit { return n.audit }

// Mailbox is the session's mailbox.
func (n *Node) Mailbox() *Mailbox { return n.mailbox }

// Broadcaster is the session's broadcaster.
func (n *Node) Broadcaster() *Broadcaster { return n.bc }

// Events delivers what the session should record or act on.
func (n *Node) Events() <-chan Event { return n.events }

// Filter is the node's filter mode.
func (n *Node) Filter() string { return n.opts.Filter }

// SetRelay attaches the relay (sync is on). nil detaches it.
func (n *Node) SetRelay(r *Relay) {
	n.mu.Lock()
	n.relay = r
	n.mu.Unlock()
	n.emit(Event{State: n.State()})
}

// State is the node's relay state.
func (n *Node) State() string {
	n.mu.Lock()
	r := n.relay
	n.mu.Unlock()
	switch {
	case r == nil:
		return StateLocalOnly
	case r.Blackout():
		return StateBlackout
	}
	return StateRelay
}

// SetSession moves the node to another session of this process: the old one
// ends its broadcast and its card, the new one broadcasts afresh.
func (n *Node) SetSession(sessionID, profile string) {
	if !uuidShape.MatchString(sessionID) {
		return
	}
	n.mu.Lock()
	old := n.self
	if old.SessionID == sessionID && old.Profile == sanitize.Ident(profile, maxIdentBytes) {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	if old.SessionID != sessionID {
		n.end(old)
	}
	n.mu.Lock()
	n.self = n.card(sessionID, profile)
	n.last = nil
	n.mu.Unlock()
	n.bc.Reset()
	_ = n.putCard()
}

func (n *Node) putCard() error {
	n.mu.Lock()
	rec := CardRecord{Card: n.self, PID: os.Getpid(), Last: n.last}
	n.mu.Unlock()
	return n.bus.PutCard(rec)
}

// Run reads the bus, keeps the card beating and broadcasts the session's work
// until ctx ends.
func (n *Node) Run(ctx context.Context) {
	t := time.NewTicker(n.opts.PollEvery)
	defer t.Stop()
	var ticks int
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ticks++
		n.pollGit()
		n.Step(ctx, ticks%5 == 0, ticks%15 == 0)
	}
}

// Step runs one pass of Run: deliver what the bus holds, refresh the local
// peers and beat the card when asked, and broadcast work that changed.
func (n *Node) Step(ctx context.Context, refreshPeers, beat bool) {
	envs, _ := n.bus.Poll()
	for _, env := range envs {
		n.receive(ctx, env)
	}
	if refreshPeers {
		n.mailbox.SetLocal(n.bus.Cards(), n.Self().ID, n.now())
	}
	if beat {
		_ = n.putCard()
	}
	if w, ok := n.bc.Due(n.now()); ok {
		_, _ = n.send(Envelope{Kind: KindBroadcast, Work: w})
	}
}

func (n *Node) pollGit() {
	if n.opts.Git == nil {
		return
	}
	raw := n.opts.Git()
	if len(raw) == 0 || string(raw) == n.lastGit {
		return
	}
	n.lastGit = string(raw)
	var info gitsync.Info
	if json.Unmarshal(raw, &info) == nil {
		n.bc.SetGit(&info)
	}
}

// receive files one envelope from the bus: it is observed, filtered, audited
// and, on a match, handed to the session.
func (n *Node) receive(ctx context.Context, env Envelope) {
	self := n.Self()
	if env.Agent.ID == self.ID {
		return
	}
	local := env.Agent.HostID == n.opts.HostID
	n.mailbox.Observe(env, n.now(), local)
	work := n.bc.Work()
	if !Match(env, Self{ID: self.ID, Repo: work.Repo}, n.opts.Filter) {
		n.audit.Record(audit.A2AReceived, OutcomeFiltered, self.SessionID, env, false)
		return
	}
	ev := Event{Env: env}
	switch env.Kind {
	case KindBroadcast:
		ev.Overlap = Overlap(env.Work.Files, work.Files)
		ev.Tell = n.mailbox.ShouldTell(env, ev.Overlap)
		if !ev.Tell {
			return
		}
	case KindReply:
		if q, ok := n.mailbox.Message(env.ReferenceMessageID); ok && q.Agent.ID == self.ID {
			ev.Question = &q
		}
	case KindBroadcastEnd:
		// A peer that left: recorded, never told to the model.
	}
	n.audit.Record(audit.A2AReceived, OutcomeMatched, self.SessionID, env, true)
	select {
	case n.events <- ev:
	case <-ctx.Done():
	}
}

func (n *Node) emit(ev Event) {
	select {
	case n.events <- ev:
	default:
	}
}

// send stamps, cleans, appends and relays an envelope from this session.
func (n *Node) send(env Envelope) (Envelope, error) {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return Envelope{}, errors.New("a2a: closed")
	}
	self, relay := n.self, n.relay
	n.mu.Unlock()
	now := n.now()
	env.MessageID = NewMessageID(now)
	env.TS = now.UnixMilli()
	env.Agent = self
	if env.ContextID == "" {
		env.ContextID = env.MessageID
	}
	if env.Kind != KindBroadcast {
		w := n.bc.Work()
		w.Files = nil
		if env.Kind == KindBroadcastEnd {
			w = n.bc.Work()
		}
		env.Work = w
	}
	clean, err := env.Clean()
	if err != nil {
		return Envelope{}, err
	}
	if _, err := n.bus.Append(clean); err != nil {
		return Envelope{}, err
	}
	n.mailbox.Record(clean)
	outcome := OutcomeLocal
	if relay != nil && relay.Outbound(clean) {
		outcome = OutcomeRelayed
	}
	n.audit.Record(audit.A2ASent, outcome, self.SessionID, clean, true)
	if clean.Kind == KindBroadcast {
		n.mu.Lock()
		n.last = &clean
		n.mu.Unlock()
		_ = n.putCard()
	}
	n.emit(Event{Env: clean, Out: true})
	return clean, nil
}

// SendMessage sends a direct message to a live peer, by its Agent Card id
// or display id. It never waits for the reply.
func (n *Node) SendMessage(to, text string) (Envelope, error) {
	self := n.Self()
	if to == self.ID || strings.TrimPrefix(to, "@") == strings.TrimPrefix(self.Display(), "@") {
		return Envelope{}, ErrSelf
	}
	peer, ok := n.mailbox.Peer(to)
	if !ok {
		// A same-host card may not have broadcast yet.
		n.mailbox.SetLocal(n.bus.Cards(), self.ID, n.now())
		if peer, ok = n.mailbox.Peer(to); !ok {
			return Envelope{}, ErrNoPeer
		}
	}
	if !hasCap(peer.Card, CapMessage) {
		return Envelope{}, fmt.Errorf("%s does not take direct messages", peer.Card.Display())
	}
	if strings.TrimSpace(text) == "" {
		return Envelope{}, errors.New("the message has no text")
	}
	return n.send(Envelope{Kind: KindMessage, To: peer.Card.ID, Parts: []Part{{Text: text}}})
}

func hasCap(c AgentCard, cp string) bool {
	for _, x := range c.Capabilities {
		if x == cp {
			return true
		}
	}
	return false
}

// Reply answers a message this session received.
func (n *Node) Reply(question Envelope, text string) (Envelope, error) {
	if question.Kind != KindMessage {
		return Envelope{}, errors.New("a2a: only a message is answered")
	}
	ctxID := question.ContextID
	if ctxID == "" {
		ctxID = question.MessageID
	}
	return n.send(Envelope{Kind: KindReply, To: question.Agent.ID, ReferenceMessageID: question.MessageID,
		ContextID: ctxID, Parts: []Part{{Text: text}}})
}

// Ack records the session's decision on a broadcast or message it received.
// A decision other than ignore is also sent to the sender as an ack. The
// returned envelope is the ack (sent or not) for the transcript.
func (n *Node) Ack(messageID, decision, reason string) (Envelope, bool, error) {
	if !ValidDecision(decision) {
		return Envelope{}, false, fmt.Errorf("decision must be one of %s", strings.Join(Decisions(), ", "))
	}
	ref, ok := n.mailbox.Message(messageID)
	self := n.Self()
	if !ok || ref.Agent.ID == self.ID {
		return Envelope{}, false, ErrNoMessage
	}
	reason = sanitize.Line(reason, MaxReasonRunes)
	ctxID := ref.ContextID
	if ctxID == "" {
		ctxID = ref.MessageID
	}
	env := Envelope{Kind: KindAck, To: ref.Agent.ID, ReferenceMessageID: ref.MessageID, ContextID: ctxID,
		Decision: decision}
	if reason != "" {
		env.Parts = []Part{{Text: reason}}
	}
	if decision == DecisionIgnore {
		now := n.now()
		env.MessageID, env.TS, env.Agent = NewMessageID(now), now.UnixMilli(), self
		clean, err := env.Clean()
		if err != nil {
			return Envelope{}, false, err
		}
		n.audit.Record(audit.A2ASent, OutcomeLocal, self.SessionID, clean, false)
		n.emit(Event{Env: clean, Out: true})
		return clean, false, nil
	}
	sent, err := n.send(env)
	return sent, err == nil, err
}

// end broadcasts the end of card's work and removes its card.
func (n *Node) end(card AgentCard) {
	n.mu.Lock()
	cur := n.self
	n.self = card
	n.mu.Unlock()
	_, _ = n.send(Envelope{Kind: KindBroadcastEnd})
	n.bus.RemoveCard(card.SessionID)
	n.mu.Lock()
	n.self = cur
	n.mu.Unlock()
}

// Close ends the session's broadcast and removes its card.
func (n *Node) Close() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return
	}
	self := n.self
	n.mu.Unlock()
	n.end(self)
	n.mu.Lock()
	n.closed = true
	n.mu.Unlock()
}
