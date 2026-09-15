// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, run the Role
// Manager pipeline over the user prompt, and return the completion text.
package run

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/sanitize"
	"github.com/vulnetix/signet/internal/wire"
)

// Config is a resolved provider + model + credentials.
type Config struct {
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
}

// DefaultModel returns a sensible model for a provider when none is given.
func DefaultModel(providerName string) string {
	switch providerName {
	case "cloudflare-workers-ai":
		return "@cf/moonshotai/kimi-k2.6"
	case "cloudflare-ai-gateway":
		return "claude-sonnet-4-5"
	case "anthropic":
		return "claude-opus-4"
	default:
		return "gpt-5"
	}
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

	cfg := Config{Provider: name, Model: model}
	switch name {
	case "cloudflare-workers-ai":
		key, acct := env("CLOUDFLARE_API_KEY"), env("CLOUDFLARE_ACCOUNT_ID")
		if key == "" || acct == "" {
			return Config{}, fmt.Errorf("cloudflare-workers-ai requires CLOUDFLARE_API_KEY and CLOUDFLARE_ACCOUNT_ID")
		}
		cfg.APIKey = key
		cfg.BaseURL = "https://api.cloudflare.com/client/v4/accounts/" + acct
	case "cloudflare-ai-gateway":
		key, acct, gw := env("CLOUDFLARE_API_KEY"), env("CLOUDFLARE_ACCOUNT_ID"), env("CLOUDFLARE_GATEWAY_ID")
		if key == "" || acct == "" || gw == "" {
			return Config{}, fmt.Errorf("cloudflare-ai-gateway requires CLOUDFLARE_API_KEY, CLOUDFLARE_ACCOUNT_ID, and CLOUDFLARE_GATEWAY_ID")
		}
		cfg.APIKey = key
		cfg.BaseURL = "https://gateway.ai.cloudflare.com/v1/" + acct + "/" + gw
	case "anthropic":
		key := env("ANTHROPIC_API_KEY")
		if key == "" {
			return Config{}, fmt.Errorf("anthropic requires ANTHROPIC_API_KEY")
		}
		cfg.APIKey = key
		cfg.BaseURL = "https://api.anthropic.com"
	default:
		key := env("OPENAI_API_KEY")
		if key == "" {
			return Config{}, fmt.Errorf("%s requires OPENAI_API_KEY", name)
		}
		cfg.APIKey = key
		cfg.BaseURL = "https://api.openai.com/v1"
	}

	if override := strings.TrimSpace(env("SIGNET_BASE_URL")); override != "" {
		cfg.BaseURL = override
	}
	return cfg, nil
}

// chat sends a raw system+user exchange and returns the assistant reply text.
func chat(cfg Config, system, user string, client *http.Client) (string, error) {
	p, err := provider.New(cfg.Provider, cfg.BaseURL, cfg.APIKey)
	if err != nil {
		return "", err
	}
	var req *http.Request
	switch cfg.Provider {
	case "cloudflare-workers-ai":
		req, err = p.NewWorkersAIRequest(cfg.Model, wire.WorkersAIRequest{Messages: chatMessages(system, user)})
		if err != nil {
			return "", err
		}
		return doWorkersAI(client, req)
	case "cloudflare-ai-gateway":
		if strings.HasPrefix(strings.ToLower(cfg.Model), "claude") {
			req, err = p.NewGatewayMessagesRequest(wire.AnthropicMessagesRequest{
				Model:     cfg.Model,
				MaxTokens: 4096,
				System:    system,
				Messages:  []wire.AnthropicMessage{{Role: "user", Content: user}},
			})
			if err != nil {
				return "", err
			}
			return doAnthropic(client, req)
		}
		req, err = p.NewGatewayChatRequest(wire.OpenAIChatRequest{Model: cfg.Model, Messages: chatMessages(system, user)})
		if err != nil {
			return "", err
		}
		return doOpenAIChat(client, req)
	case "anthropic":
		req, err = p.NewMessagesRequest(wire.AnthropicMessagesRequest{
			Model:     cfg.Model,
			MaxTokens: 4096,
			System:    system,
			Messages:  []wire.AnthropicMessage{{Role: "user", Content: user}},
		})
		if err != nil {
			return "", err
		}
		return doAnthropic(client, req)
	default:
		req, err = p.NewChatRequest(wire.OpenAIChatRequest{Model: cfg.Model, Messages: chatMessages(system, user)})
		if err != nil {
			return "", err
		}
		return doOpenAIChat(client, req)
	}
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
	if client == nil {
		client = http.DefaultClient
	}
	clean := sanitize.Sanitize(userPrompt)

	pool := nonce.New()
	sysText, err := prompt.System(prompt.Options{})
	if err != nil {
		return "", fmt.Errorf("build system prompt: %w", err)
	}
	sealed, err := rolemanager.BuildSystemPrompt([]rolemanager.SystemBlock{{Source: rolemanager.SourceHarness, Content: sysText}}, pool)
	if err != nil {
		return "", fmt.Errorf("seal system prompt: %w", err)
	}
	verified := delimiters.Egress(clean, pool)
	return chat(cfg, sealed, verified, client)
}

// classifySecurity runs the security classifier over sanitized content.
func classifySecurity(cfg Config, content string, client *http.Client) (rolemanager.Sentinel, error) {
	p := rolemanager.BuildClassifierPayload(content)
	raw, err := chat(cfg, p.System, p.User, client)
	if err != nil {
		return "", err
	}
	s, err := rolemanager.ParseSentinel(raw)
	if err != nil {
		return "", fmt.Errorf("malformed security classifier output %q", raw)
	}
	return s, nil
}

// classifyMode runs the operating-mode classifier over sanitized content.
// Malformed output fails closed to ModeUndetermined.
func classifyMode(cfg Config, content string, client *http.Client) (rolemanager.ModeSentinel, error) {
	p := rolemanager.BuildModeClassifierPayload(content)
	raw, err := chat(cfg, p.System, p.User, client)
	if err != nil {
		return "", err
	}
	s, err := rolemanager.ParseModeSentinel(raw)
	if err != nil {
		return rolemanager.ModeUndetermined, nil
	}
	return s, nil
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
	if client == nil {
		client = http.DefaultClient
	}
	clean := sanitize.Sanitize(prompt)
	res := Result{SanitizedPrompt: clean}

	sec, err := classifySecurity(cfg, clean, client)
	if err != nil {
		return res, err
	}
	res.SecuritySentinel = sec
	if !sec.IsSafe() {
		return res, fmt.Errorf("refusing prompt: classified as %s", sec)
	}

	if detectMode {
		ms, err := classifyMode(cfg, clean, client)
		if err != nil {
			return res, err
		}
		res.ModeDecision = rolemanager.DecideMode(ms, rolemanager.ModeInput{Prompt: clean})
	}

	reply, err := Run(cfg, clean, client)
	if err != nil {
		return res, err
	}
	res.Reply = reply
	return res, nil
}

func doWorkersAI(client *http.Client, req *http.Request) (string, error) {
	body, status, err := roundTrip(client, req)
	if err != nil {
		return "", err
	}
	var wr wire.WorkersAIResponse
	if err := json.Unmarshal(body, &wr); err != nil {
		return "", fmt.Errorf("decode workers ai response (%d): %w", status, err)
	}
	if !wr.Success {
		return "", fmt.Errorf("workers ai error: %+v", wr.Errors)
	}
	if len(wr.Result.Choices) > 0 {
		return wr.Result.Choices[0].Message.Content, nil
	}
	return wr.Result.Response, nil
}

func doOpenAIChat(client *http.Client, req *http.Request) (string, error) {
	body, status, err := roundTrip(client, req)
	if err != nil {
		return "", err
	}
	var cr wire.OpenAIChatResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("decode openai chat response (%d): %w", status, err)
	}
	if len(cr.Choices) == 0 {
		return "", fmt.Errorf("openai chat response has no choices")
	}
	return cr.Choices[0].Message.Content, nil
}

func doAnthropic(client *http.Client, req *http.Request) (string, error) {
	body, status, err := roundTrip(client, req)
	if err != nil {
		return "", err
	}
	var ar wire.AnthropicMessagesResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return "", fmt.Errorf("decode anthropic response (%d): %w", status, err)
	}
	var b strings.Builder
	for _, c := range ar.Content {
		b.WriteString(c.Text)
	}
	return b.String(), nil
}

func roundTrip(client *http.Client, req *http.Request) ([]byte, int, error) {
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
		return nil, resp.StatusCode, fmt.Errorf("provider returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, resp.StatusCode, nil
}
