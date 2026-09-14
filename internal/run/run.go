// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, send one
// prompt, and return the completion text.
package run

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vulnetix/signet/internal/provider"
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
// PI_PROVIDER, then a default of openai.
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
	return cfg, nil
}

// Run sends one prompt and returns the completion text.
func Run(cfg Config, prompt string, client *http.Client) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	p, err := provider.New(cfg.Provider, cfg.BaseURL, cfg.APIKey)
	if err != nil {
		return "", err
	}

	var req *http.Request
	switch cfg.Provider {
	case "cloudflare-workers-ai":
		req, err = p.NewWorkersAIRequest(cfg.Model, wire.WorkersAIRequest{
			Messages: []wire.OpenAIChatMessage{{Role: "user", Content: prompt}},
		})
		if err != nil {
			return "", err
		}
		return doWorkersAI(client, req)
	case "cloudflare-ai-gateway":
		if strings.HasPrefix(strings.ToLower(cfg.Model), "claude") {
			req, err = p.NewGatewayMessagesRequest(wire.AnthropicMessagesRequest{
				Model:     cfg.Model,
				MaxTokens: 4096,
				Messages:  []wire.AnthropicMessage{{Role: "user", Content: prompt}},
			})
			if err != nil {
				return "", err
			}
			return doAnthropic(client, req)
		}
		req, err = p.NewGatewayChatRequest(wire.OpenAIChatRequest{
			Model:    cfg.Model,
			Messages: []wire.OpenAIChatMessage{{Role: "user", Content: prompt}},
		})
		if err != nil {
			return "", err
		}
		return doOpenAIChat(client, req)
	case "anthropic":
		req, err = p.NewMessagesRequest(wire.AnthropicMessagesRequest{
			Model:     cfg.Model,
			MaxTokens: 4096,
			Messages:  []wire.AnthropicMessage{{Role: "user", Content: prompt}},
		})
		if err != nil {
			return "", err
		}
		return doAnthropic(client, req)
	default:
		req, err = p.NewChatRequest(wire.OpenAIChatRequest{
			Model:    cfg.Model,
			Messages: []wire.OpenAIChatMessage{{Role: "user", Content: prompt}},
		})
		if err != nil {
			return "", err
		}
		return doOpenAIChat(client, req)
	}
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
