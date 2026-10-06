package tui

import (
	"time"

	"github.com/vulnetix/belai/internal/config"
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

// maxPlaceBack bounds how many rows a late row may hop back over.
const maxPlaceBack = 64

// movableRow reports whether a row may be placed by its time. Only notices and
// cards move: a user prompt, an assistant bubble, a reasoning row or a tool row
// is paired with its neighbours by the persistence and history code, so it
// stays where the event stream put it.
func movableRow(m components.Message) bool {
	switch m.Role {
	case "rolemanager", "system", components.ReportRole, components.VulnRole:
		return true
	}
	return false
}

// placeByTime moves messages[i] back to where its own time puts it.
//
// Agent events and role-manager decisions reach the TUI on two channels that
// are read independently, so a decision made before a tool result can arrive
// after it. The row is stamped with the time it happened, so it is moved back
// over any row of the same turn stamped later. It never crosses a user prompt
// or an unstamped row.
//
// The persistence cursor counts the leading rows already written. A row that
// needs no write (a decision, already recorded, or an ephemeral card) may move
// anywhere in the turn and the cursor then covers it; a row that still has to
// be written never moves behind the cursor, so it is written once, in place.
func (a *App) placeByTime(i int) {
	if i < 1 || i >= len(a.messages) {
		return
	}
	row := a.messages[i]
	if row.CreatedAt.IsZero() || !movableRow(row) {
		return
	}
	free := row.Persisted || neverPersisted(row)
	lo := 0
	if !free {
		lo = a.persistedUpTo
	}
	j := i
	for j > lo && i-j < maxPlaceBack {
		p := a.messages[j-1]
		if p.Role == "user" || p.CreatedAt.IsZero() || !p.CreatedAt.After(row.CreatedAt) {
			break
		}
		j--
	}
	if j == i {
		return
	}
	copy(a.messages[j+1:i+1], a.messages[j:i])
	a.messages[j] = row
	// Every row from j to i moved down one.
	if free && j < a.persistedUpTo {
		a.persistedUpTo++
	}
	shift := func(idx *int) {
		if *idx >= j && *idx < i {
			*idx++
		}
	}
	shift(&a.tts.cardIdx)
	shift(&a.tts.srcIdx)
	shift(&a.saveFileMsg)
}

// placeLate places every row added since index from by its time. It is the
// chronological layout's pass over what one event created: a notice an event
// raised while an earlier decision was already in the thread lands in time
// order too.
func (a *App) placeLate(from int) {
	for i := max(from, 1); i < len(a.messages); i++ {
		a.placeByTime(i)
	}
}

// chronological reports whether the thread is laid out strictly by time.
func (a *App) chronological() bool { return a.settings.Layout() == config.LayoutChronological }

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
