// Package wire defines the HTTP request/response shapes for the three
// provider surfaces the ai-firewall gateway relays: OpenAI chat/completions,
// OpenAI responses, and Anthropic messages. It keeps the "shape" layer free of
// any network I/O so it can be unit-tested in isolation.
package wire

import (
	"strings"
)

// Surface identifies one of the three relayed provider surfaces.
type Surface string

const (
	SurfaceOpenAIChat        Surface = "openai-chat"
	SurfaceOpenAIResponses   Surface = "openai-responses"
	SurfaceAnthropicMessages Surface = "anthropic-messages"
)

// Path returns the URL path appended to a base URL for this surface.
// OpenAI base URLs already carry the /v1 prefix; Anthropic base URLs do not.
func (s Surface) Path() string {
	switch s {
	case SurfaceOpenAIChat:
		return "/chat/completions"
	case SurfaceOpenAIResponses:
		return "/responses"
	case SurfaceAnthropicMessages:
		return "/v1/messages"
	default:
		return ""
	}
}

// BuildURL joins a base URL with a surface path, tolerating a trailing slash
// on the base URL.
func BuildURL(baseURL string, s Surface) string {
	return strings.TrimRight(baseURL, "/") + s.Path()
}

// ---------------------------------------------------------------------------
// OpenAI chat/completions
// ---------------------------------------------------------------------------

// OpenAIChatMessage is a single message in a chat/completions request.
type OpenAIChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// OpenAIChatRequest is the body of a chat/completions call.
type OpenAIChatRequest struct {
	Model           string               `json:"model"`
	Messages        []OpenAIChatMessage  `json:"messages"`
	Stream          bool                 `json:"stream,omitempty"`
	Temperature     *float64             `json:"temperature,omitempty"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	Tools           []OpenAITool         `json:"tools,omitempty"`
	ToolChoice      string               `json:"tool_choice,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	StreamOptions   *OpenAIStreamOptions `json:"stream_options,omitempty"`
}

// OpenAIStreamOptions requests usage accounting on a streamed response.
type OpenAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// OpenAIChatResponse is the non-streaming chat/completions response.
type OpenAIChatResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Choices []OpenAIChatChoice `json:"choices"`
	Usage   OpenAIChatUsage    `json:"usage"`
}

// OpenAIChatChoice is one completion choice.
type OpenAIChatChoice struct {
	Index        int               `json:"index"`
	Message      OpenAIChatMessage `json:"message"`
	FinishReason string            `json:"finish_reason"`
}

// OpenAIChatUsage reports token usage.
type OpenAIChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIChatStreamChunk is one SSE `data:` payload in a streaming
// chat/completions response. The final usage chunk has an empty choices array
// and carries usage in the Usage field.
type OpenAIChatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string                `json:"role,omitempty"`
			Content   string                `json:"content,omitempty"`
			ToolCalls []OpenAIToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Usage *OpenAIChatUsage `json:"usage,omitempty"`
}

// OpenAIToolCallDelta is one tool-call fragment in an OpenAI stream delta.
// Arguments arrive as string fragments and are assembled by index.
type OpenAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// ---------------------------------------------------------------------------
// OpenAI responses
// ---------------------------------------------------------------------------

// OpenAIResponsesRequest is the body of a responses call.
type OpenAIResponsesRequest struct {
	Model  string `json:"model"`
	Input  any    `json:"input"`
	Stream bool   `json:"stream,omitempty"`
}

// OpenAIResponseItem is an element of the response `output` array.
type OpenAIResponseItem struct {
	Type    string                  `json:"type"`
	Text    string                  `json:"text,omitempty"`
	Content []OpenAIResponseContent `json:"content,omitempty"`
	Role    string                  `json:"role,omitempty"`
}

// OpenAIResponseContent is a content block inside an output item.
type OpenAIResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// OpenAIResponsesResponse is the non-streaming responses response.
type OpenAIResponsesResponse struct {
	ID     string               `json:"id"`
	Object string               `json:"object"`
	Output []OpenAIResponseItem `json:"output"`
}

// OpenAIResponsesStreamEvent is one SSE `data:` payload in a streaming
// responses response.
type OpenAIResponsesStreamEvent struct {
	Type  string              `json:"type"`
	Delta string              `json:"delta,omitempty"`
	Text  string              `json:"text,omitempty"`
	Item  *OpenAIResponseItem `json:"item,omitempty"`
}

// ---------------------------------------------------------------------------
// Anthropic messages
// ---------------------------------------------------------------------------

// AnthropicMessage is a single message in a messages request.
type AnthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// NewAnthropicTextMessage builds an AnthropicMessage whose Content is a plain
// string, preserving wire compatibility with pre-tool callers.
func NewAnthropicTextMessage(role, text string) AnthropicMessage {
	return AnthropicMessage{Role: role, Content: text}
}

// NewAnthropicBlockMessage builds an AnthropicMessage whose Content is an
// array of blocks (text, tool_use, or tool_result).
func NewAnthropicBlockMessage(role string, blocks []AnthropicRequestBlock) AnthropicMessage {
	return AnthropicMessage{Role: role, Content: blocks}
}

// AnthropicMessagesRequest is the body of a messages call.
type AnthropicMessagesRequest struct {
	Model      string             `json:"model"`
	MaxTokens  int                `json:"max_tokens"`
	System     string             `json:"system,omitempty"`
	Messages   []AnthropicMessage `json:"messages"`
	Stream     bool               `json:"stream,omitempty"`
	Tools      []AnthropicToolDef `json:"tools,omitempty"`
	ToolChoice string             `json:"tool_choice,omitempty"`
	Thinking   *AnthropicThinking `json:"thinking,omitempty"`
}

// AnthropicThinking enables extended thinking with a token budget.
type AnthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// AnthropicContentBlock is a content block in a messages response.
type AnthropicContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// AnthropicMessagesResponse is the non-streaming messages response.
type AnthropicMessagesResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []AnthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      AnthropicUsage          `json:"usage"`
}

// AnthropicUsage reports token usage on a messages response. Cache tokens
// occupy the window, so the cache read/creation inputs are part of the prompt
// total when mapped onto transcript.Usage.
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// AnthropicStreamEvent is one SSE `data:` payload in a streaming messages
// response. message_start carries input tokens and message_delta carries
// output tokens; both describe one response. content_block_start carries a
// tool_use block's id/name, and content_block_delta carries text_delta or
// input_json_delta fragments.
type AnthropicStreamEvent struct {
	Type  string `json:"type"`
	Index *int   `json:"index,omitempty"`
	Delta struct {
		Type        string `json:"type,omitempty"`
		Text        string `json:"text,omitempty"`
		StopReason  string `json:"stop_reason,omitempty"`
		PartialJSON string `json:"partial_json,omitempty"`
	} `json:"delta"`
	ContentBlock *struct {
		Type string `json:"type,omitempty"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block,omitempty"`
	Message *struct {
		Usage *AnthropicUsage `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Usage *AnthropicUsage `json:"usage,omitempty"`
}

// ---------------------------------------------------------------------------
// Cloudflare Workers AI
// ---------------------------------------------------------------------------

// WorkersAIRequest is the body of a Workers AI /ai/run/{model} call.
type WorkersAIRequest struct {
	Messages  []OpenAIChatMessage `json:"messages"`
	Stream    bool                `json:"stream,omitempty"`
	MaxTokens int                 `json:"max_tokens,omitempty"`
	Tools     []OpenAITool        `json:"tools,omitempty"`
}

// WorkersAIError is a single Workers AI error entry.
type WorkersAIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// WorkersAIResponse is the non-streaming Workers AI response. Chat models
// return result.choices (OpenAI-compatible); text-generation models return
// result.response.
type WorkersAIResponse struct {
	Result struct {
		Response string             `json:"response"`
		Choices  []OpenAIChatChoice `json:"choices"`
		Usage    *OpenAIChatUsage   `json:"usage,omitempty"`
	} `json:"result"`
	Success bool             `json:"success"`
	Errors  []WorkersAIError `json:"errors"`
}
