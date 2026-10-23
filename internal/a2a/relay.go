package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Relay state files in the A2A directory.
const (
	blackoutFile = "blackout.json"
	cursorFile   = "relay.json"
)

// maxRelayAge is how old a relayed envelope may be and still be delivered: a
// host that was offline does not replay an hour of other hosts' broadcasts.
const maxRelayAge = time.Hour

// relayBatch bounds one upload.
const relayBatch = 50

// blackoutState is the host's last known A2A blackout, shared by every
// process on the host: a TUI's syncer and an rc daemon's heartbeat both learn
// it, and every relay on the host obeys it.
type blackoutState struct {
	On bool  `json:"on"`
	At int64 `json:"at"`
}

// SetBlackout records the organisation's A2A blackout for every process on
// the host. It reports whether the state changed.
func SetBlackout(dir string, on bool, now time.Time) (bool, error) {
	prev := Blackout(dir)
	if prev == on {
		return false, nil
	}
	data, _ := json.Marshal(blackoutState{On: on, At: now.UnixMilli()})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	tmp := filepath.Join(dir, ".blackout.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, filepath.Join(dir, blackoutFile))
}

// Blackout reports the host's last known A2A blackout. No state is off.
func Blackout(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, blackoutFile))
	if err != nil {
		return false
	}
	var st blackoutState
	return json.Unmarshal(data, &st) == nil && st.On
}

// RelayClient is the part of sessionsync.Client the relay uses.
type RelayClient interface {
	PostA2A(ctx context.Context, hostID string, envelopes []json.RawMessage) (sessionsync.A2APostResult, error)
}

// Relay forwards this process's outbound envelopes to Vulnetix and writes the
// organisation's inbound ones into the local bus, de-duplicated by messageId.
// It runs only while sync is on (a login), one per host process. During an
// A2A blackout it relays nothing in either direction: the local bus goes on,
// and the local audit records each envelope it held back as withheld.
type Relay struct {
	client RelayClient
	hostID string
	bus    *Bus
	audit  *Audit
	dir    string
	now    func() time.Time
	// OnBlackout, when set, is called when the blackout switches.
	OnBlackout func(on bool)

	mu       sync.Mutex
	queue    []Envelope
	cursor   int64
	wake     chan struct{}
	blackout bool
	session  func() string
	lastErr  error
	up       bool
}

// NewRelay builds a relay for hostID over bus. session names this process's
// live session for the audit.
func NewRelay(client RelayClient, hostID string, bus *Bus, a *Audit, session func() string) *Relay {
	r := &Relay{client: client, hostID: hostID, bus: bus, audit: a, dir: bus.Dir(), now: time.Now,
		wake: make(chan struct{}, 1), session: session}
	r.blackout = Blackout(r.dir)
	r.cursor = r.loadCursor()
	return r
}

func (r *Relay) sessionID() string {
	if r.session == nil {
		return ""
	}
	return r.session()
}

// Blackout reports whether the relay is stopped by an A2A blackout.
func (r *Relay) Blackout() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.blackout
}

// Up reports whether the last upload succeeded.
func (r *Relay) Up() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.up
}

// SetBlackout applies the organisation's A2A blackout, from a session's
// controls or the rc heartbeat, records it for the host and audits a change.
func (r *Relay) SetBlackout(on bool) {
	r.mu.Lock()
	changed := r.blackout != on
	r.blackout = on
	var held []Envelope
	if on {
		held, r.queue = r.queue, nil
	}
	cb := r.OnBlackout
	r.mu.Unlock()
	_, _ = SetBlackout(r.dir, on, r.now())
	for _, env := range held {
		r.audit.Record(audit.A2AWithheld, OutcomeWithheld, r.sessionID(), env, true)
	}
	if changed {
		r.audit.RecordBlackout(on, r.sessionID())
		if cb != nil {
			cb(on)
		}
	}
}

// refreshBlackout picks up a blackout another process on the host learnt.
func (r *Relay) refreshBlackout() {
	if on := Blackout(r.dir); on != r.Blackout() {
		r.SetBlackout(on)
	}
}

// Outbound queues an envelope this process sent for upload. It reports false
// when the blackout withheld it (and audits that).
func (r *Relay) Outbound(env Envelope) bool {
	r.mu.Lock()
	if r.blackout {
		r.mu.Unlock()
		r.audit.Record(audit.A2AWithheld, OutcomeWithheld, r.sessionID(), env, true)
		return false
	}
	r.queue = append(r.queue, env)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return true
}

// After is the cursor for the inbox poll: the highest server seq delivered,
// or -1 (ask for none) during a blackout.
func (r *Relay) After() int64 {
	r.refreshBlackout()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.blackout {
		return -1
	}
	return r.cursor
}

// Deliver writes the organisation's envelopes into the local bus. An envelope
// that does not clean, comes from this host (the bus already carried it), or
// is older than maxRelayAge is skipped; the cursor still moves past it.
func (r *Relay) Deliver(items []sessionsync.A2AItem) {
	if r.Blackout() {
		return
	}
	now := r.now()
	maxSeq := r.cursorValue()
	for _, it := range items {
		if it.Seq > maxSeq {
			maxSeq = it.Seq
		}
		var env Envelope
		if json.Unmarshal(it.Envelope, &env) != nil {
			continue
		}
		clean, err := env.Clean()
		if err != nil || clean.Agent.HostID == r.hostID {
			continue
		}
		if clean.TS > 0 && now.Sub(time.UnixMilli(clean.TS)) > maxRelayAge {
			continue
		}
		_, _ = r.bus.Append(clean)
	}
	r.mu.Lock()
	if maxSeq > r.cursor {
		r.cursor = maxSeq
	}
	cur := r.cursor
	r.mu.Unlock()
	r.saveCursor(cur)
}

func (r *Relay) cursorValue() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cursor
}

// Run uploads queued envelopes until ctx ends.
func (r *Relay) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	var backoff time.Duration
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-t.C:
			r.refreshBlackout()
		}
		if backoff > 0 {
			if !sleepCtx(ctx, backoff) {
				return
			}
		}
		if err := r.flush(ctx); err != nil {
			backoff = min(max(2*backoff, 2*time.Second), time.Minute)
		} else {
			backoff = 0
		}
	}
}

// flush uploads one batch of the queue.
func (r *Relay) flush(ctx context.Context) error {
	r.mu.Lock()
	if r.blackout || len(r.queue) == 0 {
		r.mu.Unlock()
		return nil
	}
	n := min(len(r.queue), relayBatch)
	batch := append([]Envelope(nil), r.queue[:n]...)
	r.mu.Unlock()
	raw := make([]json.RawMessage, 0, len(batch))
	for _, env := range batch {
		b, err := json.Marshal(env)
		if err == nil {
			raw = append(raw, b)
		}
	}
	res, err := r.client.PostA2A(ctx, r.hostID, raw)
	r.mu.Lock()
	r.lastErr, r.up = err, err == nil
	if err == nil {
		r.queue = r.queue[min(n, len(r.queue)):]
	}
	r.mu.Unlock()
	if err != nil {
		if errors.Is(err, sessionsync.ErrUnauthorized) {
			// A lapsed login: drop rather than hold a growing queue.
			r.mu.Lock()
			r.queue = nil
			r.mu.Unlock()
		}
		return err
	}
	if res.Blackout {
		for _, env := range batch {
			r.audit.Record(audit.A2AWithheld, OutcomeWithheld, r.sessionID(), env, true)
		}
		r.SetBlackout(true)
	}
	return nil
}

func (r *Relay) loadCursor() int64 {
	data, err := os.ReadFile(filepath.Join(r.dir, cursorFile))
	if err != nil {
		return 0
	}
	var st struct {
		After int64 `json:"after"`
	}
	if json.Unmarshal(data, &st) != nil || st.After < 0 {
		return 0
	}
	return st.After
}

func (r *Relay) saveCursor(after int64) {
	// The cursor is host-wide: keep the highest any process reached.
	if after <= r.loadCursor() {
		return
	}
	data, _ := json.Marshal(map[string]int64{"after": after})
	tmp, err := os.CreateTemp(r.dir, ".relay-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	_ = os.Rename(tmp.Name(), filepath.Join(r.dir, cursorFile))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
