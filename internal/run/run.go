// Package run implements the noninteractive prompt path: resolve a provider
// from environment variables the same way Pi Coding Agent does, run the Role
// Manager pipeline over the user prompt, and return the completion text.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/guardrails"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/resilience"
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
	// ToolMethod is the session-stored tool calling method; ToolMethodNone
	// (zero) means detect. It is resolved once per session and carried on
	// every request so the model's method is not re-checked per turn.
	ToolMethod ToolMethod
	// MaxTokens overrides the completion cap. Zero means the surface default.
	// The classifier sets a bounded cap sized for its reply: a single sentinel
	// token for sentinel calls, or larger for structured-output calls.
	MaxTokens int
	// Classifier holds the resolved classifier config. When zero (no provider
	// and no model), NewClassifier derives it from this config with reasoning
	// off.
	Classifier ClassifierConfig
}

// ClassifierConfig is a provider/model/credentials tuple scoped to the
// security classifier. The classifier emits a single sentinel token, so its
// effort defaults to "none" (reasoning off) and its completion is capped.
type ClassifierConfig struct {
	Provider  string
	BaseURL   string
	APIKey    string
	Model     string
	Effort    string
	API       wire.Surface
	Auth      provider.Auth
	MaxTokens int
	Chunk     ChunkConfig
}

// ChunkConfig bounds the chunked classify-all path for oversized payloads.
type ChunkConfig struct {
	MaxBytes    int
	Concurrency int
}

// ClassifierMaxTokens caps a single-token sentinel classifier completion
// (security, mode, goal-evaluation, agent-evaluation). The reply itself is one
// token, but reasoning models spend output tokens on reasoning_content before
// they emit the final sentinel in content. A 16-token cap starved reasoning
// models mid-thought, leaving content empty and the verdict malformed, so the
// budget is large enough for a short reasoning preamble plus the token.
// Non-reasoning models still stop after the single token, so the wider cap
// costs them nothing. Structured-output builders (compaction, clarification,
// agent profiles) override this with ClassifierStructuredMaxTokens.
const ClassifierMaxTokens = 1024

// config converts a classifier config back to a plain request Config.
func (c ClassifierConfig) config() Config {
	return Config{
		Provider:  c.Provider,
		BaseURL:   c.BaseURL,
		APIKey:    c.APIKey,
		Model:     c.Model,
		Effort:    c.Effort,
		API:       c.API,
		Auth:      c.Auth,
		MaxTokens: c.MaxTokens,
	}
}

// ResolveClassifier derives the classifier config from the main config and an
// optional classifier settings block. Unset fields fall back to the main
// config; effort defaults to "none" so the sentinel call never pays for
// extended thinking. A classifier provider that differs from the main provider
// is resolved through src (nil means environment only).
func ResolveClassifier(main Config, cls *config.ClassifierSettings, src CredentialSource) (ClassifierConfig, error) {
	out := ClassifierConfig{
		Provider:  main.Provider,
		BaseURL:   main.BaseURL,
		APIKey:    main.APIKey,
		Model:     main.Model,
		Effort:    "none",
		API:       main.API,
		Auth:      main.Auth,
		MaxTokens: ClassifierMaxTokens,
		Chunk:     ChunkConfig{MaxBytes: 1 << 20, Concurrency: 4},
	}
	if cls == nil {
		return out, nil
	}
	if cls.Effort != "" {
		out.Effort = cls.Effort
	}
	if cls.Model != "" {
		out.Model = cls.Model
	}
	if cls.Chunk.MaxBytesOr() > 0 {
		out.Chunk.MaxBytes = cls.Chunk.MaxBytesOr()
	}
	if cls.Chunk.ConcurrencyOr() > 0 {
		out.Chunk.Concurrency = cls.Chunk.ConcurrencyOr()
	}
	if cls.Provider != "" && cls.Provider != main.Provider {
		cfg, status := Prepare(cls.Model, cls.Provider, src)
		if !status.Configured {
			var envHints []string
			return ClassifierConfig{}, &NotConfiguredError{
				Provider: cfg.Provider,
				Missing:  status.Missing,
				EnvHints: envHints,
				Searched: []string{"environment", "settings classifiers block"},
			}
		}
		out.Provider = cfg.Provider
		out.BaseURL = cfg.BaseURL
		out.APIKey = cfg.APIKey
		out.API = cfg.API
		out.Auth = cfg.Auth
		if cls.Model == "" {
			out.Model = cfg.Model
		}
	}
	return out, nil
}

// ClassifierOrDefault returns the resolved classifier config, deriving one
// from the main config when none was stored.
func (c Config) ClassifierOrDefault() ClassifierConfig {
	if c.Classifier.Provider == "" && c.Classifier.Model == "" {
		cc, _ := ResolveClassifier(c, nil, nil)
		return cc
	}
	return c.Classifier
}

func (c Config) String() string {
	return fmt.Sprintf("{Provider:%s BaseURL:%s APIKey:<redacted> Model:%s}", c.Provider, c.BaseURL, c.Model)
}

func (c Config) GoString() string {
	return fmt.Sprintf("run.Config{Provider:%q, BaseURL:%q, APIKey:%q, Model:%q}", c.Provider, c.BaseURL, "<redacted>", c.Model)
}

// ProviderError is a structured provider failure. It carries enough metadata
// for the resilience layer to classify retryable vs fatal errors while keeping
// the Error() string byte-identical to the legacy format.
type ProviderError struct {
	Provider   string
	Op         string
	Status     int    // 0 for a pre-response transport error
	Body       string // redacted at construction
	retryAfter time.Duration
	Err        error
}

// Error keeps the same text as the pre-resilience implementation so existing
// tests and CLI output stay unchanged.
func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider returned %d: %s", e.Status, e.Body)
	}
	if e.Err != nil {
		return fmt.Sprintf("request: %v", e.Err)
	}
	return "provider error"
}

func (e *ProviderError) Unwrap() error             { return e.Err }
func (e *ProviderError) StatusCode() int           { return e.Status }
func (e *ProviderError) RetryAfter() time.Duration { return e.retryAfter }

// newProviderError builds a ProviderError and redacts the API key from the
// raw response body before storing it.
func newProviderError(op string, cfg Config, resp *http.Response, body []byte, redact func(string) string) *ProviderError {
	msg := strings.TrimSpace(string(body))
	if redact != nil {
		msg = redact(msg)
	}
	retryAfter := parseRetryAfter(resp.Header.Get("retry-after"))
	return &ProviderError{
		Provider:   cfg.Provider,
		Op:         op,
		Status:     resp.StatusCode,
		Body:       msg,
		retryAfter: retryAfter,
	}
}

// parseRetryAfter parses a Retry-After header value using the shared
// resilience logic. It ignores failures and returns 0 so a bogus header never
// aborts the turn.
func parseRetryAfter(header string) time.Duration {
	return resilience.ParseRetryAfter(header, time.Now())
}

// reasoningEnabled reports whether an effort value should attach a
// reasoning_effort hint. "none" is the classifier's default: the sentinel call
// must not pay for extended thinking, and OpenAI rejects "none" as a
// reasoning_effort value, so it is omitted rather than sent verbatim.
func reasoningEnabled(effort string) bool {
	e := strings.ToLower(strings.TrimSpace(effort))
	return e != "" && e != "none"
}

// maxTokensOr returns v when positive, else the surface default.
func maxTokensOr(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// Attachment is user-referenced content that has already been sanitised and
// classified SAFE by the caller. It is sealed into its turn at egress, with a
// nonce from the live pool, so the seal survives the sanitise pass every turn
// body goes through.
type Attachment struct {
	Kind  string // "file" | "shell"
	Label string // the @path the user typed, or the ! command
	Body  string
}

// Turn is one message in a multi-turn conversation.
type Turn struct {
	Role        string // "user" | "assistant" | "tool"
	Content     string
	ToolCalls   []rolemanager.ToolCall
	ToolCallID  string
	ToolName    string
	Attachments []Attachment
	// Directive is a harness-authored continuation instruction sealed into this
	// turn at egress as a <directive> block. It is a separate field rather than
	// part of Content because egressTurns sanitizes Content — which strips every
	// known harness kind — before sealing, so a directive written into Content
	// would be silently deleted on its way to the provider.
	Directive string
	// egrossed memoises the sanitised, sealed and egress-verified content for
	// this turn. Turns are immutable once appended to a conversation and the
	// nonce pool never rotates mid-session, so the memo stays valid. Empty
	// means not yet computed. It is unexported so it never reaches the wire
	// shape.
	egrossed string
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
	if len(e.Missing) == 1 && e.Missing[0] == "provider" {
		return fmt.Sprintf("%s is not a built-in provider and no custom profile is defined (add it under the providers block in settings.json)", e.Provider)
	}
	return fmt.Sprintf("%s requires %s (looked in: %s)", e.Provider, strings.Join(e.EnvHints, ", "), strings.Join(e.Searched, ", "))
}

func (e *NotConfiguredError) Is(target error) bool { return target == ErrNotConfigured }

// CredentialSource resolves one provider field.
type CredentialSource interface {
	Lookup(provider, field string) (value, origin string, ok bool)
}

// ProviderSource resolves a custom provider profile by name. A CredentialSource
// that does not implement it has no custom providers.
type ProviderSource interface {
	Profile(name string) (provider.Profile, bool)
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
	case "openrouter:api_key":
		if v := f("OPENROUTER_API_KEY"); v != "" {
			return v, "$OPENROUTER_API_KEY", true
		}
	case "google-gemini":
		if v := f("GEMINI_API_KEY"); v != "" {
			return v, "$GEMINI_API_KEY", true
		}
		if v := f("GOOGLE_API_KEY"); v != "" {
			return v, "$GOOGLE_API_KEY", true
		}
	case "github-copilot:oauth_token":
		if v := f("GITHUB_COPILOT_TOKEN"); v != "" {
			return v, "$GITHUB_COPILOT_TOKEN", true
		}
		if v := f("GH_TOKEN"); v != "" {
			return v, "$GH_TOKEN", true
		}
	case "huggingface:api_key":
		if v := f("HF_TOKEN"); v != "" {
			return v, "$HF_TOKEN", true
		}
		if v := f("HUGGINGFACE_TOKEN"); v != "" {
			return v, "$HUGGINGFACE_TOKEN", true
		}
	default:
		// Custom providers resolve from their derived variable; EnvSource
		// fails closed rather than falling back to an unrelated provider's key.
		if field == "api_key" {
			if v := f(envVarForProvider(provider)); v != "" {
				return v, "$" + envVarForProvider(provider), true
			}
		}
	}
	return "", "", false
}

// Status reports how a provider's credentials resolved.
type Status struct {
	Configured bool
	Missing    []string
	Origins    map[string]string // field -> origin description
	Notes      []string
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
	case "openrouter":
		return "openrouter/auto"
	case "google-gemini":
		return "gemini-2.5-flash"
	case "ollama":
		return "llama3"
	case "github-copilot":
		return "gpt-4o"
	case "huggingface":
		return "Qwen/Qwen2.5-72B-Instruct"
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

// ollamaBaseURL returns the Ollama base URL: OLLAMA_HOST when set, normalised
// to include a scheme and the /v1 suffix, otherwise the local default.
func ollamaBaseURL() string {
	host := strings.TrimSpace(os.Getenv("OLLAMA_HOST"))
	if host == "" {
		return "http://localhost:11434/v1"
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	return strings.TrimRight(host, "/") + "/v1"
}

// Prepare resolves a provider configuration from a CredentialSource.
func Prepare(model, providerName string, src CredentialSource) (Config, Status) {
	name := normalizeProvider(providerName)
	if model == "" && provider.Builtin(name) {
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
	case "openai":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://api.openai.com/v1"
	case "openrouter":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://openrouter.ai/api/v1"
	case "google-gemini":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"
	case "ollama":
		// Ollama is local and needs no credential. provider.New still requires
		// a non-empty key, so pass a fixed placeholder rather than loosening
		// that validation for everyone.
		cfg.APIKey = "ollama"
		cfg.BaseURL = ollamaBaseURL()
	case "github-copilot":
		if oauth, origin, ok := src.Lookup(name, "oauth_token"); ok {
			cfg.APIKey = oauth
			status.Origins["oauth_token"] = origin
		} else {
			status.Missing = append(status.Missing, "oauth_token")
		}
		cfg.BaseURL = "https://api.githubcopilot.com"
		cfg.Auth = provider.AuthCopilot
	case "huggingface":
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
		cfg.BaseURL = "https://api-inference.huggingface.co/v1"
	default:
		// Custom path: an unknown name must resolve to a configured profile.
		// Built-in arms are reached first, so a profile named "openai" is never
		// consulted — the second layer of the shadowing defence.
		ps, ok := src.(ProviderSource)
		if !ok {
			status.Missing = append(status.Missing, "provider")
			break
		}
		prof, ok := ps.Profile(name)
		if !ok {
			status.Missing = append(status.Missing, "provider")
			break
		}
		cfg.BaseURL = prof.BaseURL
		cfg.API = prof.API
		cfg.Auth = prof.Auth
		if cfg.Model == "" && len(prof.Models) > 0 {
			cfg.Model = prof.Models[0]
		}
		if key, origin, ok := src.Lookup(name, "api_key"); ok {
			cfg.APIKey = key
			status.Origins["api_key"] = origin
		} else {
			status.Missing = append(status.Missing, "api_key")
		}
	}

	status.Configured = len(status.Missing) == 0
	if override := strings.TrimSpace(os.Getenv("SIGNET_BASE_URL")); override != "" {
		if cfg.BaseURL != "" && cfg.BaseURL != override {
			status.Notes = append(status.Notes, fmt.Sprintf("SIGNET_BASE_URL overrides the base URL for %s", name))
		}
		cfg.BaseURL = override
		status.Origins["base_url"] = "$SIGNET_BASE_URL"
	} else if g, err := guardrails.Load(); err == nil && string(g.Provider) == name {
		// Guardrails is a fallback base-URL source, below SIGNET_BASE_URL, and
		// only for the provider it serves.
		cfg.BaseURL = g.BaseURL
		status.Origins["base_url"] = "guardrails (" + g.KeySource + ")"
	}
	return cfg, status
}

// Resolve reads provider configuration from environment variables, mirroring
// Pi's resolution order: explicit provider flag, then SIGNET_PROVIDER, then
// PI_PROVIDER, then a default of openai. SIGNET_BASE_URL overrides the base
// URL for any provider (used by tests and proxies).
func Resolve(model, providerName string, env func(string) string) (Config, error) {
	return ResolveWithSource(model, providerName, env, EnvSource(env))
}

// ResolveWithSource is Resolve with an explicit CredentialSource. Passing a
// source that implements ProviderSource enables custom providers.
func ResolveWithSource(model, providerName string, env func(string) string, src CredentialSource) (Config, error) {
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

	cfg, status := Prepare(model, name, src)
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
			case "openrouter:api_key":
				envHints = append(envHints, "OPENROUTER_API_KEY")
			case "google-gemini:api_key":
				envHints = append(envHints, "GEMINI_API_KEY", "GOOGLE_API_KEY")
			case "github-copilot:oauth_token":
				envHints = append(envHints, "GITHUB_COPILOT_TOKEN", "GH_TOKEN")
			case "huggingface:api_key":
				envHints = append(envHints, "HF_TOKEN", "HUGGINGFACE_TOKEN")
			default:
				if m == "api_key" {
					envHints = append(envHints, envVarForProvider(cfg.Provider))
				}
			}
		}
		searched := []string{"environment"}
		if len(status.Missing) == 1 && status.Missing[0] == "provider" {
			searched = []string{"settings providers block"}
		}
		return Config{}, &NotConfiguredError{
			Provider: cfg.Provider,
			Missing:  status.Missing,
			EnvHints: envHints,
			Searched: searched,
		}
	}

	return cfg, nil
}

// chat sends a raw system+user exchange and returns the assistant reply text.
func chat(ctx context.Context, cfg Config, system, user string, client *http.Client) (string, error) {
	return doChat(ctx, cfg, system, []Turn{{Role: "user", Content: user}}, client)
}

// doChat is the blocking, non-tool classifier/chat shim.
func doChat(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client) (string, error) {
	return doChatWithPool(ctx, cfg, system, turns, client, nil)
}

func doChatWithPool(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool) (string, error) {
	a, err := SendTurnsWithTools(ctx, cfg, system, turns, client, pool, nil, nil)
	if err != nil {
		return "", err
	}
	return a.Text, nil
}

// NewClassifier returns a rolemanager.Classifier backed by the configured
// provider. The classifier uses cfg.Classifier when one was resolved, else the
// main config with reasoning off.
func NewClassifier(cfg Config, client *http.Client) rolemanager.Classifier {
	return NewClassifierWithRetry(cfg, client, nil)
}

// NewClassifierWithRetry is NewClassifier with an onRetry callback invoked
// before each L1 backoff. The callback can forward resilience.Attempt metadata
// to observers such as the TUI.
func NewClassifierWithRetry(cfg Config, client *http.Client, onRetry func(resilience.Attempt)) rolemanager.Classifier {
	cc := cfg.ClassifierOrDefault()
	return rolemanager.ClassifierFunc(func(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
		c := cc.config()
		if p.MaxTokens > 0 {
			c.MaxTokens = p.MaxTokens
		}
		return chatWithRetry(ctx, c, p.System, p.User, client, onRetry)
	})
}

func chatWithRetry(ctx context.Context, cfg Config, system, user string, client *http.Client, onRetry func(resilience.Attempt)) (string, error) {
	return doChatWithPoolRetry(ctx, cfg, system, []Turn{{Role: "user", Content: user}}, client, nil, onRetry)
}

func doChatWithPoolRetry(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, onRetry func(resilience.Attempt)) (string, error) {
	if pool == nil {
		pool = nonce.New()
	}
	turns = egressTurns(turns, pool)
	a, err := sendTurnsWithTools(ctx, cfg, system, turns, client, nil, nil, onRetry)
	if err != nil {
		return "", err
	}
	return a.Text, nil
}

// NewPipeline builds a rolemanager.Pipeline from the resolved classifier
// config: the classifier (reasoning off by default) plus the chunked
// classify-all bounds and an optional verdict cache.
func NewPipeline(cfg Config, client *http.Client, cache *rolemanager.Cache) *rolemanager.Pipeline {
	return NewPipelineWithRetry(cfg, client, cache, nil)
}

// NewPipelineWithRetry is NewPipeline with an onRetry callback forwarded to
// the underlying classifier so retries are visible to observers.
func NewPipelineWithRetry(cfg Config, client *http.Client, cache *rolemanager.Cache, onRetry func(resilience.Attempt)) *rolemanager.Pipeline {
	cc := cfg.ClassifierOrDefault()
	p := rolemanager.NewPipelineWithChunk(NewClassifierWithRetry(cfg, client, onRetry), rolemanager.ChunkConfig{
		MaxBytes:    cc.Chunk.MaxBytes,
		Concurrency: cc.Chunk.Concurrency,
	})
	p.Cache = cache
	return p
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
	// BuildSystemPrompt already verified and egressed the block; re-running
	// Egress here would re-scan the whole prompt for no effect.
	return sealed, nil
}

// buildRequest creates the sealed HTTP request for a provider.
// It is the single place where a chat/completions request is built,
// guarding against the streaming path drifting from the sealed path.
// The returned dialect is the structural guarantee: SendTurnsWithTools and
// decodeDelta consume the same dialect that built the request, so they cannot
// drift.
// newRequestFactory returns a factory that builds an HTTP request for the
// provider, plus the dialect resolved once for the turn. The factory can be
// called repeatedly during retry; the dialect stays identical across attempts.
func newRequestFactory(cfg Config, system string, turns []Turn, stream bool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (func(context.Context) (*http.Request, error), dialect, error) {
	d, err := resolveDialect(cfg)
	if err != nil {
		return nil, dialect{}, err
	}
	// A session-stored tool method (from detection or provider correction)
	// wins over the dialect's default.
	if cfg.ToolMethod != ToolMethodNone {
		d.method = cfg.ToolMethod
	}

	// Copilot token exchange and provider construction live inside the factory
	// so each retry attempt gets a fresh token and a fresh request.
	factory := func(ctx context.Context) (*http.Request, error) {
		key := cfg.APIKey
		if cfg.Auth == provider.AuthCopilot {
			token, err := copilotExchanger.Token(ctx, cfg.APIKey)
			if err != nil {
				return nil, err
			}
			key = token.Value
		}
		var p *provider.Provider
		if cfg.API != "" {
			p, err = provider.NewFromProfile(cfg.Provider, provider.Profile{BaseURL: cfg.BaseURL, API: cfg.API, Auth: cfg.Auth}, key)
		} else {
			p, err = provider.New(cfg.Provider, cfg.BaseURL, key)
		}
		if err != nil {
			return nil, err
		}

		var reasoningEffort string
		if d.effort && reasoningEnabled(cfg.Effort) {
			reasoningEffort = strings.ToLower(strings.TrimSpace(cfg.Effort))
		}
		var thinking *wire.AnthropicThinking
		if d.thinking {
			if budget := models.ThinkingBudget(cfg.Effort); budget > 0 {
				thinking = &wire.AnthropicThinking{Type: "enabled", BudgetTokens: budget}
			}
		}
		var streamOpts *wire.OpenAIStreamOptions
		if stream && d.usage {
			streamOpts = &wire.OpenAIStreamOptions{IncludeUsage: true}
		}

		// Repair any assistant turns whose tool_calls never received a matching
		// tool result in the stored transcript. Synthetic results are added to
		// the outbound payload only and are never written to session storage.
		turns = synthesizeDanglingToolResults(turns)
		// Bound the context cost: keep the last few iterations' tool results in
		// full and elide older ones to a short head.
		turns = elideToolResults(turns)

		switch d.kind {
		case kindWorkersAI:
			return p.NewWorkersAIRequest(cfg.Model, wire.WorkersAIRequest{
				Messages:  buildOpenAIMessages(system, turns, d.method),
				Stream:    stream,
				MaxTokens: cfg.MaxTokens,
				Tools:     openAITools,
			})
		case kindAnthropicMessages:
			return d.messagesRequest(p, wire.AnthropicMessagesRequest{
				Model:      cfg.Model,
				MaxTokens:  maxTokensOr(cfg.MaxTokens, 4096),
				System:     system,
				Messages:   buildAnthropicMessages(turns),
				Stream:     stream,
				Tools:      anthropicTools,
				ToolChoice: "auto",
				Thinking:   thinking,
			})
		default:
			return d.chatRequest(p, wire.OpenAIChatRequest{
				Model:           cfg.Model,
				Messages:        buildOpenAIMessages(system, turns, d.method),
				Stream:          stream,
				MaxTokens:       cfg.MaxTokens,
				Tools:           openAITools,
				ToolChoice:      "auto",
				ReasoningEffort: reasoningEffort,
				StreamOptions:   streamOpts,
			})
		}
	}
	return factory, d, nil
}

// buildRequest is the one-shot shim over newRequestFactory. It is kept for
// callers that already resolve the request once, and so the test suite keeps
// passing without edits.
func buildRequest(ctx context.Context, cfg Config, system string, turns []Turn, stream bool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (*http.Request, dialect, error) {
	factory, d, err := newRequestFactory(cfg, system, turns, stream, openAITools, anthropicTools)
	if err != nil {
		return nil, d, err
	}
	req, err := factory(ctx)
	return req, d, err
}

// Assistant is the structured result from a model turn.
type Assistant struct {
	Text       string
	Reasoning  string
	ToolCalls  []rolemanager.ToolCall
	Stop       bool
	Usage      *transcript.Usage // provider-reported usage, when available
	StopReason string
}

// SendTurns sends a conversation and returns the assistant reply, including
// any tool calls the model emitted.
func SendTurns(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client) (Assistant, error) {
	return SendTurnsWithTools(ctx, cfg, system, turns, client, nil, nil, nil)
}

// SendTurnsWithTools is SendTurns with tool definitions advertised to the model.
// The turns are sanitised and egress-verified before being serialised.
func SendTurnsWithTools(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef) (Assistant, error) {
	if pool == nil {
		pool = nonce.New()
	}
	turns = egressTurns(turns, pool)
	return sendTurnsWithTools(ctx, cfg, system, turns, client, openAITools, anthropicTools, nil)
}

// httpResult is the minimal per-attempt output for the retry loop.
type httpResult struct {
	body   []byte
	status int
}

// defaultRetryPolicy is the L1 policy for model provider calls.
var defaultRetryPolicy = resilience.Policy{
	MaxAttempts: 3,
	Base:        500 * time.Millisecond,
	Cap:         8 * time.Second,
	Ceiling:     60 * time.Second,
	Jitter:      0.25,
}

// sendTurnsWithTools is the core blocking request/response path. The caller
// must already have sanitised and egress-verified turns. L1 retry wraps the
// request factory plus roundTrip so pre-first-byte failures (status and
// transport) are retried without consuming the iteration budget.
func sendTurnsWithTools(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, openAITools []wire.OpenAITool, anthropicTools []wire.AnthropicToolDef, onRetry func(resilience.Attempt)) (Assistant, error) {
	if client == nil {
		client = httpclient.Default()
	}
	factory, d, err := newRequestFactory(cfg, system, turns, false, openAITools, anthropicTools)
	if err != nil {
		return Assistant{}, err
	}
	redact := func(s string) string {
		return strings.ReplaceAll(s, cfg.APIKey, "<redacted>")
	}

	do := func(ctx context.Context) (httpResult, error) {
		req, err := factory(ctx)
		if err != nil {
			return httpResult{}, err
		}
		body, status, err := roundTrip(ctx, client, req, cfg, redact)
		if err != nil {
			return httpResult{}, err
		}
		return httpResult{body: body, status: status}, nil
	}

	res, err := resilience.Do(ctx, defaultRetryPolicy, resilience.DefaultClassifier{}, do, onRetry)
	if err != nil {
		return Assistant{}, err
	}

	switch d.kind {
	case kindWorkersAI:
		return parseWorkersAI(res.body, res.status, redact)
	case kindAnthropicMessages:
		return parseAnthropic(res.body, res.status, redact)
	default:
		return parseOpenAIChat(res.body, res.status, redact)
	}
}

func parseWorkersAI(body []byte, status int, redact func(string) string) (Assistant, error) {
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
			args, err := parseToolCallArgs(tc.Function.Arguments)
			if err != nil {
				return Assistant{}, fmt.Errorf("malformed tool arguments: %w", err)
			}
			calls = append(calls, rolemanager.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
		}
		return Assistant{Text: msg.Content, Reasoning: msg.ReasoningContent, ToolCalls: calls}, nil
	}
	return Assistant{Text: wr.Result.Response}, nil
}

func parseOpenAIChat(body []byte, status int, redact func(string) string) (Assistant, error) {
	_ = redact
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
		args, err := parseToolCallArgs(tc.Function.Arguments)
		if err != nil {
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
	return Assistant{Text: msg.Content, Reasoning: msg.ReasoningContent, ToolCalls: calls, Stop: stop, Usage: usage, StopReason: cr.Choices[0].FinishReason}, nil
}

func parseAnthropic(body []byte, status int, redact func(string) string) (Assistant, error) {
	_ = redact
	var ar wire.AnthropicMessagesResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return Assistant{}, fmt.Errorf("decode anthropic response (%d): %w", status, err)
	}
	var b strings.Builder
	var reasoning strings.Builder
	var calls []rolemanager.ToolCall
	for _, c := range ar.Content {
		switch c.Type {
		case "thinking":
			reasoning.WriteString(c.Thinking)
		default:
			b.WriteString(c.Text)
		}
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
	return Assistant{Text: b.String(), Reasoning: reasoning.String(), ToolCalls: calls, Usage: usage, StopReason: ar.StopReason}, nil
}

// synthesizeDanglingToolResults inserts synthetic "No result provided"
// tool-role turns for every tool_call that has no matching tool turn in the
// transcript. It works on a copy: the original stored turns are never mutated.
func synthesizeDanglingToolResults(turns []Turn) []Turn {
	resolved := make(map[string]bool)
	for _, t := range turns {
		if t.Role == "tool" && t.ToolCallID != "" {
			resolved[t.ToolCallID] = true
		}
	}
	out := make([]Turn, 0, len(turns))
	for _, t := range turns {
		out = append(out, t)
		if t.Role != "assistant" || len(t.ToolCalls) == 0 {
			continue
		}
		for _, tc := range t.ToolCalls {
			if tc.ID != "" && !resolved[tc.ID] {
				out = append(out, Turn{
					Role:       "tool",
					Content:    "No result provided",
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
				})
			}
		}
	}
	return out
}

// keepRecentToolIterations is how many trailing tool-result iterations stay in
// full on the outbound request. Older tool results are elided to a short head,
// which is the largest token-cost centre in long sessions.
const keepRecentToolIterations = 3

// elideToolResults returns a copy of turns with tool results older than the
// last keepRecentToolIterations iterations truncated to
// transcript.DefaultMaxToolResultChars. An iteration boundary is an assistant
// turn that carried tool calls; the tool turns following it belong to that
// iteration. Elision runs per request (not memoised like egress) because a
// turn's position shifts as the conversation grows.
func elideToolResults(turns []Turn) []Turn {
	if len(turns) == 0 {
		return turns
	}
	keepFrom := 0
	iterations := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "assistant" && len(turns[i].ToolCalls) > 0 {
			iterations++
			if iterations >= keepRecentToolIterations {
				keepFrom = i
				break
			}
		}
	}
	if keepFrom == 0 {
		return turns
	}
	out := make([]Turn, len(turns))
	copy(out, turns)
	for i := 0; i < keepFrom; i++ {
		if out[i].Role == "tool" {
			out[i].Content = transcript.TruncateRunes(out[i].Content, transcript.DefaultMaxToolResultChars)
		}
	}
	return out
}

func buildOpenAIMessages(system string, turns []Turn, method wire.ToolMethod) []wire.OpenAIChatMessage {
	msgs := make([]wire.OpenAIChatMessage, 0, len(turns)+1)
	if system != "" {
		msgs = append(msgs, wire.OpenAIChatMessage{Role: "system", Content: system})
	}
	for _, t := range turns {
		switch t.Role {
		case "assistant":
			msg := wire.OpenAIChatMessage{Role: t.Role, Content: t.Content}
			for _, tc := range t.ToolCalls {
				args := string(tc.RawArgs)
				if args == "" {
					b, _ := json.Marshal(tc.Args)
					args = string(b)
				}
				var argsJSON json.RawMessage
				if method == wire.ToolMethodObject {
					argsJSON = wire.NewObjectToolCallArgs(args)
				} else {
					argsJSON = wire.NewStringToolCallArgs(args)
				}
				msg.ToolCalls = append(msg.ToolCalls, wire.OpenAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: wire.ToolCallFunction{
						Name:      tc.Name,
						Arguments: argsJSON,
					},
				})
			}
			if t.Content == "" && len(msg.ToolCalls) == 0 {
				continue
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
		switch t.Role {
		case "assistant":
			if t.Content == "" && len(t.ToolCalls) == 0 {
				continue
			}
			blocks := make([]wire.AnthropicRequestBlock, 0, 1+len(t.ToolCalls))
			if t.Content != "" {
				blocks = append(blocks, wire.AnthropicRequestBlock{Type: "text", Text: t.Content})
			}
			for _, tc := range t.ToolCalls {
				input := tc.Args
				if tc.RawArgs != "" {
					var rawInput map[string]any
					_ = json.Unmarshal([]byte(tc.RawArgs), &rawInput)
					input = rawInput
				}
				blocks = append(blocks, wire.AnthropicRequestBlock{Type: "tool_use", ID: tc.ID, Name: tc.Name, Input: input})
			}
			msgs = append(msgs, wire.NewAnthropicBlockMessage(t.Role, blocks))
		case "tool":
			// Anthropic tool results re-enter as a user message carrying a
			// tool_result block keyed to the originating tool_use id.
			msgs = append(msgs, wire.NewAnthropicBlockMessage("user", []wire.AnthropicRequestBlock{
				{Type: "tool_result", ToolUseID: t.ToolCallID, Content: t.Content},
			}))
		default:
			msgs = append(msgs, wire.NewAnthropicTextMessage(t.Role, t.Content))
		}
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
func Run(ctx context.Context, cfg Config, userPrompt string, client *http.Client) (string, error) {
	out, err := RunTurns(ctx, cfg, []Turn{{Role: "user", Content: userPrompt}}, client)
	if err != nil {
		return "", err
	}
	return out, nil
}

// RunTurns sends a conversation history and returns the latest assistant reply.
func RunTurns(ctx context.Context, cfg Config, turns []Turn, client *http.Client) (string, error) {
	return RunTurnsWithPool(ctx, cfg, turns, client, nonce.New(), prompt.Options{})
}

// RunTurnsWithPool is RunTurns with a caller-provided nonce pool so that
// multi-turn sessions reuse the same pool across requests.
func RunTurnsWithPool(ctx context.Context, cfg Config, turns []Turn, client *http.Client, pool *nonce.Pool, opts prompt.Options) (string, error) {
	if client == nil {
		client = httpclient.Default()
	}
	if pool == nil {
		pool = nonce.New()
	}
	verifiedSystem, err := SealSystem(cfg, pool, opts)
	if err != nil {
		return "", err
	}

	return doChatWithPool(ctx, cfg, verifiedSystem, turns, client, pool)
}

// Result captures what the noninteractive pipeline decided and produced.
type Result struct {
	SanitizedPrompt  string
	SecuritySentinel rolemanager.Sentinel
	ModeDecision     rolemanager.ModeDecision
	Reply            string
	Usage            *transcript.Usage // provider-reported usage on the final turn
	// GoalSentinel is how a goal-mode pass loop ended (empty in agent/plan
	// mode). Passes is how many passes the loop ran.
	GoalSentinel rolemanager.GoalSentinel
	Passes       int
}

// Engage runs the full noninteractive Role Manager pipeline: sanitize, then
// security-classify (refusing any non-SAFE sentinel), then optionally
// mode-classify, then send the sanitized prompt and return the reply.
func Engage(ctx context.Context, cfg Config, prompt string, detectMode bool, client *http.Client) (Result, error) {
	return EngageWithPosture(ctx, cfg, prompt, detectMode, client, posture.Defaults())
}

// EngageWithPosture is Engage with an explicit posture policy.
func EngageWithPosture(ctx context.Context, cfg Config, prompt string, detectMode bool, client *http.Client, pol posture.Policy) (Result, error) {
	if client == nil {
		client = httpclient.Default()
	}
	clean := sanitize.Sanitize(prompt)
	res := Result{SanitizedPrompt: clean}

	pipe := NewPipeline(cfg, client, nil)

	// Admit and Select are independent (same sanitized prompt, different
	// system prompts, no data flow): run them concurrently.
	selectCh := make(chan rolemanager.ModeDecision, 1)
	selectErrCh := make(chan error, 1)
	go func() {
		d, err := rolemanager.Select(ctx, pipe.Classifier, rolemanager.ModeInput{Prompt: clean, GoalLimit: rolemanager.DefaultGoalPromptLengthLimit})
		selectCh <- d
		selectErrCh <- err
	}()

	dec, err := pipe.Admit(ctx, clean, pol)
	if err != nil {
		return res, err
	}
	if dec.Action != rolemanager.ActionProceed {
		return res, &rolemanager.RefusalError{Sentinel: dec.Sentinel}
	}
	res.SecuritySentinel = dec.Sentinel

	if err := <-selectErrCh; err != nil {
		return res, err
	}
	res.ModeDecision = <-selectCh

	reply, err := Run(ctx, cfg, clean, client)
	if err != nil {
		return res, err
	}
	res.Reply = reply
	return res, nil
}

func roundTrip(ctx context.Context, client *http.Client, req *http.Request, cfg Config, redact func(string) string) ([]byte, int, error) {
	req = req.WithContext(ctx)
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
		return nil, resp.StatusCode, newProviderError("roundTrip", cfg, resp, body, redact)
	}
	return body, resp.StatusCode, nil
}

// parseToolCallArgs decodes a raw tool-call arguments value that may be either
// a JSON-encoded string (classic OpenAI) or a raw JSON object.
func parseToolCallArgs(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	// Try the classic string form first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		var args map[string]any
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return nil, err
		}
		return args, nil
	}
	// Otherwise it is an object form.
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	return args, nil
}
