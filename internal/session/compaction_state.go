package session

import "math"

// EntryTypeCompactionState is the session entry type carrying the compaction
// circuit breaker, so a resumed or forked session keeps a paused summary step
// paused instead of retrying a failing classifier at once.
const EntryTypeCompactionState = "compaction_state"

// Bounds a value read back from a session file is clamped to. The file is not
// trusted: a hand-edited or damaged entry must not be able to hold the summary
// step off for long or overflow an int.
const (
	maxCompactionFails    = 1000
	maxCompactionCooldown = 20
)

// CompactionState is the breaker's persisted state.
type CompactionState struct {
	// Fails is the number of summary failures in a row.
	Fails int
	// CooldownLeft is how many pass boundaries the summary step still skips.
	// It is a count, not a position, because the pass counter restarts with
	// each process.
	CooldownLeft int
	// Tokens and Window are the last estimate and window the harness measured,
	// for /compact status after a resume. Facts only.
	Tokens int
	Window int
}

// Clamp returns the state with every field inside its bounds.
func (c CompactionState) Clamp() CompactionState {
	c.Fails = min(max(c.Fails, 0), maxCompactionFails)
	c.CooldownLeft = min(max(c.CooldownLeft, 0), maxCompactionCooldown)
	c.Tokens = min(max(c.Tokens, 0), math.MaxInt32)
	c.Window = min(max(c.Window, 0), math.MaxInt32)
	return c
}

// ToEntry renders the state as a session entry. The values ride in Meta, as
// numbers only.
func (c CompactionState) ToEntry(parentID string) Entry {
	c = c.Clamp()
	return Entry{
		ID:       MustID(),
		ParentID: parentID,
		Type:     EntryTypeCompactionState,
		Meta: map[string]any{
			"fails":         c.Fails,
			"cooldown_left": c.CooldownLeft,
			"tokens":        c.Tokens,
			"window":        c.Window,
		},
	}
}

// CompactionStateFromEntry reads a compaction_state entry. A value that is not
// a finite number reads as zero, and every field is clamped.
func CompactionStateFromEntry(e Entry) CompactionState {
	return CompactionState{
		Fails:        boundedInt(e.Meta, "fails"),
		CooldownLeft: boundedInt(e.Meta, "cooldown_left"),
		Tokens:       boundedInt(e.Meta, "tokens"),
		Window:       boundedInt(e.Meta, "window"),
	}.Clamp()
}

// LatestCompactionState returns the last compaction_state entry, or ok=false
// when there is none.
func LatestCompactionState(entries []Entry) (CompactionState, bool) {
	var out CompactionState
	found := false
	for _, e := range entries {
		if e.Type != EntryTypeCompactionState {
			continue
		}
		out, found = CompactionStateFromEntry(e), true
	}
	return out, found
}

// boundedInt reads a whole number from a decoded JSON map: JSON numbers arrive as
// float64, and a fresh in-memory entry holds an int.
func boundedInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(max(min(v, math.MaxInt32), math.MinInt32))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return int(max(min(v, math.MaxInt32), math.MinInt32))
	}
	return 0
}
