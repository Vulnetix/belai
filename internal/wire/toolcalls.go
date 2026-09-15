package wire

// OpenAITool is the wire shape for a tool offered in an OpenAI chat
// /completions or responses request.
type OpenAITool struct {
	Type     string           `json:"type"`
	Function OpenAIFunctionDef `json:"function"`
}

// OpenAIFunctionDef is the function metadata inside an OpenAI tool
type OpenAIFunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// OpenAIToolCall is a model-emitted tool invocation inside an assistant
// message. The Arguments field is a JSON *string* requiring a second unmarshal
to access the typed map.
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
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
// ID, Name, and Input.
type AnthropicRequestBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}
