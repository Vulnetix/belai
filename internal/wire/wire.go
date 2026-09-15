// Package wire defines the HTTP request/response shapes for the three
// provider surfaces the ai-firewall gateway relays: OpenAI chat/completions,
// OpenAI responses, and Anthropic messages. It keeps the "shape" layer free of
// any network I/O so it can be unit-tested in isolation.
package wire

import (
	"net/http"
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

// AuthHeaders returns the provider-specific authentication headers.
// OpenAI-style surfaces use a Bearer token; Anthropic uses x-api-key plus an
// anthropic-version header.
func AuthHeaders(s Surface, apiKey string) map[string]string {
	h := map[string]string{"content-type": "application/json"}
	if s == SurfaceAnthropicMessages {
		h["x-api-key"] = apiKey
		h["anthropic-version"] = "2023-06-01"
	} else {
		h["authorization"] = "Bearer " + apiKey
	}
	return h
}

// ---------------------------------------------------------------------------
// OpenAI chat/completions
// ---------------------------------------------------------------------------

// OpenAIChatMessage is a single message in a chat/completions request.
type OpenAIChatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
}

// OpenAIChatRequest is the body of a chat/completions call.
type OpenAIChatRequest struct {
	Model       string              `json:"model"`
	Messages    []OpenAIChatMessage `json:"messages"`
	Stream      bool                `json:"stream,omitempty"`
	Temperature *float64            `json:"temperature,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Tools       []OpenAITool        `json:"tools,omitempty"`
	ToolChoice  string              `json:"tool_choice,omitempty"`
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
// chat/completions response.
type OpenAIChatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
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

// AnthropicMessagesRequest is the body of a messages call.
type AnthropicMessagesRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []AnthropicMessage `json:"messages"`
	Stream    bool               `json:"stream,omitempty"`
	Tools     []AnthropicToolDef `json:"tools,omitempty"`
	ToolChoice string            `json:"tool_choice,omitempty"`
}

// AnthropicContentBlock is a content block in a messages response.
type AnthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// AnthropicMessagesResponse is the non-streaming messages response.
type AnthropicMessagesResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []AnthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
}

// AnthropicStreamEvent is one SSE `data:` payload in a streaming messages
// response.
type AnthropicStreamEvent struct {
	Type  string `json:"type"`
	Index *int   `json:"index,omitempty"`
	Delta struct {
		Type       string `json:"type,omitempty"`
		Text       string `json:"text,omitempty"`
		StopReason string `json:"stop_reason,omitempty"`
	} `json:"delta"`
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
	} `json:"result"`
	Success bool             `json:"success"`
	Errors  []WorkersAIError `json:"errors"`
}

// ApplyAuthHeader applies the surface's auth header to an outgoing request.
func ApplyAuthHeader(req *http.Request, s Surface, apiKey string) {
	if s == SurfaceAnthropicMessages {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("authorization", "Bearer "+apiKey)
	}
}
