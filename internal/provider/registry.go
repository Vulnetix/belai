// Package provider adapts the wire-level shapes into concrete HTTP requests
// for each model provider. Every provider is reached through a base URL, an
// API key, a wire surface, and an auth style — matching the ai-firewall
// gateway contract.
//
// The provider registry is the single source of truth for compiled-in
// providers: one descriptor per provider replaces the parallel switch
// statements that previously scattered the same facts across run, credentials,
// models and modelfetch.
package provider

import (
	"os"
	"strings"

	"github.com/vulnetix/signet/internal/wire"
)

// Field is one credential a provider requires.
type Field struct {
	Name     string
	EnvVars  []string
	Secret   bool
	Optional bool
}

// ModelSpec is one selectable model in a provider's static catalogue.
type ModelSpec struct {
	ID, Label     string
	Efforts       []string
	ContextWindow int
}

// Builder composes a base URL from resolved credential fields, used when the
// provider default is not a constant.
type Builder func(fields map[string]string) string

// Auth names one wire authentication style. It is independent of the wire
// surface a provider speaks: a custom provider may, for example, speak the
// Anthropic surface with a bearer token.
//
// AuthXAPIKey additionally carries an anthropic-version header. That header is
// conceptually a surface concern, but bundling it here preserves today's bytes
// exactly and is correct in practice; do not thread the surface into Provider
// to split it.
type Auth string

const (
	AuthBearer  Auth = "bearer"    // authorization: Bearer <key>
	AuthXAPIKey Auth = "x-api-key" // x-api-key + anthropic-version: 2023-06-01
	AuthCFAIG   Auth = "cf-aig"    // cf-aig-authorization: Bearer <key>
	AuthCopilot Auth = "copilot"   // Bearer + Editor-Version + Copilot-Integration-Id
)

// Valid reports whether a is a known auth style.
func (a Auth) Valid() bool {
	switch a {
	case AuthBearer, AuthXAPIKey, AuthCFAIG, AuthCopilot:
		return true
	}
	return false
}

// Descriptor is the complete, self-contained identity of a compiled-in
// provider. Consumer packages convert from these provider-owned types into
// their own representations so the registry stays a leaf.
type Descriptor struct {
	Name    string
	Auth    Auth
	Fields  []Field
	BaseURL string // constant default; empty when BaseURLBuilder is used
	// BaseURLBuilder composes the default base URL from resolved fields.
	// When non-nil, BaseURL is ignored.
	BaseURLBuilder Builder
	// BaseURLField names an optional credential field that overrides the
	// default base URL (e.g. "base_url" for cloudflare-ai-gateway and the
	// OpenAI-compatible new providers).
	BaseURLField string
	NetrcHost    string

	Surface    wire.Surface
	ToolMethod wire.ToolMethod
	Thinking   bool // emit anthropic thinking budget from Effort
	Effort     bool // emit reasoning_effort
	Usage      bool // emit stream_options.include_usage

	DefaultModel string
	Models       []ModelSpec // static fallback catalogue
	ListPath     string      // "" = no live fetch; "/models" for most
	Local        bool        // probe with localinfer.ProbeRunning
}

var registry = map[string]Descriptor{
	"openai": {
		Name: "openai", Auth: AuthBearer,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"OPENAI_API_KEY"}, Secret: true},
		},
		BaseURL: "https://api.openai.com/v1", NetrcHost: "api.openai.com",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		Effort: true, Usage: true,
		DefaultModel: "gpt-5",
		Models: []ModelSpec{
			{ID: "gpt-5", Label: "GPT-5"},
			{ID: "gpt-5-mini", Label: "GPT-5 Mini"},
			{ID: "gpt-4.1", Label: "GPT-4.1"},
		},
		ListPath: "",
	},
	"anthropic": {
		Name: "anthropic", Auth: AuthXAPIKey,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"ANTHROPIC_API_KEY"}, Secret: true},
		},
		BaseURL: "https://api.anthropic.com", NetrcHost: "api.anthropic.com",
		Surface: wire.SurfaceAnthropicMessages, ToolMethod: wire.ToolMethodBlocks,
		Thinking:     true,
		DefaultModel: "claude-opus-4-5",
		Models: []ModelSpec{
			{ID: "claude-opus-4-5", Label: "Claude Opus 4.5"},
			{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5"},
			{ID: "claude-haiku-4-5", Label: "Claude Haiku 4.5"},
		},
		ListPath: "/v1/models",
	},
	"cloudflare-workers-ai": {
		Name: "cloudflare-workers-ai", Auth: AuthBearer,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"CLOUDFLARE_API_KEY"}, Secret: true},
			{Name: "account_id", EnvVars: []string{"CLOUDFLARE_ACCOUNT_ID"}, Secret: false},
		},
		BaseURLBuilder: buildCloudflareWorkersAI, NetrcHost: "api.cloudflare.com",
		Surface: wire.SurfaceWorkersAI, ToolMethod: wire.ToolMethodObject,
		DefaultModel: "@cf/moonshotai/kimi-k2.6",
		Models: []ModelSpec{
			{ID: "@cf/moonshotai/kimi-k2.6", Label: "Kimi K2.6"},
			{ID: "@cf/openai/gpt-oss-120b", Label: "GPT-OSS 120B"},
			{ID: "@cf/meta/llama-4-scout-17b-16e-instruct", Label: "Llama 4 Scout"},
			{ID: "@cf/qwen/qwen3-30b-a3b-fp8", Label: "Qwen3 30B"},
		},
		ListPath: "/ai/models/search",
	},
	"cloudflare-ai-gateway": {
		Name: "cloudflare-ai-gateway", Auth: AuthCFAIG,
		Fields: []Field{
			{Name: "token", EnvVars: []string{"CF_AIG_TOKEN"}, Secret: true},
			{Name: "account_id", EnvVars: []string{"CF_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID"}, Secret: false},
			{Name: "base_url", EnvVars: []string{"CF_AIG_URL"}, Secret: false, Optional: true},
		},
		BaseURLBuilder: buildCloudflareGateway, NetrcHost: "gateway.ai.cloudflare.com",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "claude-sonnet-4-5",
		Models: []ModelSpec{
			{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5"},
			{ID: "claude-opus-4-5", Label: "Claude Opus 4.5"},
			{ID: "gpt-5", Label: "GPT-5"},
		},
		ListPath: "",
	},
	"openrouter": {
		Name: "openrouter", Auth: AuthBearer,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"OPENROUTER_API_KEY"}, Secret: true},
		},
		BaseURL: "https://openrouter.ai/api/v1", NetrcHost: "openrouter.ai",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "openrouter/auto",
		Models: []ModelSpec{
			{ID: "openrouter/auto", Label: "OpenRouter Auto"},
			{ID: "openai/gpt-4o", Label: "GPT-4o"},
			{ID: "anthropic/claude-3.5-sonnet", Label: "Claude 3.5 Sonnet"},
			{ID: "google/gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
		},
		ListPath: "/models",
	},
	"google-gemini": {
		Name: "google-gemini", Auth: AuthBearer,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, Secret: true},
		},
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", NetrcHost: "generativelanguage.googleapis.com",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "gemini-2.5-flash",
		Models: []ModelSpec{
			{ID: "gemini-2.5-flash", Label: "Gemini 2.5 Flash"},
			{ID: "gemini-2.5-pro", Label: "Gemini 2.5 Pro"},
			{ID: "gemini-2.0-flash", Label: "Gemini 2.0 Flash"},
		},
		ListPath: "/models",
	},
	"ollama": {
		Name: "ollama", Auth: AuthBearer,
		Fields: []Field{
			{Name: "host", EnvVars: []string{"SIGNET_OLLAMA_HOST"}, Secret: false, Optional: true},
			{Name: "port", EnvVars: []string{"SIGNET_OLLAMA_PORT"}, Secret: false, Optional: true},
			{Name: "protocol", EnvVars: []string{"SIGNET_OLLAMA_PROTOCOL"}, Secret: false, Optional: true},
		},
		BaseURLBuilder: buildOllama, NetrcHost: "localhost",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "llama3",
		Models:       nil,
		ListPath:     "/models",
		Local:        true,
	},
	"llama-server": {
		Name: "llama-server", Auth: AuthBearer,
		Fields: []Field{
			{Name: "host", EnvVars: []string{"SIGNET_LLAMA_HOST"}, Secret: false, Optional: true},
			{Name: "port", EnvVars: []string{"SIGNET_LLAMA_PORT"}, Secret: false, Optional: true},
			{Name: "protocol", EnvVars: []string{"SIGNET_LLAMA_PROTOCOL"}, Secret: false, Optional: true},
		},
		BaseURLBuilder: buildLlamaServer, NetrcHost: "localhost",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "default",
		Models:       nil,
		ListPath:     "/models",
		Local:        true,
	},
	"github-copilot": {
		Name: "github-copilot", Auth: AuthCopilot,
		Fields: []Field{
			{Name: "oauth_token", EnvVars: []string{"GITHUB_COPILOT_TOKEN", "GH_TOKEN"}, Secret: true},
		},
		BaseURL: "https://api.githubcopilot.com", NetrcHost: "api.githubcopilot.com",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "gpt-4o",
		Models: []ModelSpec{
			{ID: "gpt-4o", Label: "GPT-4o"},
			{ID: "claude-sonnet-4-5", Label: "Claude Sonnet 4.5"},
			{ID: "o3-mini", Label: "o3 Mini"},
		},
		ListPath: "/models",
	},
	"huggingface": {
		Name: "huggingface", Auth: AuthBearer,
		Fields: []Field{
			{Name: "api_key", EnvVars: []string{"HF_TOKEN", "HUGGINGFACE_TOKEN"}, Secret: true},
		},
		BaseURL: "https://router.huggingface.co/v1", NetrcHost: "huggingface.co",
		Surface: wire.SurfaceOpenAIChat, ToolMethod: wire.ToolMethodString,
		DefaultModel: "",
		Models:       nil,
		ListPath:     "/models",
	},
}

// buildCloudflareWorkersAI composes the Cloudflare Workers AI base URL from
// the resolved account_id.
func buildCloudflareWorkersAI(fields map[string]string) string {
	acct := fields["account_id"]
	if acct == "" {
		return ""
	}
	return "https://api.cloudflare.com/client/v4/accounts/" + acct
}

// buildCloudflareGateway composes the Cloudflare AI Gateway compatibility base
// URL from account_id, with the base_url field taking precedence.
func buildCloudflareGateway(fields map[string]string) string {
	if base := fields["base_url"]; base != "" {
		return base
	}
	acct := fields["account_id"]
	if acct == "" {
		return ""
	}
	return "https://gateway.ai.cloudflare.com/v1/" + acct + "/default/compat"
}

// buildOllama constructs an Ollama base URL from decomposed host, port, and
// protocol, falling back to the OLLAMA_HOST environment variable or the
// localhost default. When OLLAMA_HOST is set and no decomposition is present,
// its value is treated as a complete host/port prefix exactly like the
// legacy ollamaBaseURL behaviour.
func buildOllama(fields map[string]string) string {
	host := strings.TrimSpace(fields["host"])
	port := strings.TrimSpace(fields["port"])
	protocol := strings.TrimSpace(fields["protocol"])

	// When no credential decomposition is present, delegate to OLLAMA_HOST
	// the same way the previous ollamaBaseURL() did.
	if host == "" && port == "" && protocol == "" {
		if env := strings.TrimSpace(ollamaHostEnv()); env != "" {
			if !strings.Contains(env, "://") {
				env = "http://" + env
			}
			return strings.TrimRight(env, "/") + "/v1"
		}
	}

	if protocol == "" {
		protocol = "http"
	}
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "11434"
	}
	return protocol + "://" + host + ":" + port + "/v1"
}

// buildLlamaServer constructs a llama.cpp / llama-server base URL from
// decomposed host, port, and protocol.
func buildLlamaServer(fields map[string]string) string {
	host := strings.TrimSpace(fields["host"])
	port := strings.TrimSpace(fields["port"])
	protocol := strings.TrimSpace(fields["protocol"])
	if protocol == "" {
		protocol = "http"
	}
	if host == "" {
		host = "localhost"
	}
	if port == "" {
		port = "8080"
	}
	return protocol + "://" + host + ":" + port + "/v1"
}

// ollamaHostEnv returns the OLLAMA_HOST environment value if set.
func ollamaHostEnv() string {
	return os.Getenv("OLLAMA_HOST")
}
