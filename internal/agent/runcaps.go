package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/transcript"
)

// Run caps (docs/run-caps.md). The meter (internal/budget) measures; this file
// acts on its verdicts at a model round boundary: tell the model to finalise at
// finalize_at, give a run that was never warned a short reserve, then ask the
// user whether to extend. The model is never asked anything and never sees a
// ceiling, and only an answer on the user's own channel extends.

// RunCapAskName is the AskRequest.Name of a run-cap ask.
const RunCapAskName = "RunCap"

// RunCapChoice is the user's answer to a reached run cap.
type RunCapChoice string

// The answers.
const (
	// RunCapExtend raises the ceilings by half of their original values.
	RunCapExtend RunCapChoice = "extend"
	// RunCapStop ends the run and forgets the counters, so a resume starts from
	// zero.
	RunCapStop RunCapChoice = "stop"
	// RunCapStopKeep ends the run and keeps the counters, so a resume carries
	// the spend.
	RunCapStopKeep RunCapChoice = "stop_keep"
)

// Valid reports whether c is one of the three answers.
func (c RunCapChoice) Valid() bool {
	return c == RunCapExtend || c == RunCapStop || c == RunCapStopKeep
}

// RunCapAsk is a reached run cap as a surface renders it: an enum and
// integers.
type RunCapAsk struct {
	// Reason is the budget.Reason word that was reached: cost, tokens or wall.
	Reason string
	// Used and Limit are in the reason's unit: millionths of a dollar for cost,
	// tokens for tokens, milliseconds for wall.
	Used, Limit int64
	// ExtendBy is what one extension adds, in the same unit.
	ExtendBy int64
	// ExtendsLeft is how many extensions remain.
	ExtendsLeft int
}

// RunCapAmount renders an amount of a reason's unit as text.
func RunCapAmount(reason string, v int64) string {
	switch reason {
	case budget.ReasonCost:
		return fmt.Sprintf("$%.2f", float64(v)/1e6)
	case budget.ReasonWall:
		return time.Duration(v * int64(time.Millisecond)).Round(time.Second).String()
	}
	return fmt.Sprintf("%d tokens", v)
}

// Text is the ask's question, built from the enum and integers.
func (a RunCapAsk) Text() string {
	return fmt.Sprintf("Run cap reached: %s %s of %s. Extend by %s (%d left), stop now, or stop and keep the counters?",
		a.Reason, RunCapAmount(a.Reason, a.Used), RunCapAmount(a.Reason, a.Limit), RunCapAmount(a.Reason, a.ExtendBy), a.ExtendsLeft)
}

// errRunCapStop ends a pass on a reached run cap. endOnRunCap turns it into an
// ordinary stop at the end of the turn, so no layer between shows it as a
// failure.
var errRunCapStop = errors.New("run cap reached")

// RunCaps is the session's meter, nil when no cap is set.
func (s *Session) RunCaps() *budget.Meter { return s.runCaps }

// noteSpend adds one main-model call to the meter, at the price of the model
// that made it (a subagent may run a different one). A model with no known
// price leaves the cost cap out for that call, said once.
func (s *Session) noteSpend(u *transcript.Usage, emit func(Event)) {
	m := s.runCaps
	if m == nil || u == nil {
		return
	}
	var price *budget.Price
	if p, ok := budget.PriceFor(s.cfg.Provider, s.cfg.Model); ok {
		price = &p
	}
	first := price == nil && !m.Unpriced()
	m.Add(*u, price)
	if first && !s.exploreSubagent {
		emit(Event{Kind: EventWarningKind, Warning: fmt.Sprintf("run caps: no price is known for %s/%s, so the cost cap is off for it; the token and wall caps still count", s.cfg.Provider, s.cfg.Model)})
	}
}

// capAskable reports whether anyone can be asked to extend: an interactive
// session whose permission asks are on. Ask off, a headless run, a subagent and
// a background agent stop instead.
func (s *Session) capAskable() bool {
	return s.allowAsk && !s.exploreSubagent && !s.live.AskDisabled()
}

// capBoundary runs at the top of each model round. It returns the finalise
// directive to add to the turns, and whether the run must stop. A stop is a
// reached cap that was not extended; the pass returns errRunCapStop for it.
func (s *Session) capBoundary(ctx context.Context, emit func(Event)) (directive []run.Turn, stop bool) {
	m := s.runCaps
	if m == nil {
		return nil, false
	}
	v := m.Check()
	switch v.Level {
	case budget.OK:
		return nil, false
	case budget.Finalise:
		if s.exploreSubagent {
			return nil, false
		}
		return s.capFinalise(v.Reason, "nearly reached", emit), false
	}
	// A reached pass ceiling is resilience.max_passes, which stops the loop
	// itself; the meter only warned about it.
	if v.Reason == budget.ReasonPasses {
		return nil, false
	}
	if s.exploreSubagent {
		return nil, true
	}
	// A run that went from below the warning to the cap in one round was never
	// told to finalise: tell it now and give it two rounds.
	if !v.Warned && !s.capSeen {
		// The round that carries the directive is the first of the reserve.
		s.capReserve = FinalizeReservePasses - 1
		return s.capFinalise(v.Reason, "reached", emit), false
	}
	if s.capReserve > 0 {
		s.capReserve--
		return nil, false
	}
	return nil, s.capDecide(ctx, v.Reason, emit) != RunCapExtend
}

// capFinalise returns the finalise directive for reason, once a turn, and
// records the event.
func (s *Session) capFinalise(reason, what string, emit func(Event)) []run.Turn {
	f, err := NewRunCapFact(reason)
	if err != nil {
		return nil
	}
	s.capSeen = true
	emit(Event{Kind: EventWarningKind, Warning: "run caps: the " + reason + " cap is " + what + "; told the model to finalise"})
	s.emitCapAudit("finalise", reason)
	return directiveTurns(f.Text())
}

// capDecide settles a reached cap: it asks the user when anyone can be asked and
// extensions remain, and applies the answer. Anything unanswered, cancelled,
// refused or not an extend stops the run. It returns the choice that took
// effect.
func (s *Session) capDecide(ctx context.Context, reason string, emit func(Event)) RunCapChoice {
	m := s.runCaps
	choice := RunCapStop
	if left := m.ExtendsLeft(); left > 0 && s.capAskable() {
		c := m.Caps()
		snap := m.Snapshot()
		ask := &RunCapAsk{Reason: reason, ExtendsLeft: left}
		switch reason {
		case budget.ReasonCost:
			ask.Used, ask.Limit = snap.CostMicroUSD, c.CostMicroUSD
		case budget.ReasonTokens:
			ask.Used, ask.Limit = int64(snap.Tokens), int64(c.Tokens)
		default:
			ask.Used, ask.Limit = snap.WallMS, c.Wall.Milliseconds()
		}
		ask.ExtendBy = m.ExtendBy(reason)
		reply := make(chan PermissionAskReply, 1)
		emit(Event{Kind: EventPermissionAskKind, Ask: &AskRequest{Name: RunCapAskName, Subject: reason, RunCap: ask}, AskReply: reply})
		select {
		case r := <-reply:
			if r.Choice.Valid() {
				choice = r.Choice
			}
		case <-ctx.Done():
		}
	}
	if choice == RunCapExtend && !m.Extend() {
		choice = RunCapStop
	}
	switch choice {
	case RunCapExtend:
		s.capSeen, s.capReserve = false, 0
		emit(Event{Kind: EventWarningKind, Warning: "run caps: extended; the " + reason + " cap was raised by half of its original value"})
		s.emitCapAudit("extended", reason)
		_ = m.Save()
	case RunCapStopKeep:
		s.emitCapAudit("stopped_keep", reason)
		_ = m.Save()
	default:
		m.Forget()
		s.emitCapAudit("stopped", reason)
	}
	return choice
}

func (s *Session) emitCapAudit(outcome, reason string) {
	data := map[string]string{"reason": reason}
	if s.runCaps != nil {
		data["passes"] = fmt.Sprint(s.runCaps.Snapshot().Passes)
	}
	audit.Emit(audit.Fact{Kind: audit.RunCap, ActorKind: audit.ActorHarness, SessionID: s.sessionID, Outcome: outcome, Data: data})
}

// endOnRunCap turns errRunCapStop into the end of a turn: the partial reply, no
// error, and run.StopRunCap as the stop reason.
func (s *Session) endOnRunCap(res run.Result, err error) (run.Result, error) {
	if !errors.Is(err, errRunCapStop) {
		return res, err
	}
	s.capStopped = true
	s.turnStop = run.StopRunCap
	if res.Reply == "" {
		res.Reply = s.capLast
	}
	return res, nil
}

// beginRunCapTurn and its closer bracket one outermost turn so the wall cap
// counts time spent running turns and the counters are saved when a turn ends.
func (s *Session) beginRunCapTurn() func() {
	m := s.runCaps
	if m == nil || s.exploreSubagent {
		return func() {}
	}
	m.BeginTurn()
	return func() {
		m.EndTurn()
		_ = m.Save()
	}
}
