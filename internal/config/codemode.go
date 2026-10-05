package config

// CodeSettings governs code mode (docs/code-mode.md): the Code tool's
// script interpreter. Every limit has a default; a repository-visible project
// layer may turn code mode off and lower a limit, never turn it on or raise
// one, because a script is the model acting on the user's behalf.
type CodeSettings struct {
	// Enabled turns code mode on. nil means on.
	Enabled *bool `json:"enabled,omitempty"`
	// TimeoutMS bounds one script's wall-clock time.
	TimeoutMS *int `json:"timeout_ms,omitempty"`
	// MaxCalls bounds the nested tool calls one script may make.
	MaxCalls *int `json:"max_calls,omitempty"`
	// MaxOutputBytes bounds what one script may print.
	MaxOutputBytes *int `json:"max_output_bytes,omitempty"`
}

// Code mode defaults and ceilings. A layer may set a value below the
// default or between the default and the ceiling (user layers only); nothing
// goes above the ceiling.
const (
	DefaultCodeTimeoutMS      = 120_000
	DefaultCodeMaxCalls       = 50
	DefaultCodeMaxOutputBytes = 16 * 1024
	maxCodeTimeoutMS          = 600_000
	maxCodeMaxCalls           = 500
	maxCodeMaxOutputBytes     = 256 * 1024
)

// CodeEnabled reports whether code mode is available. Default on.
func (s Settings) CodeEnabled() bool {
	return s.Code == nil || s.Code.Enabled == nil || *s.Code.Enabled
}

// CodeLimits returns the effective timeout (milliseconds), nested call budget
// and output cap (bytes), each clamped to its ceiling.
func (s Settings) CodeLimits() (timeoutMS, maxCalls, maxOutput int) {
	timeoutMS, maxCalls, maxOutput = DefaultCodeTimeoutMS, DefaultCodeMaxCalls, DefaultCodeMaxOutputBytes
	if s.Code != nil {
		if v := s.Code.TimeoutMS; v != nil && *v > 0 {
			timeoutMS = min(*v, maxCodeTimeoutMS)
		}
		if v := s.Code.MaxCalls; v != nil && *v > 0 {
			maxCalls = min(*v, maxCodeMaxCalls)
		}
		if v := s.Code.MaxOutputBytes; v != nil && *v > 0 {
			maxOutput = min(*v, maxCodeMaxOutputBytes)
		}
	}
	return timeoutMS, maxCalls, maxOutput
}

// mergeCode folds in over cur. From the project layer only an explicit false
// for Enabled and a value below the current effective one take effect.
func mergeCode(cur, in *CodeSettings, project bool) *CodeSettings {
	if in == nil {
		return cur
	}
	out := &CodeSettings{}
	if cur != nil {
		*out = *cur
	}
	if in.Enabled != nil && !(project && *in.Enabled) {
		b := *in.Enabled
		out.Enabled = &b
	}
	lower := func(dst **int, v *int, def int) {
		if v == nil || *v <= 0 {
			return
		}
		if project {
			eff := def
			if *dst != nil {
				eff = **dst
			}
			if *v >= eff {
				return
			}
		}
		n := *v
		*dst = &n
	}
	lower(&out.TimeoutMS, in.TimeoutMS, DefaultCodeTimeoutMS)
	lower(&out.MaxCalls, in.MaxCalls, DefaultCodeMaxCalls)
	lower(&out.MaxOutputBytes, in.MaxOutputBytes, DefaultCodeMaxOutputBytes)
	return out
}
