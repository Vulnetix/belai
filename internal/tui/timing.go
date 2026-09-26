package tui

import (
	"time"

	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/tui/components"
)

// Transcript timing. The session file used to be stamped when a turn's rows
// were flushed, which is at turn end, so every row of a 900-second turn
// carried the same millisecond and nothing said where the time went. Rows are
// now stamped when they come into being (from the emitting event's own
// timestamp where there is one) and the work they report carries its
// duration.

// stampMessages gives every row that has no CreatedAt yet the time at. New
// rows are only ever appended, so the scan stops at the first stamped row
// from the end. A new assistant bubble adopts any provider-call durations that
// arrived before it existed.
func (a *App) stampMessages(at time.Time) {
	if at.IsZero() {
		at = time.Now()
	}
	for i := len(a.messages) - 1; i >= 0; i-- {
		if !a.messages[i].CreatedAt.IsZero() {
			return
		}
		a.messages[i].CreatedAt = at
		if a.messages[i].Role == "assistant" && len(a.pendingModelMS) > 0 {
			a.messages[i].ModelCallsMS = append(a.messages[i].ModelCallsMS, a.pendingModelMS...)
			a.pendingModelMS = nil
		}
	}
}

// noteModelCall attaches one provider call's duration to this turn's
// assistant bubble: the latest assistant row after the latest user row. A
// call that finished before the turn had a bubble is held until one appears.
func (a *App) noteModelCall(d time.Duration) {
	ms := d.Milliseconds()
	for i := len(a.messages) - 1; i >= 0; i-- {
		switch a.messages[i].Role {
		case "assistant":
			if a.messages[i].SubagentID == "" {
				a.messages[i].ModelCallsMS = append(a.messages[i].ModelCallsMS, ms)
				return
			}
		case "user":
			a.pendingModelMS = append(a.pendingModelMS, ms)
			return
		}
	}
	a.pendingModelMS = append(a.pendingModelMS, ms)
}

// entryTime is the session timestamp for a row: when it was created, falling
// back to now for a row that predates stamping.
func entryTime(m components.Message) int64 {
	if m.CreatedAt.IsZero() {
		return time.Now().UnixMilli()
	}
	return m.CreatedAt.UnixMilli()
}

// timedEntry stamps e with the row's creation time and adds its measured
// durations to the entry's meta.
func timedEntry(m components.Message, e session.Entry) session.Entry {
	e.Timestamp = entryTime(m)
	if m.DurationMS > 0 {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		e.Meta["duration_ms"] = m.DurationMS
	}
	if len(m.ModelCallsMS) > 0 {
		if e.Meta == nil {
			e.Meta = map[string]any{}
		}
		var total int64
		for _, ms := range m.ModelCallsMS {
			total += ms
		}
		e.Meta["duration_ms"] = total
		e.Meta["model_calls_ms"] = m.ModelCallsMS
	}
	return e
}

// toolDurationMS is a tool row's execution time: the agent's own measurement
// when it sent one, else the span from the row's start to the result event.
func toolDurationMS(measured time.Duration, started, resultAt time.Time) int64 {
	if measured > 0 {
		return measured.Milliseconds()
	}
	if started.IsZero() {
		return 0
	}
	if resultAt.IsZero() {
		resultAt = time.Now()
	}
	return resultAt.Sub(started).Milliseconds()
}
