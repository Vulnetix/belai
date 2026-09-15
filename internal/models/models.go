// Package models is the provider model catalog: the list of selectable model
// ids per provider, their effort levels, and the thinking-budget mapping.
package models

import "strings"

// Model is one selectable model in a provider's catalog.
type Model struct {
	ID      string
	Label   string
	Efforts []string
}

// defaultEfforts is the effort set used by providers that expose all three
// levels. Empty effort means "provider default".
var defaultEfforts = []string{"low", "medium", "high"}

// Catalog returns the selectable models for a provider, in display order.
func Catalog(provider string) []Model {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return []Model{
			{ID: "claude-opus-4-5", Label: "Claude Opus 4.5", Efforts: defaultEfforts},
			{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5", Efforts: defaultEfforts},
			{ID: "claude-haiku-4-5", Label: "Claude Haiku 4.5", Efforts: defaultEfforts},
		}
	case "cloudflare-workers-ai":
		return []Model{
			{ID: "@cf/moonshotai/kimi-k2.6", Label: "Kimi K2.6", Efforts: defaultEfforts},
			{ID: "@cf/openai/gpt-oss-120b", Label: "GPT-OSS 120B", Efforts: defaultEfforts},
			{ID: "@cf/meta/llama-4-scout-17b-16e-instruct", Label: "Llama 4 Scout", Efforts: defaultEfforts},
			{ID: "@cf/qwen/qwen3-30b-a3b-fp8", Label: "Qwen3 30B", Efforts: defaultEfforts},
		}
	case "cloudflare-ai-gateway":
		return []Model{
			{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5", Efforts: defaultEfforts},
			{ID: "claude-opus-4-5", Label: "Claude Opus 4.5", Efforts: defaultEfforts},
			{ID: "gpt-5", Label: "GPT-5", Efforts: defaultEfforts},
		}
	case "openai":
		return []Model{
			{ID: "gpt-5", Label: "GPT-5", Efforts: defaultEfforts},
			{ID: "gpt-5-mini", Label: "GPT-5 Mini", Efforts: defaultEfforts},
			{ID: "gpt-4.1", Label: "GPT-4.1", Efforts: defaultEfforts},
		}
	default:
		// Unknown names are custom providers; their catalogue comes from the
		// profile, never the OpenAI list.
		return nil
	}
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
