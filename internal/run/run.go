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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/signet/internal/calltrace"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/httpclient"
	"github.com/vulnetix/signet/internal/mlclassify"
	"github.com/vulnetix/signet/internal/models"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/posture"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/provider"
	"github.com/vulnetix/signet/internal/resilience"
	"github.com/vulnetix/signet/internal/rolemanager"
	"github.com/vulnetix/signet/internal/rolemanager/jev"
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
	// Kind is the descriptor kind of a custom instance ("ollama",
	// "llama-server", "", or "openai-compatible"). Empty for built-ins.
	// Downstream consumers (modelfetch, availability) dispatch on it without
	// re-reading settings.
	Kind string
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
	// Security holds the resolved ML classifier-stack config (kind, phases,
	// phase-3 on/off). A Kind of "" or "llm" means the LLM sentinel path; the
	// ML stack only engages when Kind == "models".
	Security SecurityClassifierConfig
	// Routing holds the resolved model-routing config. Kind "defined" (zero)
	// routes every model call to this config; Kind "routed" routes each
	// role-manager use case through the Jev routing activity over Candidates.
	Routing RoutingConfig
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

// SecurityClassifierConfig is the resolved ML classifier-stack config. It is
// separate from ClassifierConfig because the LLM classifier that serves mode
// select, goal contract, clarify, plan eval, goal eval and compaction keeps
// its today behaviour (inherit the main model), while the ML stack is
// fail-closed: no inheritance, and phase 3 only when a classifier provider
// and model are both explicitly set.
type SecurityClassifierConfig struct {
	// Kind is "llm" or "models".
	Kind string
	// Phase1 and Phase2 configure the two local gates; nil disables that gate.
	Phase1 *mlclassify.ModelConfig
	Phase2 *mlclassify.ModelConfig
	// Phase2Deferred reports that the jailbreak gate is not running locally
	// (this build variant has no embedded jailbreak model and no remote model
	// was configured) and its JAILBREAK responsibility is deferred to phase 3.
	// It is false when the user explicitly set phase2.source: "disabled".
	Phase2Deferred bool
	// Phase3On reports whether phase 3 is enabled: on the models path, a
	// classifier provider and model are both explicitly set.
	Phase3On bool
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
	if cls.Chunk.MaxBytesOr() > 0 {
		out.Chunk.MaxBytes = cls.Chunk.MaxBytesOr()
	}
	if cls.Chunk.ConcurrencyOr() > 0 {
		out.Chunk.Concurrency = cls.Chunk.ConcurrencyOr()
	}

	// ResolveClassifier produces the guardrail classifier config: the LLM
	// sentinel on the llm path, and phase 3 on the models path. On both paths
	// classifier.provider/model override the main config. The role classifier
	// (mode select, goal/plan eval, compaction, …) is resolved separately by
	// NewRoleClassifier from the main config or the routing table, so a stale
	// classifier.provider can no longer move the role classifier off the main
	// model.
	if cls.Model != "" {
		out.Model = cls.Model
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
	// A model whose leading path segment is a different built-in provider can
	// never be served by the resolved classifier provider. Drop it: fall back
	// to the main model on an inherited provider, or the provider's own default
	// on an explicit one. This is the stale fresh-install default
	// ("openrouter/free") left behind after the provider moved.
	if out.Model != "" && !classifierModelApplies(out.Provider, out.Model) {
		if cls.Provider == "" {
			out.Model = main.Model
		} else {
			out.Model = DefaultModel(out.Provider)
		}
	}
	return out, nil
}

// classifierModelApplies reports whether a model id may be served by a
// classifier provider. The only unambiguous mismatch it rejects is a model id
// whose leading path segment is a different built-in provider name:
// "openrouter/free" can never be served by cloudflare-ai-gateway, anthropic,
// or huggingface. Un-namespaced ids ("gpt-5-mini") and the Workers AI
// namespace ("@cf/...") pass through, and a custom provider (which may proxy
// any model id) is never rejected.
func classifierModelApplies(providerName, model string) bool {
	if model == "" {
		return true
	}
	// A custom provider can serve any model id, so a namespaced id cannot be
	// attributed to a foreign built-in provider.
	if !provider.Builtin(providerName) {
		return true
	}
	head := model
	if i := strings.IndexByte(model, '/'); i >= 0 {
		head = model[:i]
	}
	head = strings.ToLower(strings.TrimSpace(head))
	if head == "" || !provider.Builtin(head) {
		return true
	}
	return head == strings.ToLower(strings.TrimSpace(providerName))
}

// ProfileOverride carries the provider/model/effort pins an engaged agent
// profile applies to a session's main config. It is the subset of
// agentprofile.AgentProfile shared by the TUI's engaged profile and background
// agents, kept in run so both callers resolve the override the same way.
type ProfileOverride struct {
	Provider string
	Model    string
	Effort   string
}

// ApplyProfileOverride returns the session's main config after applying an
// agent profile's provider/model/effort pins. A provider change re-resolves
// the provider through src so the new provider's credentials, base URL, auth
// style and wire surface replace the inherited ones: the previous provider's
// API key must never be sent to a different provider. The classifier config is
// then re-derived from the new main config — a stale Classifier would keep
// calling the previous provider/model at every role-manager activity.
//
// A provider that cannot be configured is an error (the session cannot be
// built). A classifier that cannot be resolved is left empty so
// ClassifierOrDefault re-derives it from the main config at pipeline build,
// matching the TUI's existing fail-open handling of classifier resolution.
func ApplyProfileOverride(cfg Config, o ProfileOverride, cls *config.ClassifierSettings, src CredentialSource) (Config, error) {
	if o.Provider == "" && o.Model == "" && o.Effort == "" {
		return cfg, nil
	}
	if src == nil {
		src = EnvSource(os.Getenv)
	}

	out := cfg
	if o.Effort != "" {
		out.Effort = o.Effort
	}
	if o.Provider != "" && o.Provider != cfg.Provider {
		prepared, err := ResolveWithSource(o.Model, o.Provider, os.Getenv, src)
		if err != nil {
			return Config{}, err
		}
		out = prepared
		out.Effort = cfg.Effort
		if o.Effort != "" {
			out.Effort = o.Effort
		}
	}
	if o.Model != "" {
		out.Model = o.Model
		// Mirror the classifier guard for the main model: a model id
		// namespaced to a different built-in provider must not ride on an
		// inherited provider. A profile that pins the provider explicitly
		// keeps the pair as written.
		if o.Provider == "" && !classifierModelApplies(out.Provider, out.Model) {
			out.Model = cfg.Model
		}
	}

	// Re-derive the role-manager config from the new main config. On the
	// models path ResolveClassifier already returns the main-inheriting LLM
	// classifier, and ResolveSecurityClassifier recomputes phase 3.
	out.Classifier = ClassifierConfig{}
	if cc, err := ResolveClassifier(out, cls, src); err == nil {
		out.Classifier = cc
	}
	out.Security = ResolveSecurityClassifier(cls)
	return out, nil
}

// ClassifierKind resolves the effective classifier kind: an explicit setting,
// else "models" when the binary embeds a model, else "llm". A binary that
// embeds the phase models always uses the models path — the /model kind row is
// locked there, so an explicit kind setting cannot override the embedded
// classifier.
func ClassifierKind(cls *config.ClassifierSettings) string {
	if mlclassify.Embedded() {
		return "models"
	}
	if cls != nil && cls.Kind != "" {
		return cls.Kind
	}
	return "llm"
}

const (
	phase1ModelID     = "GuardrailsAI/prompt-saturation-attack-detector"
	phase2ModelID     = "leomaurodesenv/bert-base-uncased-trustairlab-jailbreak"
	phase1AttackLabel = "LABEL_1" // the phase-1 model has no id2label; LABEL_1 is the saturation-attack class
	phase2AttackLabel = "unsafe"  // id2label: 0 = "safe", 1 = "unsafe"
)

// Phase1ModelID returns the default phase-1 model id.
func Phase1ModelID() string { return phase1ModelID }

// Phase2ModelID returns the default phase-2 model id.
func Phase2ModelID() string { return phase2ModelID }

// ResolveSecurityClassifier resolves the ML classifier-stack config from the
// raw settings. It carries no credentials: phase 3 reuses the already-resolved
// LLM classifier on Config, and remote phases resolve the HuggingFace token at
// pipeline construction. A Kind of "" or "llm" means the LLM sentinel path.
func ResolveSecurityClassifier(cls *config.ClassifierSettings) SecurityClassifierConfig {
	sc := SecurityClassifierConfig{Kind: ClassifierKind(cls)}
	if sc.Kind != "models" {
		return sc
	}
	// On a no-classifier binary phase 1 defaults to the known saturation model
	// over HuggingFace when a token resolves; otherwise it has no model and the
	// /model row shows the configure hint.
	hfAvailable := hfTokenConfigured()
	sc.Phase1 = resolveSecurityPhase(cls, 1, hfAvailable)
	sc.Phase2 = resolveSecurityPhase(cls, 2, hfAvailable)
	// Phase 2 is deferred to phase 3 when no local jailbreak gate can run:
	// this build variant embeds no jailbreak model and no remote model was
	// configured. On the jailbreak variant the gate is embedded but opt-in, so
	// an unset phase 2 is "available but off" (disabled), never deferred — the
	// user can turn it on locally. An explicit source: "disabled" is also not
	// deferred: it is a deliberate turn-off, so JAILBREAK is not handed to
	// phase 3.
	_, phase2Embedded := mlclassify.EmbeddedPhase2()
	sc.Phase2Deferred = sc.Phase2 == nil && !phase2Embedded && (cls == nil || cls.Phase2.Source != "disabled")
	// Phase 3 is opt-in and the switch is the existing provider+model choice:
	// it runs iff both are explicitly set and the model can actually be served
	// by that provider. No inheritance on the models path.
	sc.Phase3On = cls != nil && cls.Provider != "" && cls.Model != "" &&
		classifierModelApplies(cls.Provider, cls.Model)
	return sc
}

// resolveSecurityPhase resolves one phase gate to an mlclassify.ModelConfig.
func resolveSecurityPhase(cls *config.ClassifierSettings, phase int, hfAvailable bool) *mlclassify.ModelConfig {
	var ps config.ClassifierPhaseSettings
	if cls != nil {
		if phase == 1 {
			ps = cls.Phase1
		} else {
			ps = cls.Phase2
		}
	}
	if ps.Source == "disabled" {
		return nil
	}

	// Phase 2 (jailbreak) is opt-in even when the model is embedded. The
	// embedded jailbreak classifier over-triggers on ordinary tool results —
	// code, listings, JSON, help text and test output all score as "jailbreak"
	// above 0.95, higher than the canonical DAN jailbreak — so no threshold
	// separates them. Embedding the weights only makes the gate *available*,
	// never *on*. An explicit phase2.source or phase2.model turns it on. Phase
	// 1 stays on by default: the saturation gate is precise on tool output.
	if phase == 2 && ps.Source == "" && ps.Model == "" {
		return nil
	}

	var embeddedID string
	var embeddedOK bool
	if phase == 1 {
		embeddedID, embeddedOK = mlclassify.EmbeddedPhase1()
	} else {
		embeddedID, embeddedOK = mlclassify.EmbeddedPhase2()
	}

	model := ps.Model
	if model == "" {
		if embeddedOK {
			model = embeddedID
		} else if phase == 1 && hfAvailable {
			// A no-classifier binary with a HuggingFace token defaults phase 1
			// to the known saturation model over the inference API.
			model = phase1ModelID
		} else {
			// No embedded model, no explicit id, and (for phase 1) no token:
			// this phase has no model.
			return nil
		}
	}

	// Resolve the attack label from the curated catalogue when the model is
	// known; fall back to the per-phase embedded default otherwise. The
	// catalogue is the single source of truth for the five supported models,
	// so a curated remote model always resolves its documented label.
	attack := phase1AttackLabel
	if phase == 2 {
		attack = phase2AttackLabel
	}
	if label, ok := mlclassify.AttackLabelFor(model); ok {
		attack = label
	}

	source := ps.Source
	if source == "" {
		if embeddedOK && model == embeddedID {
			source = string(mlclassify.SourceEmbedded)
		} else {
			source = string(mlclassify.SourceHuggingFace)
		}
	}

	return &mlclassify.ModelConfig{
		ID:          model,
		Source:      mlclassify.ModelSource(source),
		Threshold:   ps.Threshold,
		AttackLabel: attack,
	}
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

// RoutingCandidate is one resolved provider/model option in the routing pool.
// Key is the use-case label from the settings map; Cfg is the fully resolved
// provider config (credentials, base URL, auth, surface, model).
type RoutingCandidate struct {
	Key string
	Cfg Config
}

// RoutingConfig is the resolved model-routing configuration carried on Config.
// The zero value is Kind "defined": every model call uses the main config.
type RoutingConfig struct {
	// Kind is "defined" (default) or "routed".
	Kind string
	// Candidates is the routing pool for Kind "routed": every use_cases entry
	// resolved into a provider config. The Jev routing activity selects one
	// candidate per role-manager use case.
	Candidates []RoutingCandidate
	// JevToken resolves the OpenRouter API key the Jev Decisions call uses. It
	// is a func so a lazily-fetched key (keychain/netrc) stays fresh per call.
	JevToken func() (string, error)
}

// ResolveRouting resolves the routing settings into a RoutingConfig. A nil or
// non-routed settings block resolves to Kind "defined" (no candidates, no Jev
// token): the main config serves every role-manager activity.
//
// Under "routed", every use_cases entry resolves to a provider config through
// src, with unset provider/model fields inheriting the main config. A
// candidate that cannot be configured is an error, not a silent skip: a broken
// routing table must not route traffic to the wrong model.
func ResolveRouting(main Config, rs *config.RoutingSettings, src CredentialSource) (RoutingConfig, error) {
	if rs == nil || rs.Kind != config.RoutingRouted {
		return RoutingConfig{Kind: config.RoutingDefined}, nil
	}
	if src == nil {
		src = EnvSource(os.Getenv)
	}
	out := RoutingConfig{Kind: config.RoutingRouted}
	keys := make([]string, 0, len(rs.UseCases))
	for k := range rs.UseCases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t := rs.UseCases[key]
		candidate, err := resolveRouteCandidate(main, t, src)
		if err != nil {
			return RoutingConfig{}, fmt.Errorf("routing.use_cases.%s: %w", key, err)
		}
		out.Candidates = append(out.Candidates, RoutingCandidate{Key: key, Cfg: candidate})
	}
	out.JevToken = func() (string, error) {
		v, _, ok := src.Lookup("openrouter", "api_key")
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("openrouter api key not configured for Jev routing")
		}
		return v, nil
	}
	return out, nil
}

// resolveRouteCandidate resolves one routing use-case entry to a provider
// config. An unset provider inherits the main provider; an unset model
// inherits the main model, or the new provider's default when the provider
// changed and no model was named.
func resolveRouteCandidate(main Config, t config.RoutingTarget, src CredentialSource) (Config, error) {
	providerName := t.Provider
	if providerName == "" {
		providerName = main.Provider
	}
	model := t.Model
	if model == "" && t.Provider != "" && t.Provider != main.Provider {
		model = DefaultModel(providerName)
	}
	if model == "" {
		model = main.Model
	}
	return ResolveWithSource(model, providerName, os.Getenv, src)
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
	if cfg.Provider == "cloudflare-ai-gateway" && resp.StatusCode == http.StatusUnauthorized {
		msg += " (hint: CF_AIG_TOKEN may be invalid or the gateway base URL may not exist)"
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

// Completion caps for requests that do not set Config.MaxTokens (the main
// agent; every role call sets its own small cap).
const (
	// streamMaxTokens bounds a streamed turn. A large Write or Edit is a
	// single tool_use block; a cap below it truncates the arguments and costs
	// the turn a "re-issue with complete arguments" round trip.
	streamMaxTokens = 64000
	// blockingMaxTokens bounds a non-streamed turn, which holds the whole
	// response in one HTTP read.
	blockingMaxTokens = 16384
	// unknownAnthropicMaxTokens is the cap for a model no catalogue or id rule
	// knows, which is every custom Anthropic-surface provider's model.
	unknownAnthropicMaxTokens = 4096
	// minThinkingBudget is the smallest budget_tokens Anthropic accepts.
	minThinkingBudget = 1024
)

// anthropicDefaultMaxTokens is the completion cap for a messages request that
// did not set one: the model's ceiling, bounded by the transport.
func anthropicDefaultMaxTokens(cfg Config, stream bool) int {
	ceiling := models.MaxOutput(cfg.Provider, cfg.Model)
	if ceiling <= 0 {
		return unknownAnthropicMaxTokens
	}
	if stream {
		return min(ceiling, streamMaxTokens)
	}
	return min(ceiling, blockingMaxTokens)
}

// anthropicThinking builds the thinking and output_config fields for the
// model's thinking style. Only the native Anthropic dialect sends either.
//
//   - budget models get {type:"enabled", budget_tokens}, clamped below
//     max_tokens (the API rejects a budget that is not smaller).
//   - adaptive models get {type:"adaptive"} + output_config.effort; they
//     reject budget_tokens. Effort "none" or unset leaves thinking off.
//   - always-on models cannot turn thinking off, so effort is the only
//     field. A role call's "none" asks for low effort, which keeps a
//     one-token sentinel reply from spending its small cap on thinking.
func anthropicThinking(cfg Config, d dialect, maxTokens int) (*wire.AnthropicThinking, *wire.AnthropicOutputConfig) {
	if !d.thinking {
		return nil, nil
	}
	effort := strings.ToLower(strings.TrimSpace(cfg.Effort))
	switch models.Thinking(cfg.Provider, cfg.Model) {
	case models.StyleBudget:
		budget := min(models.ThinkingBudget(effort), maxTokens-minThinkingBudget)
		if budget < minThinkingBudget {
			return nil, nil
		}
		return &wire.AnthropicThinking{Type: "enabled", BudgetTokens: budget}, nil
	case models.StyleAdaptive:
		if !reasoningEnabled(effort) {
			return nil, nil
		}
		return &wire.AnthropicThinking{Type: "adaptive"}, &wire.AnthropicOutputConfig{Effort: effort}
	case models.StyleAlways:
		switch {
		case reasoningEnabled(effort):
			return nil, &wire.AnthropicOutputConfig{Effort: effort}
		case effort == "none":
			return nil, &wire.AnthropicOutputConfig{Effort: "low"}
		}
	}
	return nil, nil
}

// ThinkingSource names the provider and model a signed thinking block belongs
// to. A block is replayed only to the same source: a signature is bound to the
// model that produced it, and another model rejects it.
func ThinkingSource(cfg Config) string {
	return cfg.Provider + "/" + cfg.Model
}

// applyCacheBreakpoints marks three of Anthropic's four cache breakpoints:
// the system block, the last tool definition, and the last content block of
// the newest message. Tool order and the system text are stable within a
// session, so the first two cache the fixed prefix; the third caches the
// conversation so far for the next iteration of the tool loop. The shared
// tool slice is copied, never marked in place.
func applyCacheBreakpoints(req *wire.AnthropicMessagesRequest) {
	if s, ok := req.System.(string); ok && s != "" {
		req.System = []wire.AnthropicSystemBlock{{Type: "text", Text: s, CacheControl: wire.EphemeralCache()}}
	}
	if n := len(req.Tools); n > 0 {
		tools := make([]wire.AnthropicToolDef, n)
		copy(tools, req.Tools)
		tools[n-1].CacheControl = wire.EphemeralCache()
		req.Tools = tools
	}
	if n := len(req.Messages); n > 0 {
		last := &req.Messages[n-1]
		switch c := last.Content.(type) {
		case string:
			if c != "" {
				last.Content = []wire.AnthropicRequestBlock{{Type: "text", Text: c, CacheControl: wire.EphemeralCache()}}
			}
		case []wire.AnthropicRequestBlock:
			if k := len(c); k > 0 {
				blocks := make([]wire.AnthropicRequestBlock, k)
				copy(blocks, c)
				blocks[k-1].CacheControl = wire.EphemeralCache()
				last.Content = blocks
			}
		}
	}
}

// WireModel returns the model id as it is sent on the wire for a provider.
// Workers AI models (the @cf/ namespace) routed through the Cloudflare AI
// Gateway are prefixed with "workers-ai/" so the gateway dispatches them to
// the Workers AI backend; other providers and other gateway-routed models are
// sent verbatim. This is the single source of truth shared by request
// construction and the TUI footer so the two can never disagree.
func WireModel(provider, model string) string {
	if provider == "cloudflare-ai-gateway" && strings.HasPrefix(model, "@cf/") {
		return "workers-ai/" + model
	}
	// Hugging Face Inference Providers route OpenAI-compatible chat requests
	// through a provider suffix on the model id. The API default is the
	// "fastest" policy, but the raw endpoint expects the suffix to be
	// present (e.g. "deepseek-ai/DeepSeek-R1:fastest"). Preserve an
	// already-qualified model id so users can override with a specific
	// provider or policy.
	if provider == "huggingface" && model != "" && !strings.Contains(model, ":") {
		return model + ":fastest"
	}
	return model
}

// Attachment is user-referenced content that has already been sanitised and
// classified SAFE by the caller. It is sealed into its turn at egress, with a
// nonce from the live pool, so the seal survives the sanitise pass every turn
// body goes through.
type Attachment struct {
	Kind  string // "file" | "directory" | "shell"
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
	// Thinking holds an assistant turn's signed thinking blocks, opaque
	// provider data replayed verbatim to the model that produced them
	// (ThinkingModel, see ThinkingSource) and dropped for any other. It is
	// model output: it is never sanitised into, promoted into, or rendered
	// as a system, tools or agent block.
	Thinking      []ThinkingBlock
	ThinkingModel string
	// egrossed memoises the sanitised, sealed and egress-verified content for
	// this turn. Turns are immutable once appended to a conversation and the
	// nonce pool never rotates mid-session, so the memo stays valid. Empty
	// means not yet computed. It is unexported so it never reaches the wire
	// shape.
	egrossed string
}

// ThinkingBlock is one Anthropic thinking or redacted_thinking block. Within a
// tool loop the API requires the assistant turn's signed thinking to come
// back unchanged, so the fields are kept byte-for-byte.
type ThinkingBlock struct {
	Redacted  bool   `json:"redacted,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

// requestBlock renders the block in its wire shape.
func (b ThinkingBlock) requestBlock() wire.AnthropicRequestBlock {
	if b.Redacted {
		return wire.AnthropicRequestBlock{Type: "redacted_thinking", Data: b.Data}
	}
	text := b.Thinking
	return wire.AnthropicRequestBlock{Type: "thinking", Thinking: &text, Signature: b.Signature}
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

// AliasSource resolves a user-facing provider label to its canonical slug. It
// is optional: a CredentialSource that does not implement it resolves no
// aliases, which is the fail-closed default.
type AliasSource interface {
	CanonicalProvider(label string) (string, bool)
}

// FirewallSource is implemented by a CredentialSource that can route a
// provider through the Vulnetix AI Firewall gateway.
type FirewallSource interface {
	Firewall(provider string) (baseURL, apiKey string, ok bool)
}

// EnvSource adapts an environment-lookup function to CredentialSource.
type EnvSource func(string) string

// Lookup implements CredentialSource. It derives the environment-variable
// list for each field from the provider registry so the table cannot drift
// from the run-time resolution path.
func (f EnvSource) Lookup(providerName, field string) (value, origin string, ok bool) {
	if d, ok := provider.Lookup(providerName); ok {
		for _, fld := range d.Fields {
			if fld.Name != field {
				continue
			}
			for _, ev := range fld.EnvVars {
				if v := f(ev); v != "" {
					return v, "$" + ev, true
				}
			}
			break
		}
		return "", "", false
	}
	// Custom providers resolve from their derived variable; EnvSource
	// fails closed rather than falling back to an unrelated provider's key.
	if field == "api_key" {
		if v := f(envVarForProvider(providerName)); v != "" {
			return v, "$" + envVarForProvider(providerName), true
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

// DefaultProvider is the provider a fresh install resolves to when nothing —
// settings, state, environment or flag — names one. OpenRouter is the default
// because its free tier is the only one a new user can reach with a signup
// credit alone; DefaultModel("openrouter") is the zero-cost router.
const DefaultProvider = "openrouter"

// DefaultModel returns a sensible model for a provider when none is given.
func DefaultModel(providerName string) string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		name = DefaultProvider
	}
	if d, ok := provider.Lookup(name); ok {
		return d.DefaultModel
	}
	// An unknown name is a custom profile whose model the user names itself;
	// fall back to the default provider's model rather than a paid one.
	return DefaultModel(DefaultProvider)
}

func normalizeProvider(providerName string) string {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		return DefaultProvider
	}
	return name
}

// Prepare resolves a provider configuration from a CredentialSource.
func Prepare(model, providerName string, src CredentialSource) (Config, Status) {
	name := normalizeProvider(providerName)
	if model == "" && provider.Builtin(name) {
		model = DefaultModel(name)
	}

	cfg := Config{Provider: name, Model: model}
	status := Status{Origins: map[string]string{}}

	if d, ok := provider.Lookup(name); ok {
		resolveBuiltin(&cfg, &status, d, src)
	} else {
		// Custom path: an unknown name must resolve to a configured profile.
		// Built-in lookup is checked first, so a profile named "openai" is never
		// consulted — the second layer of the shadowing defence.
		ps, ok := src.(ProviderSource)
		if !ok {
			status.Missing = append(status.Missing, "provider")
		} else {
			prof, ok := ps.Profile(name)
			if !ok {
				// A display label resolves only after both the built-in and the
				// slug lookups miss, so a label can never shadow either.
				alias, isAlias := src.(AliasSource)
				if isAlias {
					if slug, found := alias.CanonicalProvider(name); found {
						if p2, ok2 := ps.Profile(slug); ok2 {
							name = slug
							cfg.Provider = slug
							prof = p2
							ok = true
						}
					}
				}
			}
			if !ok {
				status.Missing = append(status.Missing, "provider")
			} else {
				resolveCustom(&cfg, &status, name, prof, src)
			}
		}
	}

	status.Configured = len(status.Missing) == 0
	if fw, ok := src.(FirewallSource); ok {
		if base, key, on := fw.Firewall(name); on {
			cfg.BaseURL = base
			cfg.APIKey = key
			cfg.Auth = provider.AuthBearer
			status.Origins["base_url"] = "vulnetix-firewall"
			status.Origins["api_key"] = "vulnetix-firewall"
			status.Missing = nil
			status.Notes = append(status.Notes, "routed through the Vulnetix AI Firewall")
		}
	}

	if override := strings.TrimSpace(os.Getenv("SIGNET_BASE_URL")); override != "" {
		if cfg.BaseURL != "" && cfg.BaseURL != override {
			status.Notes = append(status.Notes, fmt.Sprintf("SIGNET_BASE_URL overrides the base URL for %s", name))
		}
		cfg.BaseURL = override
		status.Origins["base_url"] = "$SIGNET_BASE_URL"
	}

	return cfg, status
}

// resolveBuiltin fills cfg and status from the descriptor table. It keeps
// the exact origins and missing-field behavior of the previous switch arms.
func resolveBuiltin(cfg *Config, status *Status, d provider.Descriptor, src CredentialSource) {
	// cfg.API stays empty for built-in providers; only custom profiles carry
	// their surface so that request construction picks provider.New.
	cfg.Auth = d.Auth

	resolved := make(map[string]string, len(d.Fields))
	for _, f := range d.Fields {
		if v, origin, ok := src.Lookup(cfg.Provider, f.Name); ok {
			resolved[f.Name] = v
			status.Origins[f.Name] = origin
		} else if !f.Optional {
			status.Missing = append(status.Missing, f.Name)
		}
	}

	cfg.BaseURL = d.BaseURL
	if d.BaseURLBuilder != nil {
		cfg.BaseURL = d.BaseURLBuilder(resolved)
	}
	if d.BaseURLField != "" {
		if override, ok := resolved[d.BaseURLField]; ok && override != "" {
			cfg.BaseURL = override
			status.Origins["base_url"] = status.Origins[d.BaseURLField]
		} else if cfg.Provider == "cloudflare-ai-gateway" && cfg.BaseURL != "" {
			status.Origins["base_url"] = "default"
		}
	}

	// Preserve legacy base_url origins for local servers.
	switch cfg.Provider {
	case "ollama":
		if resolved["host"] == "" && resolved["port"] == "" && resolved["protocol"] == "" {
			status.Origins["base_url"] = "$OLLAMA_HOST"
		}
	case "llama-server":
		if resolved["host"] == "" && resolved["port"] == "" && resolved["protocol"] == "" {
			status.Origins["base_url"] = "default"
		}
	}

	// The first secret field is the provider's API key; local servers use a
	// placeholder because they do not authenticate over the wire.
	for _, f := range d.Fields {
		if f.Secret {
			cfg.APIKey = resolved[f.Name]
			break
		}
	}
	switch cfg.Provider {
	case "ollama":
		if cfg.APIKey == "" {
			cfg.APIKey = "ollama"
		}
	case "llama-server":
		if cfg.APIKey == "" {
			cfg.APIKey = "llama"
		}
	}
}

// resolveCustom fills cfg and status for a custom profile. When the profile
// carries a kind, its auth style, wire surface and tool method default from the
// template descriptor, and an optional template api_key does not make the
// provider unconfigured — the same placeholder the built-in local providers
// use is injected so newProvider's non-empty-key check still passes.
func resolveCustom(cfg *Config, status *Status, name string, prof provider.Profile, src CredentialSource) {
	cfg.BaseURL = prof.BaseURL
	cfg.Kind = prof.Kind
	cfg.API = prof.API
	cfg.Auth = prof.Auth

	if d, ok := provider.Template(prof.Kind); ok && prof.Kind != "" {
		if prof.Auth == "" {
			cfg.Auth = d.Auth
		}
		if prof.API == "" {
			cfg.API = d.Surface
		}
		// Only inherit the template's tool method when the surface actually
		// matches it; a hand-edited kind with a different surface keeps the
		// dialect's own detection.
		if prof.API == d.Surface {
			cfg.ToolMethod = d.ToolMethod
		}
	}
	if cfg.Model == "" && len(prof.Models) > 0 {
		cfg.Model = prof.Models[0]
	}
	if key, origin, ok := src.Lookup(name, "api_key"); ok {
		cfg.APIKey = key
		status.Origins["api_key"] = origin
	} else {
		// Keyless custom provider: inject a harmless placeholder so the wire
		// layer's non-empty-key check passes. Whether the endpoint is actually
		// usable is the availability probe's call (its models endpoint must
		// answer); a server that really requires a key rejects the request
		// with a clear 401 at runtime instead of being gated here.
		cfg.APIKey = placeholderKey(prof.Kind)
	}
}

// placeholderKey returns the placeholder the keyless path injects. Local
// templates reuse the built-in local providers' placeholders; generic
// OpenAI-compatible endpoints get a neutral value keyless servers ignore.
func placeholderKey(kind string) string {
	switch kind {
	case "llama-server":
		return "llama"
	case "ollama":
		return "ollama"
	default:
		return "signet"
	}
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
			case "cloudflare-workers-ai:api_key":
				envHints = append(envHints, "CLOUDFLARE_API_KEY")
			case "cloudflare-ai-gateway:token":
				envHints = append(envHints, "CF_AIG_TOKEN")
			case "cloudflare-workers-ai:account_id":
				envHints = append(envHints, "CLOUDFLARE_ACCOUNT_ID")
			case "cloudflare-ai-gateway:account_id":
				envHints = append(envHints, "CF_ACCOUNT_ID", "CLOUDFLARE_ACCOUNT_ID")
			case "cloudflare-ai-gateway:base_url":
				envHints = append(envHints, "CF_AIG_URL")
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

// ollamaBaseURL returns the effective Ollama base URL from the environment,
// or the localhost default. It is kept as a test seam.
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

// buildOllamaBaseURL constructs an Ollama base URL from decomposed host, port,
// and protocol. Empty values default to localhost, 11434, and http. It is
// kept as a test seam.
func buildOllamaBaseURL(host, port, protocol string) string {
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

// buildLlamaBaseURL constructs a llama.cpp base URL from decomposed host,
// port, and protocol. Empty values default to localhost, 8080, and http. It
// is kept as a test seam.
func buildLlamaBaseURL(host, port, protocol string) string {
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

// classifierFromConfig builds a rolemanager.Classifier that sends
// ClassifierPayload calls through the given resolved provider config. It is
// the single construction point shared by the guardrail classifier, the
// defined role classifier and every routed candidate classifier.
func classifierFromConfig(c Config, client *http.Client, onRetry func(resilience.Attempt)) rolemanager.Classifier {
	return rolemanager.ClassifierFunc(func(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if p.MaxTokens > 0 {
			c.MaxTokens = p.MaxTokens
		}
		a, err := chatWithRetryAssistant(ctx, c, p.System, p.User, client, onRetry)
		if err != nil {
			return "", err
		}
		text := a.Text
		if p.AllowReasoningFallback && strings.TrimSpace(text) == "" {
			text = a.Reasoning
		}
		return text, nil
	})
}

// mainClassifierConfig returns the main config with reasoning off, the shape
// the role classifier uses under "defined": the global provider/model serves
// every non-guardrail role-manager activity.
func mainClassifierConfig(cfg Config) Config {
	return Config{
		Provider: cfg.Provider,
		BaseURL:  cfg.BaseURL,
		APIKey:   cfg.APIKey,
		Model:    cfg.Model,
		Effort:   "none",
		API:      cfg.API,
		Auth:     cfg.Auth,
		Kind:     cfg.Kind,
	}
}

// NewClassifierWithRetry is NewClassifier with an onRetry callback invoked
// before each L1 backoff. A classifier config whose provider and model form a
// Jev Decisions model falls back to the main config: Jev is a Decisions model,
// never a chat model, so it must never receive chat/completions. The Jev
// security path wires the Decisions call separately in NewPipelineWithRetry.
func NewClassifierWithRetry(cfg Config, client *http.Client, onRetry func(resilience.Attempt)) rolemanager.Classifier {
	cc := cfg.ClassifierOrDefault().config()
	if jev.IsDecisionsModel(cc.Provider, cc.Model) {
		cc = mainClassifierConfig(cfg)
	}
	return classifierFromConfig(cc, client, onRetry)
}

// NewRoleClassifier builds the classifier that serves the non-guardrail
// role-manager activities (mode select, goal/plan eval, compaction, goal
// contract, clarify, session name, agent eval). Under "defined" it is the main
// config with reasoning off. Under "routed" it dispatches each payload's
// UseCase through the Jev routing activity, cached per use case, falling back
// to the main config on any routing failure.
func NewRoleClassifier(cfg Config, client *http.Client, onRetry func(resilience.Attempt)) rolemanager.Classifier {
	if cfg.Routing.Kind != config.RoutingRouted || len(cfg.Routing.Candidates) == 0 {
		return classifierFromConfig(mainClassifierConfig(cfg), client, onRetry)
	}
	return newRoutedClassifier(cfg, client, onRetry)
}

// routedClassifier resolves a per-use-case classifier through Jev once, caches
// it, and delegates every later call for that use case to the cached winner.
// A transport error, an inconclusive verdict, or a winner missing from the
// pool falls back to the defined main classifier.
type routedClassifier struct {
	main    rolemanager.Classifier
	pool    []RoutingCandidate
	jev     *jev.Client
	client  *http.Client
	onRetry func(resilience.Attempt)

	mu    sync.Mutex
	cache map[string]rolemanager.Classifier
}

func newRoutedClassifier(cfg Config, client *http.Client, onRetry func(resilience.Attempt)) *routedClassifier {
	return &routedClassifier{
		main:    classifierFromConfig(mainClassifierConfig(cfg), client, onRetry),
		pool:    cfg.Routing.Candidates,
		jev:     jev.New(cfg.Routing.JevToken),
		client:  client,
		onRetry: onRetry,
		cache:   map[string]rolemanager.Classifier{},
	}
}

func (r *routedClassifier) Classify(ctx context.Context, p rolemanager.ClassifierPayload) (string, error) {
	c := r.forUseCase(ctx, p.UseCase)
	return c.Classify(ctx, p)
}

// forUseCase returns the cached classifier for a use case, resolving it once
// through Jev on first use. The empty use case resolves as "main".
func (r *routedClassifier) forUseCase(ctx context.Context, useCase string) rolemanager.Classifier {
	if useCase == "" {
		useCase = rolemanager.UseCaseMain
	}
	r.mu.Lock()
	if c, ok := r.cache[useCase]; ok {
		r.mu.Unlock()
		return c
	}
	r.mu.Unlock()

	candidates := make([]jev.Candidate, len(r.pool))
	for i, pc := range r.pool {
		candidates[i] = jev.Candidate{
			Key:      pc.Key,
			Provider: pc.Cfg.Provider,
			Model:    pc.Cfg.Model,
		}
	}
	decision, err := r.jev.Route(ctx, useCase, candidates)
	if err != nil || decision.Key == "" {
		return r.cacheAndReturn(useCase, r.main)
	}
	for _, pc := range r.pool {
		if pc.Key == decision.Key {
			// A Jev Decisions model is not a chat model: a routed winner that
			// is a Jev model falls back to the main classifier rather than
			// ever being asked to chat.
			if jev.IsDecisionsModel(pc.Cfg.Provider, pc.Cfg.Model) {
				return r.cacheAndReturn(useCase, r.main)
			}
			return r.cacheAndReturn(useCase, classifierFromConfig(pc.Cfg, r.client, r.onRetry))
		}
	}
	return r.cacheAndReturn(useCase, r.main)
}

func (r *routedClassifier) cacheAndReturn(useCase string, c rolemanager.Classifier) rolemanager.Classifier {
	r.mu.Lock()
	r.cache[useCase] = c
	r.mu.Unlock()
	return c
}

func chatWithRetryAssistant(ctx context.Context, cfg Config, system, user string, client *http.Client, onRetry func(resilience.Attempt)) (Assistant, error) {
	return doChatWithPoolRetry(ctx, cfg, system, []Turn{{Role: "user", Content: user}}, client, nil, onRetry)
}

func doChatWithPoolRetry(ctx context.Context, cfg Config, system string, turns []Turn, client *http.Client, pool *nonce.Pool, onRetry func(resilience.Attempt)) (Assistant, error) {
	if pool == nil {
		pool = nonce.New()
	}
	turns = egressTurns(turns, pool)
	a, err := sendTurnsWithTools(ctx, cfg, system, turns, client, nil, nil, onRetry)
	if err != nil {
		return Assistant{}, err
	}
	return a, nil
}

// NewPipeline builds a rolemanager.Pipeline from the resolved classifier
// config: the classifier (reasoning off by default) plus the chunked
// classify-all bounds and an optional verdict cache.
func NewPipeline(cfg Config, client *http.Client, cache *rolemanager.Cache) *rolemanager.Pipeline {
	return NewPipelineWithRetry(cfg, client, cache, nil)
}

// NewPipelineWithRetry is NewPipeline with an onRetry callback forwarded to
// the underlying classifiers so retries are visible to observers.
func NewPipelineWithRetry(cfg Config, client *http.Client, cache *rolemanager.Cache, onRetry func(resilience.Attempt)) *rolemanager.Pipeline {
	cc := cfg.ClassifierOrDefault()
	guardLabel := cc.Provider + "/" + cc.Model
	// The role classifier serves the non-guardrail role-manager activities and
	// follows the routing config (defined: main; routed: Jev). The guardrail
	// classifier serves security and always follows classifier.provider/model.
	role := NewRoleClassifier(cfg, client, onRetry)
	guard := securityGuard(cfg, client, onRetry)
	p := rolemanager.NewPipelineWithChunk(role, rolemanager.ChunkConfig{
		MaxBytes:    cc.Chunk.MaxBytes,
		Concurrency: cc.Chunk.Concurrency,
	})
	p.Cache = cache
	p.SetSecurityModelLabel(guardLabel)
	if cfg.Security.Kind == "models" {
		var phase3 rolemanager.Classifier
		if cfg.Security.Phase3On {
			phase3 = guard
		}
		var sec rolemanager.Classifier
		if ml, err := buildSecurityClassifier(cfg.Security, phase3, guardLabel); err != nil {
			// Fail closed: a stack that failed to build is a classifier that
			// errors on every call, never a silent downgrade to the LLM path.
			sec = failingClassifier{err: err}
		} else {
			sec = ml
		}
		p.Security = sec
		p.SetMLSecurity(true)
		p.SetClassifierIdentity(mlclassify.OptionsIdentity(cfg.Security.Phase1, cfg.Security.Phase2, cfg.Security.Phase3On, cfg.Security.Phase2Deferred))
	} else {
		// LLM sentinel path: the guardrail is the classifier.provider/model
		// sentinel, separate from the role classifier.
		p.Security = guard
		p.SetMLSecurity(false)
	}
	return p
}

// securityGuard builds the guardrail security classifier. It follows
// classifier.provider/model: the full five-token LLM sentinel on the llm path,
// or phase 3 on the models path. When the classifier is a Jev Decisions model
// the guard sends its security checks to the Decisions API (one noul question
// per threat category) and falls back to the agent model — the chat
// classifier NewClassifierWithRetry already resolved to main — on an
// inconclusive verdict.
func securityGuard(cfg Config, client *http.Client, onRetry func(resilience.Attempt)) rolemanager.Classifier {
	cc := cfg.ClassifierOrDefault()
	guard := NewClassifierWithRetry(cfg, client, onRetry)
	if jev.IsDecisionsModel(cc.Provider, cc.Model) {
		guard = jev.NewSecurity(func() (string, error) { return cc.APIKey, nil }, guard)
	}
	return guard
}

// failingClassifier returns a fixed error on every call, so a security stack
// that failed to build fails closed rather than silently downgrading.
type failingClassifier struct{ err error }

func (f failingClassifier) Classify(context.Context, rolemanager.ClassifierPayload) (string, error) {
	return "", f.err
}

// buildSecurityClassifier builds the mlclassify classifier stack for a
// resolved security config. phase3 is the narrowed LLM sentinel, or nil.
func buildSecurityClassifier(sc SecurityClassifierConfig, phase3 rolemanager.Classifier, phase3Label string) (*mlclassify.Classifier, error) {
	opts := mlclassify.Options{
		Phase1:         sc.Phase1,
		Phase2:         sc.Phase2,
		Phase3:         phase3,
		Phase2Deferred: sc.Phase2Deferred,
		Phase3Label:    phase3Label,
		HFToken:        envHFToken,
	}
	return mlclassify.New(opts)
}

// PreloadClassifier eagerly builds the local ML classifier stack so a variant
// binary whose embedded model fails to load is a hard startup error rather
// than a silent downgrade. It is idempotent and cheap on repeat calls (models
// are cached); a non-models config is a no-op.
func PreloadClassifier(sc SecurityClassifierConfig) error {
	if sc.Kind != "models" {
		return nil
	}
	// Eagerly load and verify only embedded models. A variant binary whose
	// embedded model fails to load or verify is a hard startup error. Remote
	// phase models fetch their tokenizer at pipeline construction, never here,
	// so a no-classifier binary with a HuggingFace token does not block startup
	// on a network fetch.
	if sc.Phase1 != nil && sc.Phase1.Source != mlclassify.SourceEmbedded {
		sc.Phase1 = nil
	}
	if sc.Phase2 != nil && sc.Phase2.Source != mlclassify.SourceEmbedded {
		sc.Phase2 = nil
	}
	if sc.Phase1 == nil && sc.Phase2 == nil {
		return nil
	}
	_, err := buildSecurityClassifier(sc, nil, "")
	return err
}

// envHFToken resolves the HuggingFace token for remote phase models. Public
// models need no token, so a missing token is not an error here; the remote
// inference call surfaces a 401 if the model actually requires one.
func envHFToken() (string, error) {
	token, _, ok := EnvSource(os.Getenv).Lookup("huggingface", "api_key")
	if !ok {
		return "", nil
	}
	return token, nil
}

// hfTokenConfigured reports whether a HuggingFace token resolves from the
// environment. It gates the no-classifier phase-1 default: without a token the
// phase has no model and the /model row shows the configure hint.
func hfTokenConfigured() bool {
	token, _ := envHFToken()
	return token != ""
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
	blocks := []rolemanager.SystemBlock{{Source: rolemanager.SourceHarness, Content: sysText}}
	// The tool briefing is sealed as its own <tools> block. It is harness
	// text like the system block, but it describes a surface the harness
	// enforces elsewhere (the registry and the plan-mode gate), so keeping it
	// separately sealed means a forged tool list cannot ride in on the
	// system block's integrity hash.
	if toolsText := prompt.ToolsBlock(opts.Tools); toolsText != "" {
		blocks = append(blocks, rolemanager.SystemBlock{
			Source:  rolemanager.SourceHarness,
			Content: toolsText,
			Kind:    "tools",
		})
	}
	sealed, err := rolemanager.BuildSystemPrompt(blocks, pool)
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
		if cfg.API != "" && !provider.Builtin(cfg.Provider) {
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
		var streamOpts *wire.OpenAIStreamOptions
		if stream && d.usage {
			streamOpts = &wire.OpenAIStreamOptions{IncludeUsage: true}
		}

		// Repair any assistant turns whose tool_calls never received a matching
		// tool result in the stored transcript. Synthetic results are added to
		// the outbound payload only and are never written to session storage.
		// Tool results are otherwise sent exactly as they were recorded: the
		// history is only ever shortened by the agent's clearing step and by
		// compaction, never per request, so the prefix stays cacheable.
		turns = synthesizeDanglingToolResults(turns)

		switch d.kind {
		case kindWorkersAI:
			return p.NewWorkersAIRequest(cfg.Model, wire.WorkersAIRequest{
				Messages:  buildOpenAIMessages(system, turns, d.method),
				Stream:    stream,
				MaxTokens: cfg.MaxTokens,
				Tools:     openAITools,
			})
		case kindAnthropicMessages:
			maxTokens := maxTokensOr(cfg.MaxTokens, anthropicDefaultMaxTokens(cfg, stream))
			thinking, outputConfig := anthropicThinking(cfg, d, maxTokens)
			// Signed thinking is replayed only to the model that produced it,
			// and only while thinking is on for this request.
			replayFor := ""
			if thinking != nil || (d.thinking && models.Thinking(cfg.Provider, cfg.Model) == models.StyleAlways) {
				replayFor = ThinkingSource(cfg)
			}
			req := wire.AnthropicMessagesRequest{
				Model:        cfg.Model,
				MaxTokens:    maxTokens,
				System:       system,
				Messages:     buildAnthropicMessages(turns, replayFor),
				Stream:       stream,
				Tools:        anthropicTools,
				ToolChoice:   "auto",
				Thinking:     thinking,
				OutputConfig: outputConfig,
			}
			if d.cache {
				applyCacheBreakpoints(&req)
			}
			return d.messagesRequest(p, req)
		default:
			req := wire.OpenAIChatRequest{
				Model:           WireModel(cfg.Provider, cfg.Model),
				Messages:        buildOpenAIMessages(system, turns, d.method),
				Stream:          stream,
				MaxTokens:       cfg.MaxTokens,
				Tools:           openAITools,
				ToolChoice:      "auto",
				ReasoningEffort: reasoningEffort,
				StreamOptions:   streamOpts,
			}
			if req.MaxTokens <= 0 && stream {
				// Only a catalogue entry is trusted here: an OpenAI-compatible
				// relay may cap a model lower than its vendor does.
				req.MaxTokens = min(models.CatalogMaxOutput(cfg.Provider, cfg.Model), streamMaxTokens)
			}
			if d.maxCompletion {
				req.MaxCompletionTokens, req.MaxTokens = req.MaxTokens, 0
			}
			return d.chatRequest(p, req)
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
	// Thinking is the turn's signed thinking, in order, for replay on the
	// next request of a tool loop (see Turn.Thinking).
	Thinking []ThinkingBlock
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
	var thinking []ThinkingBlock
	for _, c := range ar.Content {
		switch c.Type {
		case "thinking":
			reasoning.WriteString(c.Thinking)
			thinking = append(thinking, ThinkingBlock{Thinking: c.Thinking, Signature: c.Signature})
		case "redacted_thinking":
			thinking = append(thinking, ThinkingBlock{Redacted: true, Data: c.Data})
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
	return Assistant{Text: b.String(), Reasoning: reasoning.String(), ToolCalls: calls, Usage: usage, StopReason: ar.StopReason, Thinking: thinking}, nil
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

// ClearedToolResult replaces a tool result the agent has cleared from the
// history to bound its context. The model is told the bytes are gone rather
// than handed a truncated head it might mistake for the whole file.
const ClearedToolResult = "[result cleared — re-Read if needed]"

// ClearToolResult replaces a tool turn's content with ClearedToolResult and
// drops the egress memo, so the next request seals the placeholder instead of
// the old bytes. It reports whether the turn changed. Only tool turns clear.
func (t *Turn) ClearToolResult() bool {
	if t.Role != "tool" || t.Content == ClearedToolResult {
		return false
	}
	t.Content = ClearedToolResult
	t.egrossed = ""
	return true
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

// buildAnthropicMessages renders turns as Anthropic messages. replayFor is the
// ThinkingSource whose signed thinking blocks are echoed back ahead of the
// turn's text and tool_use blocks; empty replays none. Thinking recorded
// under any other source is dropped: its signature would not verify.
func buildAnthropicMessages(turns []Turn, replayFor string) []wire.AnthropicMessage {
	msgs := make([]wire.AnthropicMessage, 0, len(turns))
	for _, t := range turns {
		switch t.Role {
		case "assistant":
			if t.Content == "" && len(t.ToolCalls) == 0 {
				continue
			}
			blocks := make([]wire.AnthropicRequestBlock, 0, 1+len(t.ToolCalls)+len(t.Thinking))
			if replayFor != "" && t.ThinkingModel == replayFor {
				for _, th := range t.Thinking {
					blocks = append(blocks, th.requestBlock())
				}
			}
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
	// PlanSentinel is how a plan-mode pass loop ended (empty in agent/goal
	// mode). It is kept separate from GoalSentinel so the two modes' verdicts
	// never share a field or a presentation.
	PlanSentinel rolemanager.PlanSentinel
	// PlanPath is the absolute filesystem path of the recorded plan file for
	// a plan-mode turn. Empty in agent/goal mode or when recording failed.
	PlanPath string
	// PlanName is the plan slug recorded from the prompt. Empty in agent/goal
	// mode or when recording failed.
	PlanName string
	// PlanText is the plan the model deliberately authored via the ExitPlanMode
	// plan argument. Empty when the turn exited plan mode through the
	// evaluator rather than an explicit ExitPlanMode call.
	PlanText string
	Passes   int
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

	dec, err := pipe.Admit(ctx, clean, "prompt", pol)
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

// dropIdleConns closes the client's idle pooled connections after a transport
// failure, so the retry dials fresh instead of reusing an HTTP/2 connection
// the peer just reset or sent GOAWAY on. In-flight streams are untouched. A
// cancelled turn is not a connection fault and leaves the pool alone.
func dropIdleConns(ctx context.Context, client *http.Client) {
	if client == nil || ctx.Err() != nil {
		return
	}
	client.CloseIdleConnections()
}

func roundTrip(ctx context.Context, client *http.Client, req *http.Request, cfg Config, redact func(string) string) ([]byte, int, error) {
	req = req.WithContext(ctx)
	calltrace.Apply(ctx, req.Header)
	resp, err := client.Do(req)
	if err != nil {
		dropIdleConns(ctx, client)
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
