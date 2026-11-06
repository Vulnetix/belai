package config

import "fmt"

// CompactionSettings tunes when a session compacts its context and how much
// survives (docs/compaction.md). Every field is a plain number the harness
// reads; no model is asked anything. A zero pointer means the built-in
// default.
type CompactionSettings struct {
	// ThresholdPct is the share of the model's context window, in percent,
	// above which compaction runs at a pass boundary. Default 70, 30 to 95.
	ThresholdPct *int `json:"threshold_pct,omitempty"`
	// KeepRecentTurns is how many of the newest turns a prune never touches.
	// Default 6, 2 to 40.
	KeepRecentTurns *int `json:"keep_recent_turns,omitempty"`
	// KeepResultChars is how much of a tool result survives when a prune keeps
	// only its call. Default 300, 0 to 2000.
	KeepResultChars *int `json:"keep_result_chars,omitempty"`
	// KeepToolResults is how many of the newest tool results survive the
	// clearing fallback after a context overflow. Default 2, 0 to 20.
	KeepToolResults *int `json:"keep_tool_results,omitempty"`
	// BreakerFailures is how many summaries in a row may fail before the
	// summary step pauses. Default 3, 1 to 20.
	BreakerFailures *int `json:"breaker_failures,omitempty"`
	// BreakerCooldownPasses is how many pass boundaries the summary step stays
	// paused once the breaker opens. Default 4, 1 to 20.
	BreakerCooldownPasses *int `json:"breaker_cooldown_passes,omitempty"`
}

// Compaction defaults. internal/agent aliases these as the constants the
// pruning code and its documentation tests were written against.
const (
	DefaultCompactThresholdPct          = 70
	DefaultCompactKeepRecentTurns       = 6
	DefaultCompactKeepResultChars       = 300
	DefaultCompactKeepToolResults       = 2
	DefaultCompactBreakerFailures       = 3
	DefaultCompactBreakerCooldownPasses = 4
)

// compactionField describes one key: where it lives, its default and bounds,
// and which direction a repository may move it. A repository may only move a
// value toward compacting sooner or keeping more, because the other direction
// lets a cloned project stop a user's session from compacting.
type compactionField struct {
	key            string
	get            func(*CompactionSettings) **int
	def            int
	min, max       int
	lowerIsTighter bool
}

var compactionFields = []compactionField{
	{"compaction.threshold_pct", func(c *CompactionSettings) **int { return &c.ThresholdPct }, DefaultCompactThresholdPct, 30, 95, true},
	{"compaction.keep_recent_turns", func(c *CompactionSettings) **int { return &c.KeepRecentTurns }, DefaultCompactKeepRecentTurns, 2, 40, false},
	{"compaction.keep_result_chars", func(c *CompactionSettings) **int { return &c.KeepResultChars }, DefaultCompactKeepResultChars, 0, 2000, false},
	{"compaction.keep_tool_results", func(c *CompactionSettings) **int { return &c.KeepToolResults }, DefaultCompactKeepToolResults, 0, 20, false},
	{"compaction.breaker_failures", func(c *CompactionSettings) **int { return &c.BreakerFailures }, DefaultCompactBreakerFailures, 1, 20, false},
	{"compaction.breaker_cooldown_passes", func(c *CompactionSettings) **int { return &c.BreakerCooldownPasses }, DefaultCompactBreakerCooldownPasses, 1, 20, true},
}

// value is the field's effective value: the set one when it is in range,
// otherwise the default, so a bad value can never reach the compaction code.
func (f compactionField) value(c *CompactionSettings) int {
	if c == nil {
		return f.def
	}
	p := *f.get(c)
	if p == nil || *p < f.min || *p > f.max {
		return f.def
	}
	return *p
}

func (s Settings) compactionValue(i int) int { return compactionFields[i].value(s.Compaction) }

// CompactThresholdPct is the context share, in percent, that triggers
// compaction.
func (s Settings) CompactThresholdPct() int { return s.compactionValue(0) }

// CompactKeepRecentTurns is how many of the newest turns a prune keeps whole.
func (s Settings) CompactKeepRecentTurns() int { return s.compactionValue(1) }

// CompactKeepResultChars is how much of a result a prune keeps when it keeps
// only the call.
func (s Settings) CompactKeepResultChars() int { return s.compactionValue(2) }

// CompactKeepToolResults is how many of the newest tool results survive the
// overflow clearing fallback.
func (s Settings) CompactKeepToolResults() int { return s.compactionValue(3) }

// CompactBreakerFailures is how many summary failures in a row open the
// breaker.
func (s Settings) CompactBreakerFailures() int { return s.compactionValue(4) }

// CompactBreakerCooldownPasses is how many pass boundaries an open breaker
// skips the summary step for.
func (s Settings) CompactBreakerCooldownPasses() int { return s.compactionValue(5) }

// ValidateCompaction rejects a compaction value outside its bounds, naming
// the key. A repository's out-of-range value never gets here: mergeCompaction
// drops it.
func ValidateCompaction(s Settings) error {
	if s.Compaction == nil {
		return nil
	}
	for _, f := range compactionFields {
		if p := *f.get(s.Compaction); p != nil && (*p < f.min || *p > f.max) {
			return fmt.Errorf("%s %d must be between %d and %d", f.key, *p, f.min, f.max)
		}
	}
	return nil
}

// mergeCompaction folds in over cur. A user layer sets any key. The project
// layer, which a cloned repository controls, sets a key only when its value is
// in range and moves the effective value toward compacting sooner (a lower
// threshold or cooldown) or keeping more (a higher keep count or breaker
// tolerance).
func mergeCompaction(cur, in *CompactionSettings, project bool) *CompactionSettings {
	if in == nil {
		return cur
	}
	out := &CompactionSettings{}
	if cur != nil {
		*out = *cur
	}
	for _, f := range compactionFields {
		v := *f.get(in)
		if v == nil {
			continue
		}
		if project {
			if *v < f.min || *v > f.max {
				continue
			}
			eff := f.value(out)
			if (f.lowerIsTighter && *v >= eff) || (!f.lowerIsTighter && *v <= eff) {
				continue
			}
		}
		n := *v
		*f.get(out) = &n
	}
	return out
}

// compactionIgnored names the project keys mergeCompaction would not apply on
// top of user, for the note Resolve prints.
func compactionIgnored(user, proj *CompactionSettings) []string {
	if proj == nil {
		return nil
	}
	merged := mergeCompaction(user, proj, true)
	var out []string
	for _, f := range compactionFields {
		if v := *f.get(proj); v != nil && f.value(merged) != *v {
			out = append(out, f.key)
		}
	}
	return out
}
