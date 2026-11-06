package config

import (
	"fmt"
	"math"
)

// RunCapsSettings sets the run caps (docs/run-caps.md): ceilings on what one
// session may spend. A cap left unset is off. The pass ceiling is
// resilience.max_passes, not a key here.
//
// The user's layers and the command-line flags set and raise any key. The
// project layer, which a cloned repository controls, can only narrow: it adds a
// cap or lowers one, finalize_at (earlier warning) and max_extends (fewer
// extensions), and a zero or an out-of-range value from it is dropped.
type RunCapsSettings struct {
	// MaxCostUSD is the most the session's main-model calls may cost, in US
	// dollars at the model's price. Zero or unset is off. A model with no known
	// price leaves it off for that model.
	MaxCostUSD *float64 `json:"max_cost_usd,omitempty"`
	// MaxTokens is the most tokens (prompt plus completion) the session's
	// main-model calls may use. Zero or unset is off.
	MaxTokens *int `json:"max_tokens,omitempty"`
	// MaxWallMinutes is the most time, in minutes, the session may spend running
	// turns. Time between turns does not count. Zero or unset is off.
	MaxWallMinutes *int `json:"max_wall_minutes,omitempty"`
	// FinalizeAt is the share of a cap, in (0, 1), at which the model is told to
	// finalise. Default 0.9.
	FinalizeAt *float64 `json:"finalize_at,omitempty"`
	// MaxExtends is how many times the user may extend the caps by half of each
	// one. Default 3, 0 to 20; 0 means a reached cap always stops the run.
	MaxExtends *int `json:"max_extends,omitempty"`
}

// Run cap defaults and bounds.
const (
	DefaultRunCapFinalizeAt = 0.9
	DefaultRunCapMaxExtends = 3

	maxRunCapCostUSD     = 100000.0
	maxRunCapTokens      = 1_000_000_000_000
	maxRunCapWallMinutes = 43200 // 30 days
	maxRunCapExtends     = 20
)

func validRunCapCost(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= maxRunCapCostUSD
}

func validRunCapFinalize(v float64) bool { return !math.IsNaN(v) && v > 0 && v < 1 }

// ValidateRunCaps rejects a malformed run cap, naming the key: a negative or
// non-finite number, a finalize_at outside (0, 1), or a value past its ceiling.
// A repository's bad value never gets here: mergeRunCaps drops it.
func ValidateRunCaps(s Settings) error {
	r := s.RunCaps
	if r == nil {
		return nil
	}
	if r.MaxCostUSD != nil && !validRunCapCost(*r.MaxCostUSD) {
		return fmt.Errorf("run_caps.max_cost_usd %v must be a number from 0 to %v", *r.MaxCostUSD, maxRunCapCostUSD)
	}
	if r.MaxTokens != nil && (*r.MaxTokens < 0 || *r.MaxTokens > maxRunCapTokens) {
		return fmt.Errorf("run_caps.max_tokens %d must be between 0 and %d", *r.MaxTokens, maxRunCapTokens)
	}
	if r.MaxWallMinutes != nil && (*r.MaxWallMinutes < 0 || *r.MaxWallMinutes > maxRunCapWallMinutes) {
		return fmt.Errorf("run_caps.max_wall_minutes %d must be between 0 and %d", *r.MaxWallMinutes, maxRunCapWallMinutes)
	}
	if r.FinalizeAt != nil && !validRunCapFinalize(*r.FinalizeAt) {
		return fmt.Errorf("run_caps.finalize_at %v must be greater than 0 and less than 1", *r.FinalizeAt)
	}
	if r.MaxExtends != nil && (*r.MaxExtends < 0 || *r.MaxExtends > maxRunCapExtends) {
		return fmt.Errorf("run_caps.max_extends %d must be between 0 and %d", *r.MaxExtends, maxRunCapExtends)
	}
	return nil
}

// RunCapCostMicroUSD is the cost cap in millionths of a dollar, 0 when off.
func (s Settings) RunCapCostMicroUSD() int64 {
	if s.RunCaps == nil || s.RunCaps.MaxCostUSD == nil || !validRunCapCost(*s.RunCaps.MaxCostUSD) {
		return 0
	}
	return int64(math.Round(*s.RunCaps.MaxCostUSD * 1e6))
}

// RunCapTokens is the token cap, 0 when off.
func (s Settings) RunCapTokens() int {
	if s.RunCaps == nil || s.RunCaps.MaxTokens == nil || *s.RunCaps.MaxTokens < 0 || *s.RunCaps.MaxTokens > maxRunCapTokens {
		return 0
	}
	return *s.RunCaps.MaxTokens
}

// RunCapWallMinutes is the wall-clock cap in minutes, 0 when off.
func (s Settings) RunCapWallMinutes() int {
	if s.RunCaps == nil || s.RunCaps.MaxWallMinutes == nil || *s.RunCaps.MaxWallMinutes < 0 || *s.RunCaps.MaxWallMinutes > maxRunCapWallMinutes {
		return 0
	}
	return *s.RunCaps.MaxWallMinutes
}

// RunCapFinalizeAt is the share of a cap at which the model is told to
// finalise.
func (s Settings) RunCapFinalizeAt() float64 {
	if s.RunCaps == nil || s.RunCaps.FinalizeAt == nil || !validRunCapFinalize(*s.RunCaps.FinalizeAt) {
		return DefaultRunCapFinalizeAt
	}
	return *s.RunCaps.FinalizeAt
}

// RunCapMaxExtends is how many times the user may extend a reached cap.
func (s Settings) RunCapMaxExtends() int {
	if s.RunCaps == nil || s.RunCaps.MaxExtends == nil || *s.RunCaps.MaxExtends < 0 || *s.RunCaps.MaxExtends > maxRunCapExtends {
		return DefaultRunCapMaxExtends
	}
	return *s.RunCaps.MaxExtends
}

// RunCapsActive reports whether any spend, token or wall cap is set. The pass
// ceiling (resilience.max_passes) has its own switch and does not count.
func (s Settings) RunCapsActive() bool {
	return s.RunCapCostMicroUSD() > 0 || s.RunCapTokens() > 0 || s.RunCapWallMinutes() > 0
}

// mergeRunCaps folds in over cur. A user layer (or a flag) sets any key, so it
// may raise or clear a cap. The project layer applies a cap only when it is
// positive, in range and below the value in force (or none is), and finalize_at
// and max_extends only when lower, so a cloned repository can narrow a session
// and never widen it.
func mergeRunCaps(cur, in *RunCapsSettings, project bool) *RunCapsSettings {
	if in == nil {
		return cur
	}
	out := &RunCapsSettings{}
	if cur != nil {
		*out = *cur
	}
	eff := Settings{RunCaps: out}
	if in.MaxCostUSD != nil {
		v := *in.MaxCostUSD
		curV := float64(eff.RunCapCostMicroUSD()) / 1e6
		if !project || (validRunCapCost(v) && v > 0 && (curV == 0 || v < curV)) {
			out.MaxCostUSD = &v
		}
	}
	if in.MaxTokens != nil {
		v := *in.MaxTokens
		curV := eff.RunCapTokens()
		if !project || (v > 0 && v <= maxRunCapTokens && (curV == 0 || v < curV)) {
			out.MaxTokens = &v
		}
	}
	if in.MaxWallMinutes != nil {
		v := *in.MaxWallMinutes
		curV := eff.RunCapWallMinutes()
		if !project || (v > 0 && v <= maxRunCapWallMinutes && (curV == 0 || v < curV)) {
			out.MaxWallMinutes = &v
		}
	}
	if in.FinalizeAt != nil {
		v := *in.FinalizeAt
		if !project || (validRunCapFinalize(v) && v < eff.RunCapFinalizeAt()) {
			out.FinalizeAt = &v
		}
	}
	if in.MaxExtends != nil {
		v := *in.MaxExtends
		if !project || (v >= 0 && v <= maxRunCapExtends && v < eff.RunCapMaxExtends()) {
			out.MaxExtends = &v
		}
	}
	return out
}

// runCapsIgnored names the project keys mergeRunCaps would not apply on top of
// user, for the note Resolve prints.
func runCapsIgnored(user, proj *RunCapsSettings) []string {
	if proj == nil {
		return nil
	}
	merged := mergeRunCaps(user, proj, true)
	var out []string
	if proj.MaxCostUSD != nil && (merged.MaxCostUSD == nil || *merged.MaxCostUSD != *proj.MaxCostUSD) {
		out = append(out, "run_caps.max_cost_usd")
	}
	if proj.MaxTokens != nil && (merged.MaxTokens == nil || *merged.MaxTokens != *proj.MaxTokens) {
		out = append(out, "run_caps.max_tokens")
	}
	if proj.MaxWallMinutes != nil && (merged.MaxWallMinutes == nil || *merged.MaxWallMinutes != *proj.MaxWallMinutes) {
		out = append(out, "run_caps.max_wall_minutes")
	}
	if proj.FinalizeAt != nil && (merged.FinalizeAt == nil || *merged.FinalizeAt != *proj.FinalizeAt) {
		out = append(out, "run_caps.finalize_at")
	}
	if proj.MaxExtends != nil && (merged.MaxExtends == nil || *merged.MaxExtends != *proj.MaxExtends) {
		out = append(out, "run_caps.max_extends")
	}
	return out
}
