package posture

import "sync/atomic"

// Live carries the operator's runtime switches — the effective posture policy
// (what the guardrails switch leaves of it) and the permission-ask gate —
// behind atomics, so a session reads the current value at each gate rather
// than a snapshot taken when it was built. A toggle pressed mid-turn lands on
// the next gate check instead of the next prompt.
//
// The zero value is usable: Policy falls back to Defaults and AskDisabled
// reports false. The atomic pointer is the whole synchronisation: Set replaces
// the map with a fresh copy, so a reader that loaded the old pointer keeps a
// stable, immutable snapshot for the duration of its call and never observes a
// map being written. The ask gate is a plain atomic boolean.
type Live struct {
	pol    atomic.Pointer[Policy]
	askOff atomic.Bool
}

// NewLive returns a Live seeded with p and askDisabled. p is copied, so a
// caller mutating its own Policy afterwards cannot reach a reader through the
// Live.
func NewLive(p Policy, askDisabled bool) *Live {
	l := &Live{}
	l.Set(p, askDisabled)
	return l
}

// Fixed returns a Live that never changes, for callers with nothing to toggle
// (the one-shot CLI and hand-built test sessions). Ask reporting is off.
func Fixed(p Policy) *Live {
	return NewLive(p, false)
}

// Set atomically replaces the carried policy (a copy of p) and the ask-off
// flag. It is safe to call concurrently with every read method.
func (l *Live) Set(p Policy, askDisabled bool) {
	if l == nil {
		return
	}
	l.pol.Store(clonePolicy(p))
	l.askOff.Store(askDisabled)
}

// Policy returns a copy of the current policy. A nil receiver returns
// Defaults.
func (l *Live) Policy() Policy {
	if l == nil {
		return Defaults()
	}
	if p := l.pol.Load(); p != nil {
		out := clonePolicy(*p)
		return *out
	}
	return Defaults()
}

// Level returns the current posture level for g. A nil receiver returns
// DefaultLevel(g).
func (l *Live) Level(g Gate) Level {
	if l == nil {
		return DefaultLevel(g)
	}
	if p := l.pol.Load(); p != nil {
		return p.Level(g)
	}
	return DefaultLevel(g)
}

// AskDisabled reports whether the permission-ask gate is off. A nil receiver
// reports false.
func (l *Live) AskDisabled() bool {
	if l == nil {
		return false
	}
	return l.askOff.Load()
}

// clonePolicy copies a policy so no reader can ever observe a map being
// written. A nil policy becomes Defaults.
func clonePolicy(p Policy) *Policy {
	out := make(Policy, len(p))
	for g, lv := range p {
		out[g] = lv
	}
	return &out
}
