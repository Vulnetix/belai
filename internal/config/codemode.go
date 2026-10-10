package config

import (
	"fmt"

	"github.com/vulnetix/belai/internal/provider"
)

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
	// Model picks the model code-mode turns run on. nil means the session's
	// main model (Smart).
	Model *CodeModel `json:"model,omitempty"`
}

// Code-mode model tiers. CodeTierSmart is the session's main model and
// CodeTierFast the resolved fast tier (routing.fast_model, else the provider's
// registry fast model).
const (
	CodeTierSmart = "smart"
	CodeTierFast  = "fast"
)

// CodeModel is a code-mode model pick: either a Tier, or an explicit
// Provider/Model pair from any available provider, never both. A Provider with
// no Model uses that provider's default model; a Model with no Provider uses
// the main provider.
type CodeModel struct {
	Tier     string `json:"tier,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Explicit reports whether the pick names a provider and/or model.
func (m *CodeModel) Explicit() bool {
	return m != nil && (m.Provider != "" || m.Model != "")
}

// ValidateCodeModel validates code.model: a tier xor a provider/model pair.
func ValidateCodeModel(s Settings) error {
	if s.Code == nil || s.Code.Model == nil {
		return nil
	}
	m := s.Code.Model
	switch m.Tier {
	case "", CodeTierSmart, CodeTierFast:
	default:
		return fmt.Errorf("code.model.tier %q is invalid (want %q or %q)", m.Tier, CodeTierSmart, CodeTierFast)
	}
	if m.Tier != "" && m.Explicit() {
		return fmt.Errorf("code.model sets tier or provider/model, not both")
	}
	if m.Tier == "" && !m.Explicit() {
		return fmt.Errorf("code.model must set tier, or provider and/or model")
	}
	if m.Provider != "" && !provider.Builtin(m.Provider) && !provider.ValidCustomName(m.Provider) {
		return fmt.Errorf("code.model: invalid provider %q", m.Provider)
	}
	return nil
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
	// The model is a preference, not a limit: every layer may set it.
	if in.Model != nil {
		m := *in.Model
		out.Model = &m
	}
	return out
}
