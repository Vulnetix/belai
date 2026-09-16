package tools

import (
	"github.com/vulnetix/signet/internal/wire"
)

// Schema converts a Definition into an OpenAI-compatible function schema.
func (d Definition) Schema() map[string]any {
	props := make(map[string]any, len(d.Properties))
	for k, v := range d.Properties {
		props[k] = map[string]any{"type": v.Type, "description": v.Description}
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(d.Required) > 0 {
		out["required"] = d.Required
	}
	return out
}

// OpenAITool converts a Definition into a wire.OpenAITool.
func (d Definition) OpenAITool() wire.OpenAITool {
	return wire.OpenAITool{
		Type: "function",
		Function: wire.OpenAIFunctionDef{
			Name:        d.Name,
			Description: d.Description,
			Parameters:  d.Schema(),
		},
	}
}

// AnthropicTool converts a Definition into a wire.AnthropicToolDef.
func (d Definition) AnthropicTool() wire.AnthropicToolDef {
	return wire.AnthropicToolDef{
		Name:        d.Name,
		Description: d.Description,
		InputSchema: d.Schema(),
	}
}
