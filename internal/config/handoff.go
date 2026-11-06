package config

// HandoffSettings governs /relay, the reviewed handoff into a fresh session
// (docs/handoff.md). The draft is prose a model writes from the transcript and
// a block of facts the harness collects; nothing is written to disk until the
// user accepts it. A nil field means the built-in default.
type HandoffSettings struct {
	// Enabled switches /relay off. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxBytes caps the handoff text, both what the drafter is asked for and
	// what the review accepts. Default 16384, clamped to 2048 through 65536.
	MaxBytes *int `json:"max_bytes,omitempty"`
	// IncludeGoal sends the goal objective and the todo list to the drafter as
	// facts. nil means on.
	IncludeGoal *bool `json:"include_goal,omitempty"`
}

// Handoff bounds.
const (
	DefaultHandoffMaxBytes = 16 * 1024
	MinHandoffMaxBytes     = 2 * 1024
	MaxHandoffMaxBytes     = 64 * 1024
)

// HandoffEnabled reports whether /relay is available. Default on.
func (s Settings) HandoffEnabled() bool {
	return s.Handoff == nil || s.Handoff.Enabled == nil || *s.Handoff.Enabled
}

// HandoffIncludeGoal reports whether the goal objective and todos are given to
// the drafter. Default on.
func (s Settings) HandoffIncludeGoal() bool {
	return s.Handoff == nil || s.Handoff.IncludeGoal == nil || *s.Handoff.IncludeGoal
}

// HandoffMaxBytes is the size cap of the handoff text: the set value clamped
// to its bounds, otherwise the default, so a bad value never reaches the
// drafter or the review.
func (s Settings) HandoffMaxBytes() int {
	if s.Handoff == nil || s.Handoff.MaxBytes == nil {
		return DefaultHandoffMaxBytes
	}
	return clampHandoffBytes(*s.Handoff.MaxBytes)
}

func clampHandoffBytes(n int) int {
	switch {
	case n < MinHandoffMaxBytes:
		return MinHandoffMaxBytes
	case n > MaxHandoffMaxBytes:
		return MaxHandoffMaxBytes
	}
	return n
}

// mergeHandoff folds from over base. A user layer sets any key. The project
// layer, which a cloned repository controls, may turn /relay off, turn
// include_goal off and lower max_bytes, and never enable, raise or set true.
func mergeHandoff(base, from *HandoffSettings, project bool) *HandoffSettings {
	if from == nil {
		return base
	}
	out := &HandoffSettings{}
	if base != nil {
		*out = *base
	}
	for _, p := range []struct{ in, out **bool }{{&from.Enabled, &out.Enabled}, {&from.IncludeGoal, &out.IncludeGoal}} {
		if *p.in == nil || (project && **p.in) {
			continue
		}
		v := **p.in
		*p.out = &v
	}
	if from.MaxBytes != nil {
		v := clampHandoffBytes(*from.MaxBytes)
		if !project || v < (Settings{Handoff: out}).HandoffMaxBytes() {
			out.MaxBytes = &v
		}
	}
	return out
}

// handoffIgnored names the project keys mergeHandoff would not apply on top of
// user, for the note Resolve prints.
func handoffIgnored(user, proj *HandoffSettings) []string {
	if proj == nil {
		return nil
	}
	var out []string
	cur := Settings{Handoff: user}
	if proj.Enabled != nil && *proj.Enabled && !cur.HandoffEnabled() {
		out = append(out, "handoff.enabled")
	}
	if proj.IncludeGoal != nil && *proj.IncludeGoal && !cur.HandoffIncludeGoal() {
		out = append(out, "handoff.include_goal")
	}
	if proj.MaxBytes != nil && clampHandoffBytes(*proj.MaxBytes) >= cur.HandoffMaxBytes() {
		out = append(out, "handoff.max_bytes")
	}
	return out
}
