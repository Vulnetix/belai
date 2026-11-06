package agent

import (
	"context"
	"fmt"
	"sync"

	"github.com/vulnetix/belai/internal/session"
)

// compactBreaker stops a session from paying for a summary that keeps failing.
// A summary is one long classifier call plus an admission call; when the
// classifier cannot produce a valid, admitted one (a transport error, a reply
// missing its sections, a refusal), retrying at every pass boundary only adds
// latency and cost while the context stays over the limit. After
// compaction.breaker_failures failures in a row the summary step is skipped for
// compaction.breaker_cooldown_passes pass boundaries. Pruning never needs
// admission and keeps running, and a forced compaction after the provider has
// reported an overflow ignores the breaker.
//
// It is harness state: no model is asked whether to trip it, and nothing a
// model wrote reaches it.
type compactBreaker struct {
	mu       sync.Mutex
	fails    int
	cooldown int // pass boundaries the summary step still skips
	tokens   int // last measured estimate, for /compact status
	window   int
}

// boundary marks one pass boundary and reports whether the summary step is
// paused at it. It consumes one boundary of the cooldown.
func (b *compactBreaker) boundary() (paused bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cooldown > 0 {
		b.cooldown--
		return true
	}
	return false
}

// record folds in the outcome of one summary attempt and reports whether the
// breaker just opened. A failure at or past the limit opens it, including the
// first failure after a cooldown (half open), so a classifier that is still
// down costs one attempt per cooldown rather than a full run of them.
func (b *compactBreaker) record(ok bool, failures, cooldownPasses int) (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ok {
		b.fails, b.cooldown = 0, 0
		return false
	}
	b.fails++
	if b.fails >= failures {
		b.cooldown = cooldownPasses
		return true
	}
	return false
}

// facts records the estimate and window measured at a boundary.
func (b *compactBreaker) facts(tokens, window int) {
	b.mu.Lock()
	b.tokens, b.window = tokens, window
	b.mu.Unlock()
}

func (b *compactBreaker) state() session.CompactionState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return session.CompactionState{Fails: b.fails, CooldownLeft: b.cooldown, Tokens: b.tokens, Window: b.window}.Clamp()
}

func (b *compactBreaker) restore(c session.CompactionState) {
	c = c.Clamp()
	b.mu.Lock()
	b.fails, b.cooldown, b.tokens, b.window = c.Fails, c.CooldownLeft, c.Tokens, c.Window
	b.mu.Unlock()
}

// CompactionState is the circuit breaker's state, for the UI to persist and
// show.
func (s *Session) CompactionState() session.CompactionState { return s.breaker.state() }

// RestoreCompactionState puts back a state read from a session file. The value
// is clamped, so a damaged entry cannot hold the summary step off for long.
func (s *Session) RestoreCompactionState(c session.CompactionState) { s.breaker.restore(c) }

// noteSummaryOutcome records one summary attempt and, when the state changed
// in a way a person or a resumed session needs, tells the UI. An opening gets
// one warning and one trace record.
func (s *Session) noteSummaryOutcome(ok bool, window, tokens int) {
	before := s.breaker.state()
	s.breaker.facts(tokens, window)
	opened := s.breaker.record(ok, s.settings.CompactBreakerFailures(), s.settings.CompactBreakerCooldownPasses())
	after := s.breaker.state()
	if opened {
		cool := s.settings.CompactBreakerCooldownPasses()
		s.traceRecord("compaction_breaker", "open", "", fmt.Sprintf("fails=%d cooldown=%d", after.Fails, cool), 0)
		if s.emit != nil {
			s.emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("compaction paused for %d passes after %d failed summaries; pruning continues", cool, after.Fails)})
		}
	}
	if s.emit != nil && (after.Fails != before.Fails || after.CooldownLeft != before.CooldownLeft) {
		st := after
		s.emit(Event{Kind: EventCompactionStateKind, Compaction: &st})
	}
}

// compactionBoundary marks a pass boundary on the breaker and reports whether
// the summary step is paused at it. The cooldown shrinks by one, which the UI
// is told so a saved session records the remaining pause.
func (s *Session) compactionBoundary() (paused bool) {
	before := s.breaker.state()
	paused = s.breaker.boundary()
	if after := s.breaker.state(); s.emit != nil && after.CooldownLeft != before.CooldownLeft {
		s.emit(Event{Kind: EventCompactionStateKind, Compaction: &after})
	}
	return paused
}

// summaryFailed counts a failed summary unless the turn itself was cancelled,
// which says nothing about the classifier.
func (s *Session) summaryFailed(ctx context.Context, window, tokens int) {
	if ctx.Err() != nil {
		return
	}
	s.noteSummaryOutcome(false, window, tokens)
}
