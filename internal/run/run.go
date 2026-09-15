// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, run the Role
// Manager pipeline over the user prompt, and return the completion text.
package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/transcript"
	"github.com/vulnetix/signet/internal/wire"
)

// Config is a resolved provider + model + credentials.
type Config struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Effort   string        // empty means provider default thinking level
	API      wire.Surface  // empty for built-ins; custom providers carry their surface
	Auth     provider.Auth // empty for built-ins; custom providers carry their auth style
}

func (c Config) String() string {
	return fmt.Sprintf("{Provider:%s BaseURL:%s APIKey:<redacted> Model:%s}", c.Provider, c.BaseURL, c.Model)
}

func (c Config) GoString() string {
	return fmt.Sprintf("run.Config{Provider:%q, BaseURL:%q, APIKey:%q, Model:%q}", c.Provider, c.BaseURL, "<redacted>", c.Model)
}

// Turn is one message in a multi-turn conversation.
type Turn struct {
	Role       string // "user" | "assistant" | "tool"
	Content    string
	ToolCalls  []rolemanager.ToolCall
	ToolCallID string
	ToolName   string
}

// ErrNotConfigured is returned when provider credentials are missing.
var ErrNotConfigured = errors.New("provider credentials not configured")

// NotConfiguredError carries the details of a missing credential.
type NotConfiguredError struct {
	Provider string
	Missing  []string
	EnvHints []string
	Searched []string
}

func (e *NotConfiguredError) Error() string {
	return fmt.Sprintf("%s requires %s (looked in: %s)", e.Provider, strings.Join(e.EnvHints, ", "), strings.Join(e.Searched, ", "))
}

func (e *NotConfiguredError) Is(target error) bool { return target == ErrNotConfigured }

// CredentialSource resolves one provider field.
type CredentialSource interface {
	Lookup(provider, field string) (value, origin string, ok bool)
}

// EnvSource adapts an environment-lookup function to CredentialSource.
type EnvSource func(string) string

// Lookup implements CredentialSource.
func (f EnvSource) Lookup(provider, field string) (value, origin string, ok bool) {
	key := provider + ":" + field
	switch key {
	case "openai:api_key":
		if v := f("OPENAI_API_KEY"); v != "" {
			return v, "$OPENAI_API_KEY", true
		}
	case "anthropic:api_key":
		if v := f("ANTHROPIC_API_KEY"); v != "" {
			return v, "$ANTHROPIC_API_KEY", true
		}
	case "cloudflare-workers-ai:api_key":
		if v := f("CLOUDFLARE_API_KEY"); v != "" {
			return v, "$CLOUDFLARE_API_KEY", true
		}
	case "cloudflare-workers-ai:account_id":
		if v := f("CLOUDFLARE_ACCOUNT_ID"); v != "" {
			return v, "$CLOUDFLARE_ACCOUNT_ID", true
		}
	case "cloudflare-ai-gateway:api_key":
		if v := f("CLOUDFLARE_API_KEY"); v != "" {
			return v, "$CLOUDFLARE_API_KEY", true
		}
	case "cloudflare-ai-gateway:account_id":
		if v := f("CLOUDFLARE_ACCOUNT_ID"); v != "" {
			return v, "$CLOUDFLARE_ACCOUNT_ID", true
		}
	case "cloudflare-ai-gateway:gateway_id":
		if v := f("CLOUDFLARE_GATEWAY_ID"); v != "" {
			return v, "$CLOUDFLARE_GATEWAY_ID", true
		}
	}
	return "", "", false
}

// Status reports how a provider's credentials resolved.
type Status struct {
	Configured bool
	Missing    []string
	Origins    map[string]string // field -> origin description
}

// DefaultModel returns a sensible model for a provider when none is given.
func DefaultModel(providerName string) string {
	switch providerName {
	case "cloudflare-workers-ai":
		return "@cf/moonshotai/kimi-k2.6"
	case "cloudflare-ai-gateway":
		return "claude-sonnet-4-5"
	case "anthropic":
		return "claude-opus-4-5"
	default:
		return "gpt-5"
	}
}

func normalizeProvider(providerName string) string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		return "openai"
	}
	return name
}

// Prepare resolves a provider configuration from a CredentialSource.
func Prepare(model, providerName string, src CredentialSource) (Config, Status) {
	name := normalizeProvider(providerName)
	if model == "" {
		model = DefaultModel(name)
	}

	cfg := Config{Provider: name, Model: model}
	status := Status{Origins: map[string]string{}}

	switch name {
	case "cloudflare-workers-ai":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		if acct, origin, ok := src.Lookup(name, "account_id"); ok {
			cfg.BaseURL = "https://api.cloudflare.com/client/v4/accounts/" + acct
			status.Origins["account_id"] = origin
		} else {
			status.Missing = append(status.Missing, "account_id")
		}
	case "cloudflare-ai-gateway":
		var acct string
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		if a, origin, ok := src.Lookup(name, "account_id"); ok {
			acct = a
			status.Origins["account_id"] = origin
		} else {
			status.Missing = append(status.Missing, "account_id")
		}
		if gw, origin, ok := src.Lookup(name, "gateway_id"); ok {
			cfg.BaseURL = "https://gateway.ai.cloudflare.com/v1/" + acct + "/" + gw
			status.Origins["gateway_id"] = origin
		} else {
			status.Missing = append(status.Missing, "gateway_id")
		}
	case "anthropic":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://api.anthropic.com"
	default:
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://api.openai.com/v1"
	}

	status.Configured = len(status.Missing) == 0
	if override := strings.TrimSpace(os.Getenv("SIGNET_BASE_URL")); override != "" {
		cfg.BaseURL = override
	}
	return cfg, status
}

// Resolve reads provider configuration from environment variables, mirroring
// Pi's resolution order: explicit provider flag, then SIGNET_PROVIDER, then
// PI_PROVIDER, then a default of openai. SIGNET_BASE_URL overrides the base
// URL for any provider (used by tests and proxies).
func Resolve(model, providerName string, env func(string) string) (Config, error) {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(env("SIGNET_PROVIDER")))
	}
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(env("PI_PROVIDER")))
	}
	if name == "" {
		name = "openai"
	}
	if model == "" {
		model = DefaultModel(name)
	}

	cfg, status := Prepare(model, name, EnvSource(env))
	if !status.Configured {
		var envHints []string
		for _, m := range status.Missing {
			switch cfg.Provider + ":" + m {
			case "openai:api_key":
				envHints = append(envHints, "OPENAI_API_KEY")
			case "anthropic:api_key":
				envHints = append(envHints, "ANTHROPIC_API_KEY")
			case "cloudflare-workers-ai:api_key", "cloudflare-ai-gateway:api_key":
				envHints = append(envHints, "CLOUDFLARE_API_KEY")
			case "cloudflare-workers-ai:account_id", "cloudflare-ai-gateway:account_id":
				envHints = append(envHints, "CLOUDFLARE_ACCOUNT_ID")
			case "cloudflare-ai-gateway:gateway_id":
				envHints = append(envHints, "CLOUDFLARE_GATEWAY_ID")
			}
		}
		return Config{}, &NotConfiguredError{
			Provider: cfg.Provider,
			Missing:  status.Missing,
			EnvHints: envHints,
			Searched: []string{"environment"},
		}
	}

	if override := strings.TrimSpace(env("SIGNET_BASE_URL")); override != "" {
		cfg.BaseURL = override
	}
	return cfg, nil
}

// chat sends a raw system+user exchange and returns the assistant reply text.
func chat(cfg Config, system, user string, client *http.Client) (string, error) {
	return doChat(cfg, system, []Turn{{Role: "user", Content: user}}, client)
}

// NewClassifier returns a rolemanager.Classifier backed by the configured provider.
func NewClassifier(cfg Config, client *http.Client) rolemanager.Classifier {
	return rolemanager.ClassifierFunc(func(p rolemanager.ClassifierPayload) (string, error) {
		return chat(cfg, p.System, p.User, client)
	})
}

// classifier is the internal alias for NewClassifier.
func classifier(cfg Config, client *http.Client) rolemanager.Classifier {
	return NewClassifier(cfg, client)
}

// SealSystem builds and seals the system prompt from trusted harness blocks.
//
// The provider and model come from cfg rather than the caller so that the
// three identities named in the prompt — harness, provider, model — always
// match the request actually being sent.
func SealSystem(cfg Config, pool *nonce.Pool, opts prompt.Options) (string, error) {
	if opts.Provider == "" {
		opts.Provider = cfg.Provider
	}
	if opts.Model == "" {
		opts.Model = cfg.Model
	}
	sysText, err := prompt.System(opts)
	if err != nil {
		return "", fmt.Errorf("build system prompt: %w", err)
	}
	sealed, err := rolemanager.BuildSystemPrompt([]rolemanager.SystemBlock{{Source: rolemanager.SourceHarness, Content: sysText}}, pool)
	if err != nil {
		return "", fmt.Errorf("seal system prompt: %w", err)
	}
	return delimiters.Egress(sealed, pool), nil
}

// buildRequest creates the sealed HTTP request for a provider.
// It is the single place where a chat/completions request is built,
// guarding against the streaming path drifting from the sealed path.
// The returned dialect is the structural guarantee: SendTurnsWithTools and
// decodeDelta consume the same dialect that built the request, so they cannot
// drift.
func buildRequest(cfg Config, system string, turns []Turn, stream bool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (*http.Request, dialect, error) {
	d, err := resolveDialect(cfg)
	if err != nil {
		return nil, dialect{}, err
	}
	p, err := provider.New(cfg.Provider, cfg.BaseURL, cfg.APIKey)
	if err != nil {
		return nil, dialect{}, err
	}

	// Effort maps to per-surface thinking controls. Empty effort emits nothing,
	// keeping requests byte-identical to today for every provider.
	var reasoningEffort string
	if d.effort && cfg.Effort != "" {
		reasoningEffort = strings.ToLower(strings.TrimSpace(cfg.Effort))
	}
	var thinking *wire.AnthropicThinking
	if d.thinking {
		if budget := models.ThinkingBudget(cfg.Effort); budget > 0 {
			thinking = &wire.AnthropicThinking{Type: "enabled", BudgetTokens: budget}
		}
	}
	// Usage metering is requested only for the native OpenAI streaming surface.
	// Cloudflare AI Gateway relays to heterogeneous upstreams that may 400 on
	// the unrecognised field, and a 400 there would break all streaming, not
	// just metering. The gateway therefore degrades to pure estimation.
	var streamOpts *wire.OpenAIStreamOptions
	if stream && d.usage {
		streamOpts = &wire.OpenAIStreamOptions{IncludeUsage: true}
	}

	switch d.kind {
	case kindWorkersAI:
		req, err := p.NewWorkersAIRequest(cfg.Model, wire.WorkersAIRequest{
			Messages: buildOpenAIMessages(system, turns),
			Stream:   stream,
			Tools:    openAITools,
		})
		return req, d, err
	case kindAnthropicMessages:
		req, err := d.messagesRequest(p, wire.AnthropicMessagesRequest{
			Model:      cfg.Model,
			MaxTokens:  4096,
			System:     system,
			Messages:   buildAnthropicMessages(turns),
			Stream:     stream,
			Tools:      anthropicTools,
			ToolChoice: "auto",
			Thinking:   thinking,
		})
		return req, d, err
	default:
		req, err := d.chatRequest(p, wire.OpenAIChatRequest{
			Model:           cfg.Model,
			Messages:        buildOpenAIMessages(system, turns),
			Stream:          stream,
			Tools:           openAITools,
			ToolChoice:      "auto",
			ReasoningEffort: reasoningEffort,
			StreamOptions:   streamOpts,
		})
		return req, d, err
	}
}

// Assistant is the structured result from a model turn.
type Assistant struct {
	Text       string
	ToolCalls  []rolemanager.ToolCall
	Stop       bool
	Usage      *transcript.Usage // provider-reported usage, when available
	StopReason string
}

// SendTurns sends a conversation and returns the assistant reply, including
// any tool calls the model emitted.
func SendTurns(cfg Config, system string, turns []Turn, client *http.Client) (Assistant, error) {
	return SendTurnsWithTools(cfg, system, turns, client, nil, nil)
}

// SendTurnsWithTools is SendTurns with tool definitions advertised to the model.
func SendTurnsWithTools(cfg Config, system string, turns []Turn, client *http.Client, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (Assistant, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, d, err := buildRequest(cfg, system, turns, false, openAITools, anthropicTools)
	if err != nil {
		return Assistant{}, err
	}
	redact := func(s string) string {
		return strings.ReplaceAll(s, cfg.APIKey, "<redacted>")
	}
	switch d.kind {
	case kindWorkersAI:
		return parseWorkersAI(client, req, redact)
	case kindAnthropicMessages:
		return parseAnthropic(client, req, redact)
	default:
		return parseOpenAIChat(client, req, redact)
	}
}

func doChat(cfg Config, system string, turns []Turn, client *http.Client) (string, error) {
	a, err := SendTurns(cfg, system, turns, client)
	if err != nil {
		return "", err
	}
	return a.Text, nil
}

func parseWorkersAI(client *http.Client, req *http.Request, redact func(string) string) (Assistant, error) {
	body, status, err := roundTrip(client, req, redact)
	if err != nil {
		return Assistant{}, err
	}
	var wr wire.WorkersAIResponse
	if err := json.Unmarshal(body, &wr); err != nil {
		return Assistant{}, fmt.Errorf("decode workers ai response (%d): %w", status, err)
	}
	if !wr.Success {
		return Assistant{}, fmt.Errorf("workers ai error: %+v", wr.Errors)
	}
	if len(wr.Result.Choices) > 0 {
		msg := wr.Result.Choices[0].Message
		var calls []rolemanager.ToolCall
		for _, tc := range msg.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return Assistant{}, fmt.Errorf("malformed tool arguments: %w", err)
			}
			calls = append(calls, rolemanager.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
		}
		return Assistant{Text: msg.Content, ToolCalls: calls}, nil
	}
	return Assistant{Text: wr.Result.Response}, nil
}

func parseOpenAIChat(client *http.Client, req *http.Request, redact func(string) string) (Assistant, error) {
	body, status, err := roundTrip(client, req, redact)
	if err != nil {
		return Assistant{}, err
	}
	var cr wire.OpenAIChatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return Assistant{}, fmt.Errorf("decode openai chat response (%d): %w", status, err)
	}
	if len(cr.Choices) == 0 {
		return Assistant{}, fmt.Errorf("openai chat response has no choices")
	}
	msg := cr.Choices[0].Message
	var calls []rolemanager.ToolCall
	for _, tc := range msg.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
			return Assistant{}, fmt.Errorf("malformed tool arguments for %s: %w", tc.Function.Name, err)
		}
		calls = append(calls, rolemanager.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	stop := cr.Choices[0].FinishReason == "stop" || cr.Choices[0].FinishReason == "end_turn"
	var usage *transcript.Usage
	if cr.Usage.TotalTokens > 0 {
		usage = &transcript.Usage{
			PromptTokens:     cr.Usage.PromptTokens,
			CompletionTokens: cr.Usage.CompletionTokens,
			TotalTokens:      cr.Usage.TotalTokens,
		}
	}
	return Assistant{Text: msg.Content, ToolCalls: calls, Stop: stop, Usage: usage, StopReason: cr.Choices[0].FinishReason}, nil
}

func parseAnthropic(client *http.Client, req *http.Request, redact func(string) string) (Assistant, error) {
	body, status, err := roundTrip(client, req, redact)
	if err != nil {
		return Assistant{}, err
	}
	var ar wire.AnthropicMessagesResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return Assistant{}, fmt.Errorf("decode anthropic response (%d): %w", status, err)
	}
	var b strings.Builder
	var calls []rolemanager.ToolCall
	for _, c := range ar.Content {
		b.WriteString(c.Text)
		if c.Type == "tool_use" {
			calls = append(calls, rolemanager.ToolCall{
				ID:   c.ID,
				Name: c.Name,
				Args: c.Input,
			})
		}
	}
	usage := &transcript.Usage{
		PromptTokens:     ar.Usage.InputTokens + ar.Usage.CacheReadInputTokens + ar.Usage.CacheCreationInputTokens,
		CompletionTokens: ar.Usage.OutputTokens,
	}
	return Assistant{Text: b.String(), ToolCalls: calls, Usage: usage, StopReason: ar.StopReason}, nil
}

func buildOpenAIMessages(system string, turns []Turn) []wire.OpenAIChatMessage {
	msgs := make([]wire.OpenAIChatMessage, 0, len(turns)+1)
	if system != "" {
		msgs = append(msgs, wire.OpenAIChatMessage{Role: "system", Content: system})
	}
	for _, t := range turns {
		switch t.Role {
		case "assistant":
			msg := wire.OpenAIChatMessage{Role: t.Role, Content: t.Content}
			for _, tc := range t.ToolCalls {
				args, _ := json.Marshal(tc.Args)
				msg.ToolCalls = append(msg.ToolCalls, wire.OpenAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{Name: tc.Name, Arguments: string(args)},
				})
			}
			msgs = append(msgs, msg)
		case "tool":
			msgs = append(msgs, wire.OpenAIChatMessage{Role: "tool", Content: t.Content, ToolCallID: t.ToolCallID, Name: t.ToolName})
		default:
			msgs = append(msgs, wire.OpenAIChatMessage{Role: t.Role, Content: t.Content})
		}
	}
	return msgs
}

func buildAnthropicMessages(turns []Turn) []wire.AnthropicMessage {
	msgs := make([]wire.AnthropicMessage, 0, len(turns))
	for _, t := range turns {
		msgs = append(msgs, wire.NewAnthropicTextMessage(t.Role, t.Content))
	}
	return msgs
}

// chatMessages builds an OpenAI-style message list with an optional system
// message followed by the user message.
func chatMessages(system, user string) []wire.OpenAIChatMessage {
	msgs := make([]wire.OpenAIChatMessage, 0, 2)
	if system != "" {
		msgs = append(msgs, wire.OpenAIChatMessage{Role: "system", Content: system})
	}
	return append(msgs, wire.OpenAIChatMessage{Role: "user", Content: user})
}

// Run sends one prompt — sanitized, sealed with a nonce/integrity delimiter,
// and egress-verified — and returns the completion text.
func Run(cfg Config, userPrompt string, client *http.Client) (string, error) {
	out, err := RunTurns(cfg, []Turn{{Role: "user", Content: userPrompt}}, client)
	if err != nil {
		return "", err
	}
	return out, nil
}

// RunTurns sends a conversation history and returns the latest assistant reply.
func RunTurns(cfg Config, turns []Turn, client *http.Client) (string, error) {
	return RunTurnsWithPool(cfg, turns, client, nonce.New(), prompt.Options{})
}

// RunTurnsWithPool is RunTurns with a caller-provided nonce pool so that
// multi-turn sessions reuse the same pool across requests.
func RunTurnsWithPool(cfg Config, turns []Turn, client *http.Client, pool *nonce.Pool, opts prompt.Options) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if pool == nil {
		pool = nonce.New()
	}
	verifiedSystem, err := SealSystem(cfg, pool, opts)
	if err != nil {
		return "", err
	}

	sanitized := make([]Turn, len(turns))
	for i, t := range turns {
		sanitized[i] = Turn{
			Role:    t.Role,
			Content: delimiters.Egress(sanitize.Sanitize(t.Content), pool),
		}
	}
	return doChat(cfg, verifiedSystem, sanitized, client)
}

// Result captures what the noninteractive pipeline decided and produced.
type Result struct {
	SanitizedPrompt  string
	SecuritySentinel rolemanager.Sentinel
	ModeDecision     rolemanager.ModeDecision
	Reply            string
}

// Engage runs the full noninteractive Role Manager pipeline: sanitize, then
// security-classify (refusing any non-SAFE sentinel), then optionally
// mode-classify, then send the sanitized prompt and return the reply.
func Engage(cfg Config, prompt string, detectMode bool, client *http.Client) (Result, error) {
	return EngageWithPosture(cfg, prompt, detectMode, client, posture.Defaults())
}

// EngageWithPosture is Engage with an explicit posture policy.
func EngageWithPosture(cfg Config, prompt string, detectMode bool, client *http.Client, pol posture.Policy) (Result, error) {
	if client == nil {
		client = http.DefaultClient
	}
	clean := sanitize.Sanitize(prompt)
	res := Result{SanitizedPrompt: clean}

	pipe := rolemanager.NewPipeline(classifier(cfg, client))
	dec, err := pipe.Admit(clean, pol)
	if err != nil {
		return res, err
	}
	if dec.Action != rolemanager.ActionProceed {
		return res, &rolemanager.RefusalError{Sentinel: dec.Sentinel}
	}
	res.SecuritySentinel = dec.Sentinel

	modeDec, err := rolemanager.Select(pipe.Classifier, rolemanager.ModeInput{Prompt: clean, GoalLimit: rolemanager.DefaultGoalPromptLengthLimit})
	if err != nil {
		return res, err
	}
	res.ModeDecision = modeDec

	reply, err := Run(cfg, clean, client)
	if err != nil {
		return res, err
	}
	res.Reply = reply
	return res, nil
}

func roundTrip(client *http.Client, req *http.Request, redact func(string) string) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(body))
		if redact != nil {
			msg = redact(msg)
		}
		return nil, resp.StatusCode, fmt.Errorf("provider returned %d: %s", resp.StatusCode, msg)
	}
	return body, resp.StatusCode, nil
}
