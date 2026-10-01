package fleet

import (
	"context"
	"sync"

	"github.com/vulnetix/belai/internal/sessionsync"
)

// maxWebQueue bounds the messages held for a worker between turns. It matches
// the session's own steering buffer, so every held message fits when the next
// turn starts.
const maxWebQueue = 8

// Steerer is the part of an agent session a web message needs: queueing text
// that the session admits at its next pass boundary, exactly as a typed
// steering line.
type Steerer interface {
	Steer(text string) bool
}

// webInbox hands the website's messages to a worker. A worker takes no
// prompts of its own: the only text that reaches it is what the user typed on
// the website into the worker's session (a crew message is one such prompt per
// worker). Every one is cleaned like any web prompt and enters the running
// turn through Steer, so the role manager admits it like typed steering. A
// message that arrives between turns is held and steered into the next turn.
type webInbox struct {
	mirror interface {
		Prompts() <-chan sessionsync.RemotePrompt
		Ack(id, status, reason, entryID string)
		Nudge()
	}
	facts func() map[string]any

	mu    sync.Mutex
	turn  *webTurn
	queue []sessionsync.RemotePrompt
}

// webTurn is one running turn: where a message goes and where it is recorded.
type webTurn struct {
	steer Steerer
	tr    *transcript
}

// run consumes web prompts until ctx ends.
func (b *webInbox) run(ctx context.Context) {
	ch := b.mirror.Prompts()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			b.deliver(p)
		}
	}
}

func (b *webInbox) deliver(p sessionsync.RemotePrompt) {
	text := sessionsync.CleanPrompt(p.Content)
	if text == "" {
		b.mirror.Ack(p.ID, sessionsync.AckRefused, "the message was empty after cleaning", "")
		return
	}
	p.Content = text
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.turn != nil {
		b.steerLocked(p)
		return
	}
	if len(b.queue) >= maxWebQueue {
		b.mirror.Ack(p.ID, sessionsync.AckRefused, "this agent already holds as many waiting messages as it takes", "")
		return
	}
	b.queue = append(b.queue, p)
	b.mirror.Ack(p.ID, sessionsync.AckQueued, "", "")
}

// steerLocked sends p into the running turn and records it. b.mu is held.
func (b *webInbox) steerLocked(p sessionsync.RemotePrompt) {
	if !b.turn.steer.Steer(p.Content) {
		b.mirror.Ack(p.ID, sessionsync.AckRefused, "the agent's steering queue is full; send it again in a moment", "")
		return
	}
	meta := map[string]any{"source": "web", "remote_prompt_id": p.ID, "steering": true}
	if b.facts != nil {
		meta["profile_facts"] = b.facts()
	}
	id := b.turn.tr.log.User(p.Content, meta)
	b.mirror.Ack(p.ID, sessionsync.AckAccepted, "", id)
	b.mirror.Nudge()
}

// begin opens a turn: messages held since the last one are steered into it.
func (b *webInbox) begin(s Steerer, tr *transcript) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turn = &webTurn{steer: s, tr: tr}
	held := b.queue
	b.queue = nil
	for _, p := range held {
		b.steerLocked(p)
	}
}

// end closes the turn. Later messages are held for the next one.
func (b *webInbox) end() {
	b.mu.Lock()
	b.turn = nil
	b.mu.Unlock()
}

// profileFacts are the customisations this worker runs under, stamped on the
// entries it writes so the website can show them beside each line of a shared
// thread. They are facts the harness already holds and are safe to sync: names,
// presentation, a hash that pins the behavioural definition, the model facts
// the transcript already carries and a tool count. Never the system prompt, the
// persona text or the tool list (docs/remote-control.md: the catalogue is
// harness facts only).
func (w *Worker) profileFacts() map[string]any {
	p := w.Profile
	f := map[string]any{
		"profile":      p.Name,
		"profile_hash": ProfileHash(p),
		"tool_count":   len(p.Tools),
	}
	if w.Record.Crew != "" {
		f["crew"] = w.Record.Crew
	}
	if p.DisplayName != "" {
		f["display_name"] = p.DisplayName
	}
	if len(p.Palette) > 0 {
		f["palette"] = p.Palette
	}
	if p.AvatarID != "" {
		f["avatar_id"] = p.AvatarID
	}
	if w.Cfg.Model != "" {
		f["model"] = w.Cfg.Model
	}
	if w.Cfg.Provider != "" {
		f["provider"] = w.Cfg.Provider
	}
	if p.Effort != "" {
		f["effort"] = p.Effort
	}
	return f
}
