// Package models is the provider model catalog: the list of selectable model
// ids per provider, their effort levels, and the thinking-budget mapping.
package models

import (
	"strings"

	"github.com/vulnetix/belai/internal/provider"
)

// Model is one selectable model in a provider's catalog.
type Model struct {
	ID            string
	Label         string
	Efforts       []string
	ContextWindow int // 0 when unknown
	MaxOutput     int // completion ceiling in tokens; 0 when unknown
	Thinking      ThinkingStyle
}

// defaultEfforts is the effort set used by providers that expose all three
// levels. Empty effort means "provider default".
var defaultEfforts = []string{"low", "medium", "high"}

// DefaultEfforts returns the standard effort set for built-in providers.
func DefaultEfforts() []string { return defaultEfforts }

// Catalog returns the selectable models for a provider, in display order.
func Catalog(providerName string) []Model {
	if d, ok := provider.Lookup(providerName); ok {
		if len(d.Models) == 0 {
			return nil
		}
		out := make([]Model, len(d.Models))
		for i, m := range d.Models {
			out[i] = Model{
				ID:            m.ID,
				Label:         m.Label,
				Efforts:       m.Efforts,
				ContextWindow: m.ContextWindow,
				MaxOutput:     m.MaxOutput,
				Thinking:      m.Thinking,
			}
			if len(out[i].Efforts) == 0 {
				out[i].Efforts = defaultEfforts
			}
		}
		return out
	}
	// Unknown names are custom providers; their catalogue comes from the
	// profile, never the OpenAI list.
	return nil
}

// Efforts returns the effort levels for a model, falling back to the default
// set when the model is not in the catalog.
func Efforts(provider, modelID string) []string {
	for _, m := range Catalog(provider) {
		if m.ID == modelID {
			return m.Efforts
		}
	}
	return defaultEfforts
}

// ThinkingBudget maps an effort level to an Anthropic thinking.budget_tokens
// value. An empty or unknown effort returns 0 (provider default).
func ThinkingBudget(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 16384
	default:
		return 0
	}
}

// Label returns the human-facing label for a model id, or the id itself when
// the model is not in the catalog.
func Label(provider, modelID string) string {
	for _, m := range Catalog(provider) {
		if m.ID == modelID {
			return m.Label
		}
	}
	return modelID
}

// ContextWindowFor returns the declared context window for a model in the
// static catalog, or 0 when the model is unknown there.
func ContextWindowFor(provider, modelID string) int {
	for _, m := range Catalog(provider) {
		if m.ID == modelID {
			return m.ContextWindow
		}
	}
	return 0
}
