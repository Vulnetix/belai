package models

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/vulnetix/signet/internal/provider"
)

// ThinkingStyle re-exports the provider-owned thinking shape so callers that
// only need the catalogue do not import the registry.
type ThinkingStyle = provider.ThinkingStyle

// Thinking styles; see provider.ThinkingStyle.
const (
	StyleNone     = provider.ThinkingNone
	StyleBudget   = provider.ThinkingBudget
	StyleAdaptive = provider.ThinkingAdaptive
	StyleAlways   = provider.ThinkingAlways
)

// MaxOutput returns the model's completion ceiling in tokens: the static
// catalogue first, then the Claude id rules (live-fetched and gateway-routed
// Claude ids are not in any catalogue), else 0 for unknown.
func MaxOutput(providerName, modelID string) int {
	if m, ok := catalogEntry(providerName, modelID); ok && m.MaxOutput > 0 {
		return m.MaxOutput
	}
	if t, ok := claudeTraits(modelID); ok {
		return t.maxOutput
	}
	return 0
}

// CatalogMaxOutput is MaxOutput without the id rules: the static catalogue
// entry for this exact provider, or 0. A relay (OpenRouter, Copilot, a
// gateway) may cap a model below its vendor's ceiling, so only a catalogue
// entry written for that provider is trusted there.
func CatalogMaxOutput(providerName, modelID string) int {
	if m, ok := catalogEntry(providerName, modelID); ok {
		return m.MaxOutput
	}
	return 0
}

// Thinking returns the extended-thinking request shape the model accepts:
// the static catalogue first, then the Claude id rules, else StyleNone.
func Thinking(providerName, modelID string) ThinkingStyle {
	if m, ok := catalogEntry(providerName, modelID); ok && m.Thinking != StyleNone {
		return m.Thinking
	}
	if t, ok := claudeTraits(modelID); ok {
		return t.thinking
	}
	return StyleNone
}

func catalogEntry(providerName, modelID string) (Model, bool) {
	for _, m := range Catalog(providerName) {
		if m.ID == modelID {
			return m, true
		}
	}
	return Model{}, false
}

type traits struct {
	maxOutput int
	thinking  ThinkingStyle
}

var (
	// claude-opus-4-5, claude-sonnet-5, claude-opus-4-20250514, claude-fable-5-1
	claudeNew = regexp.MustCompile(`claude-(opus|sonnet|haiku|fable)-(\d+)(?:[-.](\d+))?`)
	// claude-3-5-sonnet-20241022, claude-3-opus-20240229
	claudeOld = regexp.MustCompile(`claude-(\d+)(?:[-.](\d+))?-(opus|sonnet|haiku)`)
)

// claudeTraits derives output ceiling and thinking shape from a Claude model
// id. Provider prefixes ("anthropic/claude-…") and date suffixes are
// tolerated. An eight-digit date in the minor slot is a snapshot date, not a
// minor version.
func claudeTraits(id string) (traits, bool) {
	id = strings.ToLower(id)
	if m := claudeOld.FindStringSubmatch(id); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor := minorOf(m[2])
		switch {
		case major == 3 && minor >= 7:
			return traits{64000, StyleBudget}, true
		case major == 3 && minor >= 5:
			return traits{8192, StyleNone}, true
		default:
			return traits{4096, StyleNone}, true
		}
	}
	m := claudeNew.FindStringSubmatch(id)
	if m == nil {
		return traits{}, false
	}
	family := m[1]
	major, _ := strconv.Atoi(m[2])
	minor := minorOf(m[3])
	switch {
	case family == "fable":
		return traits{128000, StyleAlways}, true
	case major >= 6:
		return traits{128000, StyleAlways}, true
	case major == 5 && family == "opus" && minor >= 5:
		return traits{128000, StyleAlways}, true
	case major == 5:
		return traits{64000, StyleAdaptive}, true
	case major == 4 && minor >= 6:
		if family == "opus" {
			return traits{128000, StyleAdaptive}, true
		}
		return traits{64000, StyleAdaptive}, true
	case major == 4 && family == "opus" && minor < 5:
		// Opus 4 and 4.1 cap output at 32k.
		return traits{32000, StyleBudget}, true
	case major == 4:
		return traits{64000, StyleBudget}, true
	}
	return traits{}, false
}

// minorOf parses a minor version slot, treating an absent slot or a snapshot
// date as minor 0.
func minorOf(s string) int {
	if s == "" || len(s) > 2 {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}
