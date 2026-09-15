package wire

import "encoding/json"

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
