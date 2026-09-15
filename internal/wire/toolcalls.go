// Copyright 2025 Vulnetix. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ToolMethod controls how OpenAI-style tool-call arguments are serialised on
// the wire: as a JSON-encoded string (classic OpenAI), as a raw JSON object
// (Workers AI and some gateways), or as Anthropic content blocks (no
// function.arguments field). ToolMethodNone means "not yet detected".
type ToolMethod string

const (
	ToolMethodNone   ToolMethod = ""
	ToolMethodString ToolMethod = "string"
	ToolMethodObject ToolMethod = "object"
	ToolMethodBlocks ToolMethod = "blocks"
)

// ToolCallFunction is the function half of an OpenAI tool call. Arguments is
// a raw JSON value so the request path can choose string vs object form.
type ToolCallFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// NewObjectToolCallArgs parses argsJSON and returns it as a raw JSON object.
// A malformed args string fails closed to an empty object.
func NewObjectToolCallArgs(argsJSON string) json.RawMessage {
	var raw json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &raw); err == nil && len(raw) > 0 && raw[0] == '{' {
		return raw
	}
	return json.RawMessage("{}")
}

// NewStringToolCallArgs wraps argsJSON as a JSON-encoded string.
func NewStringToolCallArgs(argsJSON string) json.RawMessage {
	b, _ := json.Marshal(argsJSON)
	return b
}

// OpenAITool is the wire shape for a tool offered in an OpenAI chat
// /completions or responses request.
type OpenAITool struct {
	Type     string            `json:"type"`
	Function OpenAIFunctionDef `json:"function"`
}

// OpenAIFunctionDef is the function metadata inside an OpenAI tool
type OpenAIFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// OpenAIToolCall is a model-emitted tool invocation inside an assistant
// message. The Arguments field is a raw JSON value; on the request path it is
// built via NewStringToolCallArgs or NewObjectToolCallArgs, and on the
// response path it may arrive as either form.
type OpenAIToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// AnthropicToolDef is the wire shape for a tool offered in an Anthropic
// messages request.
type AnthropicToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// AnthropicRequestBlock is one content block in an Anthropic message.
// For text turns it contains just Text; for tool_use blocks it contains
// ID, Name, and Input; for tool_result blocks it contains ToolUseID and
// Content.
type AnthropicRequestBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

// CanonicalToolArgs converts raw wire bytes into canonical JSON text.
// Accepts both tool call argument shapes: a JSON string whose content is
// JSON text (OpenAI), and a bare JSON object/array (Workers AI).
// Null or absent arguments yield "".
func CanonicalToolArgs(raw json.RawMessage) (string, error) {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || string(b) == "null" {
		return "", nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	if b[0] == '{' || b[0] == '[' {
		if !json.Valid(b) {
			return "", fmt.Errorf("tool arguments are not a JSON object or array: %q", b)
		}
		return string(b), nil
	}
	return "", fmt.Errorf("tool arguments are neither a JSON string nor a JSON object: %q", b)
}

// ToolArgsFragment returns the content fragment of one streamed tool-call
// arguments delta. String-shaped fragments have their JSON quoting and
// escaping removed so fragments concatenate into canonical JSON text;
// object-shaped payloads (providers that stream whole objects) pass through
// verbatim.
func ToolArgsFragment(raw json.RawMessage) string {
	b := bytes.TrimSpace(raw)
	if len(b) == 0 || string(b) == "null" {
		return ""
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return ""
		}
		return s
	}
	return string(b)
}
