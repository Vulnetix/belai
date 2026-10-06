package tools

import (
	"github.com/vulnetix/belai/internal/wire"
)

// propertySchema converts one Property into its JSON-schema form, recursing
// into array items and object properties so a nested shape (the entries of a
// todo list, the questions of AskUserQuestion) reaches the model. Without the
// recursion a model sees a bare array and invents the entry fields.
func propertySchema(v Property) map[string]any {
	p := map[string]any{"type": v.Type, "description": v.Description}
	if len(v.Enum) > 0 {
		p["enum"] = v.Enum
	}
	if v.Items != nil {
		p["items"] = propertySchema(*v.Items)
	}
	if len(v.Properties) > 0 {
		props := make(map[string]any, len(v.Properties))
		for k, c := range v.Properties {
			props[k] = propertySchema(c)
		}
		p["properties"] = props
	}
	if len(v.Required) > 0 {
		p["required"] = v.Required
	}
	return p
}

// Schema converts a Definition into an OpenAI-compatible function schema.
func (d Definition) Schema() map[string]any {
	props := make(map[string]any, len(d.Properties))
	for k, v := range d.Properties {
		props[k] = propertySchema(v)
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
