package config

import (
	"fmt"
	"math"
	"sync/atomic"
)

// JevThresholds are the score cut-offs the decision gates and relevance jobs
// read a probability against. Every field is a probability in [0,1].
type JevThresholds struct {
	// AllowAt and DenyAt bound the security gate: a score at or below AllowAt
	// lets a call or content through, one at or above DenyAt blocks it, and the
	// band between is handed to the agent model or a human.
	AllowAt, DenyAt float64
	// RouteAt is the score a routing candidate must exceed.
	RouteAt float64
	// DropAt, KeepAt, StrongAt: below DropAt an item leaves a list, at KeepAt it
	// stays, at StrongAt it is added even when no other signal picked it.
	DropAt, KeepAt, StrongAt float64
	// SwapAt is the score a single candidate needs to replace a Bash call.
	SwapAt float64
	// VoiceAt is the score a single target needs for a spoken instruction to run it.
	VoiceAt float64
	// SimpleAt is the score a request needs to be worked as a simple one:
	// without the goal contract, prefetch or verification ceremony.
	SimpleAt float64
	// GoalCompleteAt is the score the goal judge needs to call a pass complete
	// without asking the model judge; GoalRivalMax is the most a rival verdict
	// may score for that call to stand. GoalNotStartedAt is the score for a
	// clear "not started".
	GoalCompleteAt, GoalRivalMax, GoalNotStartedAt float64
	// TriageAt: below it another edit pass is judged unlikely to help.
	TriageAt float64
	// HitAt and LeadAt: explore_locate marks a file a hit or a lead.
	HitAt, LeadAt float64
	// OptionHit, OptionMargin, OptionLead: the first option is a clear winner
	// when it scores OptionHit with a lead of OptionMargin, or leads by OptionLead.
	OptionHit, OptionMargin, OptionLead float64
	// ModeConfident and ModeMargin: a detected intent is confident at
	// ModeConfident with a lead of ModeMargin over the runner-up. ModeHeadless is
	// the score a non-interactive run needs to accept it.
	ModeConfident, ModeMargin, ModeHeadless float64
	// ClearAt, AlignAt and CoverAt are the delivery relevance jobs' cut-offs:
	// a handoff rated below ClearAt goes to review, a gate rated below AlignAt is
	// flagged, and a request clause whose covering tasks all rate below CoverAt
	// gets a gap card. Each only narrows, so a higher value means more review.
	ClearAt, AlignAt, CoverAt float64
}

// DefaultJevThresholds are the values used when nothing is configured.
func DefaultJevThresholds() JevThresholds {
	return JevThresholds{
		AllowAt: 0.10, DenyAt: 0.90, RouteAt: 0.50,
		DropAt: 0.10, KeepAt: 0.50, StrongAt: 0.80, SwapAt: 0.95, VoiceAt: 0.95, SimpleAt: 0.80,
		GoalCompleteAt: 0.90, GoalRivalMax: 0.20, GoalNotStartedAt: 0.85,
		TriageAt: 0.30, HitAt: 0.50, LeadAt: 0.25,
		OptionHit: 0.50, OptionMargin: 0.10, OptionLead: 0.25,
		ModeConfident: 0.80, ModeMargin: 0.25, ModeHeadless: 0.50,
		ClearAt: 0.50, AlignAt: 0.40, CoverAt: 0.40,
	}
}

// JevThresholdSettings is the settings form of JevThresholds: a nil key keeps
// the default.
type JevThresholdSettings struct {
	AllowAt          *float64 `json:"allow_at,omitempty"`
	DenyAt           *float64 `json:"deny_at,omitempty"`
	RouteAt          *float64 `json:"route_at,omitempty"`
	DropAt           *float64 `json:"drop_at,omitempty"`
	KeepAt           *float64 `json:"keep_at,omitempty"`
	StrongAt         *float64 `json:"strong_at,omitempty"`
	SwapAt           *float64 `json:"swap_at,omitempty"`
	VoiceAt          *float64 `json:"voice_at,omitempty"`
	SimpleAt         *float64 `json:"simple_at,omitempty"`
	GoalCompleteAt   *float64 `json:"goal_complete_at,omitempty"`
	GoalRivalMax     *float64 `json:"goal_rival_max,omitempty"`
	GoalNotStartedAt *float64 `json:"goal_not_started_at,omitempty"`
	TriageAt         *float64 `json:"triage_at,omitempty"`
	HitAt            *float64 `json:"hit_at,omitempty"`
	LeadAt           *float64 `json:"lead_at,omitempty"`
	OptionHit        *float64 `json:"option_hit,omitempty"`
	OptionMargin     *float64 `json:"option_margin,omitempty"`
	OptionLead       *float64 `json:"option_lead,omitempty"`
	ModeConfident    *float64 `json:"mode_confident,omitempty"`
	ModeMargin       *float64 `json:"mode_margin,omitempty"`
	ModeHeadless     *float64 `json:"mode_headless,omitempty"`
	ClearAt          *float64 `json:"clear_at,omitempty"`
	AlignAt          *float64 `json:"align_at,omitempty"`
	CoverAt          *float64 `json:"cover_at,omitempty"`
}

// thresholdSlot pairs a setting key with its slot in the settings form and in
// the resolved form.
type thresholdSlot struct {
	key string
	set **float64
	val *float64
}

func (t *JevThresholdSettings) slots(r *JevThresholds) []thresholdSlot {
	return []thresholdSlot{
		{"allow_at", &t.AllowAt, &r.AllowAt}, {"deny_at", &t.DenyAt, &r.DenyAt},
		{"route_at", &t.RouteAt, &r.RouteAt}, {"drop_at", &t.DropAt, &r.DropAt},
		{"keep_at", &t.KeepAt, &r.KeepAt}, {"strong_at", &t.StrongAt, &r.StrongAt},
		{"swap_at", &t.SwapAt, &r.SwapAt}, {"voice_at", &t.VoiceAt, &r.VoiceAt}, {"simple_at", &t.SimpleAt, &r.SimpleAt}, {"triage_at", &t.TriageAt, &r.TriageAt},
		{"goal_complete_at", &t.GoalCompleteAt, &r.GoalCompleteAt}, {"goal_rival_max", &t.GoalRivalMax, &r.GoalRivalMax},
		{"goal_not_started_at", &t.GoalNotStartedAt, &r.GoalNotStartedAt},
		{"hit_at", &t.HitAt, &r.HitAt}, {"lead_at", &t.LeadAt, &r.LeadAt},
		{"option_hit", &t.OptionHit, &r.OptionHit}, {"option_margin", &t.OptionMargin, &r.OptionMargin},
		{"option_lead", &t.OptionLead, &r.OptionLead}, {"mode_confident", &t.ModeConfident, &r.ModeConfident},
		{"mode_margin", &t.ModeMargin, &r.ModeMargin}, {"mode_headless", &t.ModeHeadless, &r.ModeHeadless},
		{"clear_at", &t.ClearAt, &r.ClearAt}, {"align_at", &t.AlignAt, &r.AlignAt}, {"cover_at", &t.CoverAt, &r.CoverAt},
	}
}

// Resolved fills the defaults in. A nil receiver is all defaults.
func (t *JevThresholdSettings) Resolved() JevThresholds {
	r := DefaultJevThresholds()
	if t == nil {
		return r
	}
	for _, f := range t.slots(&r) {
		if *f.set != nil {
			*f.val = **f.set
		}
	}
	return r
}

// overlay returns t with every key set in src replacing its own.
func (t *JevThresholdSettings) overlay(src *JevThresholdSettings) *JevThresholdSettings {
	out := &JevThresholdSettings{}
	if t != nil {
		*out = *t
	}
	var scratch JevThresholds
	dst := out.slots(&scratch)
	for i, f := range src.slots(&scratch) {
		if *f.set != nil {
			v := **f.set
			*dst[i].set = &v
		}
	}
	return out
}

// minBand is the least gap between the allow and deny cut-offs, so the
// inconclusive band never closes.
const minBand = 0.05

// JevMinBand is minBand for /settings, which states it beside the two cut-offs.
const JevMinBand = minBand

// validate refuses a value outside [0,1] and any combination that would let a
// score both allow and deny, or invert an ordered pair. Invalid settings are an
// error, never silently reset.
func (t *JevThresholdSettings) validate() error {
	if t == nil {
		return nil
	}
	var scratch JevThresholds
	for _, f := range t.slots(&scratch) {
		if v := *f.set; v != nil && (math.IsNaN(*v) || *v < 0 || *v > 1) {
			return fmt.Errorf("jev.thresholds.%s %v must be between 0 and 1", f.key, *v)
		}
	}
	r := t.Resolved()
	switch {
	case r.AllowAt > 0.5:
		return fmt.Errorf("jev.thresholds.allow_at %v must be at most 0.5", r.AllowAt)
	case r.DenyAt < 0.5:
		return fmt.Errorf("jev.thresholds.deny_at %v must be at least 0.5", r.DenyAt)
	case r.DenyAt-r.AllowAt < minBand:
		return fmt.Errorf("jev.thresholds: deny_at %v must exceed allow_at %v by at least %v", r.DenyAt, r.AllowAt, minBand)
	case r.DropAt > r.KeepAt:
		return fmt.Errorf("jev.thresholds: drop_at %v must not exceed keep_at %v", r.DropAt, r.KeepAt)
	case r.KeepAt > r.StrongAt:
		return fmt.Errorf("jev.thresholds: keep_at %v must not exceed strong_at %v", r.KeepAt, r.StrongAt)
	case r.VoiceAt < 0.5:
		return fmt.Errorf("jev.thresholds.voice_at %v must be at least 0.5: a spoken instruction runs an action, so it needs a clear majority", r.VoiceAt)
	case r.SimpleAt < 0.5:
		return fmt.Errorf("jev.thresholds.simple_at %v must be at least 0.5: a simple verdict drops safeguards around a turn, so it needs a clear majority", r.SimpleAt)
	case r.GoalCompleteAt < 0.5:
		return fmt.Errorf("jev.thresholds.goal_complete_at %v must be at least 0.5: it ends a goal pass without the model judge, so it needs a clear majority", r.GoalCompleteAt)
	case r.GoalNotStartedAt < 0.5:
		return fmt.Errorf("jev.thresholds.goal_not_started_at %v must be at least 0.5", r.GoalNotStartedAt)
	case r.GoalRivalMax > 0.5:
		return fmt.Errorf("jev.thresholds.goal_rival_max %v must be at most 0.5: a rival verdict above it contradicts a clear call", r.GoalRivalMax)
	case r.LeadAt > r.HitAt:
		return fmt.Errorf("jev.thresholds: lead_at %v must not exceed hit_at %v", r.LeadAt, r.HitAt)
	}
	return nil
}

// JevThresholds returns the effective cut-offs for these settings.
func (s Settings) JevThresholds() JevThresholds {
	if s.Jev == nil {
		return DefaultJevThresholds()
	}
	return s.Jev.Thresholds.Resolved()
}

var activeJev atomic.Pointer[JevThresholds]

// SetActiveJevThresholds installs the cut-offs the gates and jobs read. Resolve
// calls it with the user's settings; nothing else should.
func SetActiveJevThresholds(t JevThresholds) { activeJev.Store(&t) }

// ActiveJevThresholds returns the installed cut-offs, or the defaults.
func ActiveJevThresholds() JevThresholds {
	if t := activeJev.Load(); t != nil {
		return *t
	}
	return DefaultJevThresholds()
}

// JevThresholdKeys lists every threshold key in the order the docs and
// /settings show them.
func JevThresholdKeys() []string {
	var scratch JevThresholds
	var t JevThresholdSettings
	slots := t.slots(&scratch)
	keys := make([]string, len(slots))
	for i, s := range slots {
		keys[i] = s.key
	}
	return keys
}

// Value returns the effective value of one key (its default when unset) and
// whether it is set explicitly. ok is false for an unknown key.
func (t *JevThresholdSettings) Value(key string) (v float64, set, ok bool) {
	r := t.Resolved()
	if t == nil {
		t = &JevThresholdSettings{}
	}
	for _, s := range t.slots(&r) {
		if s.key == key {
			return *s.val, *s.set != nil, true
		}
	}
	return 0, false, false
}

// With returns a copy of t with one key set to v, or back to its default when v
// is nil. An unknown key returns the copy unchanged. It does not validate; call
// ValidateJev on the result.
func (t *JevThresholdSettings) With(key string, v *float64) *JevThresholdSettings {
	out := &JevThresholdSettings{}
	if t != nil {
		*out = *t
	}
	var scratch JevThresholds
	for _, s := range out.slots(&scratch) {
		if s.key == key {
			if v == nil {
				*s.set = nil
			} else {
				c := *v
				*s.set = &c
			}
		}
	}
	return out
}

// Validate checks the thresholds on their own.
func (t *JevThresholdSettings) Validate() error { return t.validate() }
