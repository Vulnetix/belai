package wire

// Kiro (AWS CodeWhisperer / Q Developer) generateAssistantResponse shapes.
// The service has no system role and no tool role: the system prompt rides on
// the first user message, and tool results ride on the user message that
// follows the assistant's tool uses. History must alternate user and
// assistant, and the newest user message is currentMessage.

// KiroPath is the generateAssistantResponse operation path.
const KiroPath = "/generateAssistantResponse"

// KiroOrigin is the client origin Kiro's editor sends.
const KiroOrigin = "AI_EDITOR"

// KiroRequest is the generateAssistantResponse body.
type KiroRequest struct {
	ConversationState KiroConversationState `json:"conversationState"`
	ProfileArn        string                `json:"profileArn,omitempty"`
	// AdditionalModelRequestFields carries model-specific fields (effort,
	// max_tokens). The service validates it against the model's published
	// schema, so it holds only what that schema declares.
	AdditionalModelRequestFields map[string]any `json:"additionalModelRequestFields,omitempty"`
}

// KiroConversationState carries the conversation.
type KiroConversationState struct {
	ChatTriggerType string             `json:"chatTriggerType"`
	ConversationID  string             `json:"conversationId"`
	CurrentMessage  KiroHistoryEntry   `json:"currentMessage"`
	History         []KiroHistoryEntry `json:"history,omitempty"`
}

// KiroHistoryEntry is exactly one of a user or an assistant message.
type KiroHistoryEntry struct {
	UserInputMessage         *KiroUserInputMessage         `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *KiroAssistantResponseMessage `json:"assistantResponseMessage,omitempty"`
}

// KiroUserInputMessage is a user turn.
type KiroUserInputMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId,omitempty"`
	Origin                  string                   `json:"origin,omitempty"`
	UserInputMessageContext *KiroUserInputMessageCtx `json:"userInputMessageContext,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
}

// KiroImage is one image on a user message.
type KiroImage struct {
	Format string          `json:"format"` // png, jpeg, gif or webp
	Source KiroImageSource `json:"source"`
}

// KiroImageSource holds the image bytes; encoding/json writes a []byte as
// base64, which is the wire form.
type KiroImageSource struct {
	Bytes []byte `json:"bytes"`
}

// KiroUserInputMessageCtx carries tool results and tool definitions.
type KiroUserInputMessageCtx struct {
	ToolResults []KiroToolResult `json:"toolResults,omitempty"`
	Tools       []KiroTool       `json:"tools,omitempty"`
}

// KiroToolResult answers one tool use.
type KiroToolResult struct {
	ToolUseID string            `json:"toolUseId"`
	Content   []KiroTextContent `json:"content"`
	Status    string            `json:"status"`
}

// KiroTextContent is one text item of a tool result.
type KiroTextContent struct {
	Text string `json:"text"`
}

// KiroTool is one advertised tool.
type KiroTool struct {
	ToolSpecification KiroToolSpecification `json:"toolSpecification"`
}

// KiroToolSpecification names a tool and its JSON schema.
type KiroToolSpecification struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema KiroInputSchema `json:"inputSchema"`
}

// KiroInputSchema wraps the JSON schema.
type KiroInputSchema struct {
	JSON map[string]any `json:"json"`
}

// KiroAssistantResponseMessage is an assistant turn.
type KiroAssistantResponseMessage struct {
	Content  string        `json:"content"`
	ToolUses []KiroToolUse `json:"toolUses,omitempty"`
}

// KiroToolUse is one tool call the assistant made.
type KiroToolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
}

// KiroAssistantResponseEvent is an assistantResponseEvent payload.
type KiroAssistantResponseEvent struct {
	Content string `json:"content"`
}

// KiroToolUseEvent is a toolUseEvent payload: an input fragment for toolUseId,
// with Stop set on the call's last fragment.
type KiroToolUseEvent struct {
	ToolUseID string `json:"toolUseId"`
	Name      string `json:"name"`
	Input     any    `json:"input"`
	Stop      bool   `json:"stop"`
}

// KiroMetadataEvent is a metadataEvent payload. Field names vary between
// service versions, so every counter is optional.
type KiroMetadataEvent struct {
	TokenUsage *KiroTokenUsage `json:"tokenUsage"`
}

// KiroTokenUsage is the token accounting of one response.
type KiroTokenUsage struct {
	UncachedInputTokens   int `json:"uncachedInputTokens"`
	InputTokens           int `json:"inputTokens"`
	CacheReadInputTokens  int `json:"cacheReadInputTokens"`
	CacheWriteInputTokens int `json:"cacheWriteInputTokens"`
	OutputTokens          int `json:"outputTokens"`
	TotalTokens           int `json:"totalTokens"`
}

// KiroContextUsageEvent is a contextUsageEvent payload.
type KiroContextUsageEvent struct {
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
}

// KiroExceptionPayload is the body of an exception or error frame.
type KiroExceptionPayload struct {
	Message  string `json:"message"`
	MessageU string `json:"Message"`
}
