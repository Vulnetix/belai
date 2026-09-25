package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/signet/internal/wire"
)

// DefaultMaxAgents is the default concurrency ceiling for fan-out subagents
// and background agents. It is intentionally generous (15) because modern
// LLM workloads are I/O-bound on tool results; raising the ceiling lets the
// harness run read-only exploration in parallel instead of queuing work.
const DefaultMaxAgents = 15

// DefaultMaxIterations is the per-pass tool-loop budget when
// resilience.max_iterations is unset. Every budget overflow costs a
// continuation directive plus an evaluator call and breaks the model's
// momentum, so the default is sized for a real task (reads, edits and a test
// run) rather than for a single lookup.
const DefaultMaxIterations = 40

// Settings holds user- and project-level configuration.
// Project settings override global settings field-by-field.
type Settings struct {
	// Provider is the default provider name.
	Provider string `json:"provider,omitempty"`
	// Model is the default model ID (e.g. "gpt-5", "claude-opus-4-5").
	Model string `json:"model,omitempty"`
	// Effort is the default effort/thinking level (e.g. "low", "medium", "high").
	Effort string `json:"effort,omitempty"`
	// Caveman, when non-nil, toggles the caveman voice rewrite.
	Caveman *bool `json:"caveman,omitempty"`
	// Guardrails, when non-nil and false, disables the posture gates. Default
	// true. The project layer may only tighten (turn them back on).
	Guardrails *bool `json:"guardrails,omitempty"`
	// AskPermission, when non-nil and false, disables the permission-ask gate:
	// an "ask" decision resolves to allow with no prompt. Default true. The
	// project layer may only tighten.
	AskPermission *bool `json:"ask_permission,omitempty"`
	// ReadOnly, when non-nil and true, is the master read-only switch:
	// mutating tools (Bash, Write, Edit) are not registered at all. nil or
	// false (the default) registers the full tool set.
	ReadOnly *bool `json:"read_only,omitempty"`
	// BashReadOnly is the deprecated alias for ReadOnly, accepted on read for
	// backward compatibility and folded into ReadOnly. It is never written.
	BashReadOnly *bool `json:"bash_readonly,omitempty"`
	// Permissions is the structured tool-permission rule set. The legacy flat
	// map form is still accepted on read but never written.
	Permissions PermissionRules `json:"permissions,omitempty"`
	// SessionRetentionDays is how long to keep idle sessions (default 28).
	SessionRetentionDays *int `json:"session_retention_days,omitempty"`
	// UI holds TUI presentation toggles.
	UI *UISettings `json:"ui,omitempty"`
	// ContextWindows overrides the built-in context-window size (in tokens)
	// for specific model ids. Use it for models Signet does not know.
	ContextWindows map[string]int `json:"context_windows,omitempty"`
	// ShowSessionNames toggles session names in the status bar (default on).
	ShowSessionNames *bool `json:"show_session_names,omitempty"`
	// Resilience controls provider retry and tool-loop budgets.
	Resilience *ResilienceSettings `json:"resilience,omitempty"`
	// Providers defines custom provider profiles, keyed by provider name.
	// Secrets never live here; they are resolved from api_key_env or the
	// credential backends.
	Providers map[string]ProviderProfile `json:"providers,omitempty"`
	// ProviderLabels maps a provider name (built-in or custom slug) to its
	// user-facing display label. It is the single source of truth for display
	// labels; the profile carries no display name. Labels are matched from
	// user input (CLI args and settings files) and rendered in the TUI, so
	// they are charset-restricted and uniqueness-checked in validation.
	ProviderLabels map[string]string `json:"provider_labels,omitempty"`
	// AllowProjectProviders opts in to project-layer provider definitions.
	// Defaults to false: a project file defining a provider is an API-key
	// exfiltration primitive, so it requires an explicit user opt-in.
	AllowProjectProviders *bool `json:"allow_project_providers,omitempty"`
	// AllowProjectWorkspaceDirs opts in to project-layer workspace directory
	// proposals. Defaults to false: a cloned repo could otherwise name
	// sensitive directories and silently widen the sandbox.
	AllowProjectWorkspaceDirs *bool `json:"allow_project_workspace_dirs,omitempty"`
	// Classifier configures the security classifier separately from the main
	// agent model. nil means reuse the main provider/model with reasoning off.
	Classifier *ClassifierSettings `json:"classifier,omitempty"`
	// Routing configures how the global provider/model serves the main turn
	// and the non-guardrail role-manager activities. nil means "defined": one
	// global provider/model serves everything.
	Routing *RoutingSettings `json:"routing,omitempty"`
	// LSP configures language-server diagnostics.
	LSP *LSPSettings `json:"lsp,omitempty"`
	// Sweep enables the background filesystem sweep for .vulnetix projects.
	VulnetixSweepEnabled *bool `json:"vulnetix_sweep_enabled,omitempty"`
	// SweepRoots restricts the sweep to a list of paths. Empty means $HOME and
	// the current workdir's parent.
	VulnetixSweepRoots []string `json:"vulnetix_sweep_roots,omitempty"`
	// Vulnetix holds per-project /vulnetix configuration. It is typed and
	// allowlisted so arbitrary argv can never be persisted here.
	Vulnetix *VulnetixSettings `json:"vulnetix,omitempty"`
	// WorkspaceDirs is a project-layer allowlist of additional directories that
	// may be added to sessions started in this project. If non-empty, only
	// directories in this list (and persisted to the project registry) are
	// attached; others are filtered out of the registry.
	WorkspaceDirs []string `json:"workspace_dirs,omitempty"`
	// UpdateCheck, when non-nil and false, disables the startup check for a
	// newer Signet release. Default true. SIGNET_NO_UPDATE_CHECK=1 overrides
	// it for a single run.
	UpdateCheck *bool `json:"update_check,omitempty"`
	// AutoCommitPerTask, when non-nil and true, commits each completed goal's
	// changed files as one conventional commit. Default false: committing is a
	// repository mutation and must be an explicit user opt-in. Global only: a
	// repo-visible project settings file must never be able to make the harness
	// commit, so the project layer is dropped in Resolve.
	AutoCommitPerTask *bool `json:"auto_commit_per_task,omitempty"`
	// TokenBudgets caps the tokens each provider+model may spend per session,
	// day or month. Global only: the project layer is dropped in Resolve.
	TokenBudgets []TokenBudget `json:"token_budgets,omitempty"`
}

// VulnetixSettings is the per-project /vulnetix configuration.
type VulnetixSettings struct {
	Subcommands     []string `json:"subcommands,omitempty"`
	Timeout         string   `json:"timeout,omitempty"`
	ContinueOnError *bool    `json:"continue_on_error,omitempty"`
	OrgID           string   `json:"org_id,omitempty"`
	// GatewayURL overrides the default Vulnetix AI Firewall host for self-hosted
	// deployments. Empty means https://guardrails.vulnetix.com.
	GatewayURL string `json:"gateway_url,omitempty"`
	// AutoFix opts `/vulnetix review` into `vulnetix fix --yes` after the SCA
	// scan. Default false: the review attaches a --dry-run plan instead of
	// mutating the tree without confirmation.
	AutoFix *bool `json:"autofix,omitempty"`
	// FirewallEnabled routes the session's LLM traffic through the Vulnetix AI
	// Firewall gateway. Default false.
	FirewallEnabled *bool `json:"firewall_enabled,omitempty"`
	// DepWatch runs the dependency-manifest hook: a manifest the session
	// changes is checked with the Vulnetix CLI when the change added or
	// updated dependencies. Default true; false turns the hook off.
	DepWatch *bool `json:"dep_watch,omitempty"`
}

// DepWatchEnabled reports whether the dependency-manifest hook runs. Default
// true.
func (s *VulnetixSettings) DepWatchEnabled() bool {
	return s == nil || s.DepWatch == nil || *s.DepWatch
}

// GatewayURLOrDefault returns the configured gateway URL, or the default.
func (s VulnetixSettings) GatewayURLOrDefault() string {
	if s.GatewayURL != "" {
		return s.GatewayURL
	}
	return "https://guardrails.vulnetix.com"
}

// AutoFixEnabled reports whether /vulnetix review may run `vulnetix fix --yes`
// unattended. Default false: the review attaches a --dry-run plan instead.
func (s VulnetixSettings) AutoFixEnabled() bool {
	return s.AutoFix != nil && *s.AutoFix
}

// UpdateCheckEnabled reports whether the startup release check may run.
// Default true.
func (s Settings) UpdateCheckEnabled() bool {
	return s.UpdateCheck == nil || *s.UpdateCheck
}

// AutoCommitPerTaskEnabled reports whether a completed goal is committed
// automatically. Default false: committing is a repo mutation and must be an
// explicit user opt-in.
func (s Settings) AutoCommitPerTaskEnabled() bool {
	return s.AutoCommitPerTask != nil && *s.AutoCommitPerTask
}

// SweepEnabled reports whether the vulnetix sweep is on. Default true.
func (s Settings) SweepEnabled() bool {
	return s.VulnetixSweepEnabled == nil || *s.VulnetixSweepEnabled
}

// SweepRoots returns the configured sweep roots, or nil for defaults.
func (s Settings) SweepRoots() []string {
	return s.VulnetixSweepRoots
}

// UnmarshalJSON accepts read_only (canonical) and bash_readonly (deprecated
// alias), folding the alias into ReadOnly when the canonical key is absent.
// The alias is never retained, so files written back emit read_only only.
func (s *Settings) UnmarshalJSON(data []byte) error {
	type alias Settings
	aux := struct {
		*alias
		BashReadOnly *bool `json:"bash_readonly,omitempty"`
	}{alias: (*alias)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if s.ReadOnly == nil && aux.BashReadOnly != nil {
		s.ReadOnly = aux.BashReadOnly
	}
	s.BashReadOnly = nil
	return nil
}

// ClassifierSettings configures the security classifier independently of the
// main agent model. The classifier's whole job is to emit a single sentinel
// token, so its effort defaults to "none" (reasoning off) regardless of the
// main model's effort.
type ClassifierSettings struct {
	// Kind selects the security classifier stack: "llm" (the full five-token
	// LLM sentinel) or "models" (local BERT gates plus an optional narrowed
	// phase-3 LLM sentinel). Empty derives from the build variant: "models"
	// when the binary embeds a model, else "llm".
	Kind string `json:"kind,omitempty"`
	// Provider is the classifier's provider; empty means the main provider.
	// On the models path it is phase 3's provider: phase 3 runs iff Provider
	// and Model are both explicitly set.
	Provider string `json:"provider,omitempty"`
	// Model is the classifier's model; empty means the main model. On the
	// models path it is phase 3's model.
	Model string `json:"model,omitempty"`
	// Effort is the classifier's reasoning effort; empty means "none".
	Effort string `json:"effort,omitempty"`
	// Tier picks which model answers the security guard when Provider and
	// Model are unset: "main" (default) keeps the main model, "fast" moves it
	// to the fast-tier model. A smaller guard is less robust against prompt
	// injection, so fast is an explicit opt-in. Classification still runs on
	// every required kind; only the answering model changes.
	Tier string `json:"tier,omitempty"`
	// Caveman, when non-nil and true, applies the caveman voice to the
	// classifier's prose payloads only — the compaction summary, the session
	// name, and the agent-profile designer. Sentinel payloads are never
	// voiced: their replies are parsed strictly, and a voice rewrite would
	// break the parse. It is independent of Settings.Caveman, which governs
	// the agent's own voice.
	Caveman *bool `json:"caveman,omitempty"`
	// Chunk bounds the chunked classify-all path for oversized payloads.
	Chunk ClassifierChunkSettings `json:"chunk,omitempty"`
	// Phase1 configures the prompt-saturation gate. Only consulted when Kind
	// is "models".
	Phase1 ClassifierPhaseSettings `json:"phase1,omitempty"`
	// Phase2 configures the jailbreak gate. Only consulted when Kind is
	// "models". Source "disabled" turns it off.
	Phase2 ClassifierPhaseSettings `json:"phase2,omitempty"`
}

// ClassifierPhaseSettings configures one local BERT gate.
type ClassifierPhaseSettings struct {
	// Model is the HuggingFace model id.
	Model string `json:"model,omitempty"`
	// Source is "embedded", "huggingface", or (phase 2 only) "disabled".
	Source string `json:"source,omitempty"`
	// Threshold is the attack-probability threshold at or above which the
	// gate fires. Zero means the default (mlclassify.DefaultThreshold, 0.75).
	Threshold float64 `json:"threshold,omitempty"`
}

// ClassifierChunkSettings bounds chunked classification of oversized content.
// Content over the threshold is split into overlapping chunks (so an injection
// straddling a boundary is still seen whole by one chunk) and classified
// concurrently; any non-SAFE verdict fails the whole content closed.
type ClassifierChunkSettings struct {
	// MaxBytes is the size over which content is chunked. Zero means 1 MiB.
	MaxBytes int `json:"max_bytes,omitempty"`
	// Concurrency caps how many chunks classify in parallel. Zero means 4.
	Concurrency int `json:"concurrency,omitempty"`
}

// merge folds from over c, taking any non-zero field from from.
func (c *ClassifierSettings) merge(from *ClassifierSettings) {
	if from == nil {
		return
	}
	if from.Kind != "" {
		c.Kind = from.Kind
	}
	if from.Provider != "" {
		c.Provider = from.Provider
	}
	if from.Model != "" {
		c.Model = from.Model
	}
	if from.Effort != "" {
		c.Effort = from.Effort
	}
	if from.Tier != "" {
		c.Tier = from.Tier
	}
	if from.Caveman != nil {
		c.Caveman = from.Caveman
	}
	if from.Chunk.MaxBytes != 0 {
		c.Chunk.MaxBytes = from.Chunk.MaxBytes
	}
	if from.Chunk.Concurrency != 0 {
		c.Chunk.Concurrency = from.Chunk.Concurrency
	}
	c.Phase1.merge(&from.Phase1)
	c.Phase2.merge(&from.Phase2)
}

// merge folds from over c, taking any non-zero field from from.
func (c *ClassifierPhaseSettings) merge(from *ClassifierPhaseSettings) {
	if from == nil {
		return
	}
	if from.Model != "" {
		c.Model = from.Model
	}
	if from.Source != "" {
		c.Source = from.Source
	}
	if from.Threshold != 0 {
		c.Threshold = from.Threshold
	}
}

// IsZero reports whether the classifier settings carry no overrides.
func (c *ClassifierSettings) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Kind == "" && c.Provider == "" && c.Model == "" && c.Effort == "" && c.Tier == "" &&
		c.Caveman == nil &&
		c.Chunk.MaxBytes == 0 && c.Chunk.Concurrency == 0 &&
		c.Phase1.IsZero() && c.Phase2.IsZero()
}

// IsZero reports whether the phase settings carry no overrides.
func (c *ClassifierPhaseSettings) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Model == "" && c.Source == "" && c.Threshold == 0
}

// MaxBytesOr returns the chunk threshold, defaulting to 1 MiB.
func (c ClassifierChunkSettings) MaxBytesOr() int {
	if c.MaxBytes <= 0 {
		return 1 << 20
	}
	return c.MaxBytes
}

// ConcurrencyOr returns the chunk concurrency, defaulting to 4.
func (c ClassifierChunkSettings) ConcurrencyOr() int {
	if c.Concurrency <= 0 {
		return 4
	}
	return c.Concurrency
}

// ProviderProfile is the JSON shape for one custom provider definition.
type ProviderProfile struct {
	BaseURL   string          `json:"base_url"`
	API       wire.Surface    `json:"api"`
	Auth      string          `json:"auth,omitempty"`        // bearer (default) | x-api-key | cf-aig
	APIKeyEnv string          `json:"api_key_env,omitempty"` // env var holding the key
	Models    []ProviderModel `json:"models,omitempty"`

	// Kind names the built-in descriptor this instance is templated from:
	// "ollama", "llama-server", or ""/"openai-compatible" for a generic
	// OpenAI-chat endpoint. It selects the list endpoint, the context-window
	// enrichment, local liveness probing and whether api_key is optional.
	Kind string `json:"kind,omitempty"`
	// Protocol is http | https. It is kept alongside Host/Port so the editor
	// and the default display label can be rebuilt without re-parsing a URL.
	// BaseURL stays the authoritative wire value.
	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
}

// ProviderModel is one model in a custom provider's catalogue.
type ProviderModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
}

// UISettings holds TUI presentation toggles.
type UISettings struct {
	Banner        *bool `json:"banner,omitempty"`
	StatusBar     *bool `json:"status_bar,omitempty"`
	KittyKeyboard *bool `json:"kitty_keyboard,omitempty"`
	Colors        *bool `json:"colors,omitempty"`
	Spinner       *bool `json:"spinner,omitempty"`
	ShowReasoning *bool `json:"show_reasoning,omitempty"`
	ShowToolCalls *bool `json:"show_tool_calls,omitempty"`
	ShowEdits     *bool `json:"show_edits,omitempty"`
	ShowTodos     *bool `json:"show_todos,omitempty"`
	Mouse         *bool `json:"mouse,omitempty"`
	// ShowInternalWork selects how much of the role manager's internal
	// decision-making is shown in the signet panel: "hidden", "decisions",
	// "security", or "all". It is display-only — every level runs exactly the
	// same gates.
	ShowInternalWork *string `json:"show_internal_work,omitempty"`
	// BudgetCycleSeconds is how long the footer shows one token budget before
	// cycling to the next (default 10, minimum 2). See BudgetCycle.
	BudgetCycleSeconds *int `json:"budget_cycle_seconds,omitempty"`
	// BudgetWarn prints a system line on each model call for the selected
	// model while any of its budgets is amber or red. Default off.
	BudgetWarn *bool `json:"budget_warn,omitempty"`
}

// merge folds from over u, taking any non-nil field from from. It is the
// single place the per-field UI merge lives, so a fifth field cannot be added
// to one merge path and forgotten in the other.
func (u *UISettings) merge(from *UISettings) {
	if from == nil {
		return
	}
	if from.Banner != nil {
		u.Banner = from.Banner
	}
	if from.StatusBar != nil {
		u.StatusBar = from.StatusBar
	}
	if from.KittyKeyboard != nil {
		u.KittyKeyboard = from.KittyKeyboard
	}
	if from.Colors != nil {
		u.Colors = from.Colors
	}
	if from.Spinner != nil {
		u.Spinner = from.Spinner
	}
	if from.ShowReasoning != nil {
		u.ShowReasoning = from.ShowReasoning
	}
	if from.ShowToolCalls != nil {
		u.ShowToolCalls = from.ShowToolCalls
	}
	if from.ShowEdits != nil {
		u.ShowEdits = from.ShowEdits
	}
	if from.ShowTodos != nil {
		u.ShowTodos = from.ShowTodos
	}
	if from.Mouse != nil {
		u.Mouse = from.Mouse
	}
	if from.ShowInternalWork != nil {
		u.ShowInternalWork = from.ShowInternalWork
	}
	if from.BudgetCycleSeconds != nil {
		u.BudgetCycleSeconds = from.BudgetCycleSeconds
	}
	if from.BudgetWarn != nil {
		u.BudgetWarn = from.BudgetWarn
	}
}

// ResilienceSettings controls the provider retry and agent-loop budgets.
// A zero value means "use the built-in default".
type ResilienceSettings struct {
	// MaxAttempts is the inclusive pre-first-byte retry budget per model
	// call. It defaults to 3.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// MaxIterations is the per-prompt tool-loop budget. It defaults to 10.
	MaxIterations int `json:"max_iterations,omitempty"`
	// MaxPasses is the goal-mode pass-loop ceiling. Zero means unbounded;
	// the default honours that, and the setting exists for CI and for anyone
	// who wants a hard ceiling.
	MaxPasses int `json:"max_passes,omitempty"`
	// MaxClarifyRounds bounds the explore→clarify→explore loop. Zero means the
	// default (3); a negative value disables clarification entirely.
	MaxClarifyRounds int `json:"max_clarify_rounds,omitempty"`
	// MaxExploreIterations bounds the tool-loop budget of a single explore
	// subagent (the agentic explore phase). It defaults to 8, deeper than the
	// historical 4 so a subagent actually runs rg/find/git before clarifying,
	// while still keeping the fan-out bounded. Zero means the default (8).
	MaxExploreIterations int `json:"max_explore_iterations,omitempty"`
	// MaxProcessRecoveries bounds how many times a supervised process may be
	// restarted by the recovery subagent before it is marked failed. Zero
	// means the default (3).
	MaxProcessRecoveries int `json:"max_process_recoveries,omitempty"`
	// MaxAgents caps how many fan-out subagents (explore plus background
	// agents) run at once across the whole session. It defaults to 15. Zero
	// means the default (15); a higher value requires provider rate-limit
	// headroom. It is intentionally overridable per-project (unlike retry
	// budgets), because concurrency is a local performance preference, not a
	// safety budget.
	MaxAgents int `json:"max_agents,omitempty"`
	// PlanExplore, when true, runs the plan-mode repository survey before the
	// first planning pass. nil means off (the default): the survey held the
	// planner back for minutes on a small prompt, and the planner's own
	// passes read what they need.
	PlanExplore *bool `json:"plan_explore,omitempty"`
	// GoalExplore, when true, launches the explore fan-out before a goal
	// whose prompt carries references. nil means off (the default): the goal
	// loop's own first pass reads what it needs, and a pre-flight survey held
	// that pass back for minutes only for the goal to re-read the same files.
	GoalExplore *bool `json:"goal_explore,omitempty"`
}

// RoutingKind values for RoutingSettings.Kind. "defined" is the default: one
// global provider/model serves the main turn and every non-guardrail
// role-manager activity. "routed" asks the Jev routing activity to select a
// UseCases entry per use case.
const (
	RoutingDefined = "defined"
	RoutingRouted  = "routed"

	// ModeDetectionAuto, ModeDetectionJev and ModeDetectionLlm select the
	// intent-detection backend. Auto uses Jev only when the user already
	// sends traffic to OpenRouter/Jev.
	ModeDetectionAuto = "auto"
	ModeDetectionJev  = "jev"
	ModeDetectionLlm  = "llm"

	// ClassifierTierMain and ClassifierTierFast are the classifier.tier values.
	ClassifierTierMain = "main"
	ClassifierTierFast = "fast"
)

// RoutingTarget is one provider/model pair a routed use case may resolve to.
// At least one of Provider/Model must be set; a missing field inherits the
// global value at resolution time.
type RoutingTarget struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// RoutingSettings configures how the global provider/model serves the main
// turn and the non-guardrail role-manager activities.
type RoutingSettings struct {
	// Kind selects the routing mode. Empty means "defined".
	Kind string `json:"kind,omitempty"`
	// UseCases maps a semantic use case to its provider/model candidate.
	// Known keys: "main", "mode_eval", "goal_eval", "plan_eval",
	// "goal_contract", "clarify", "compaction", "session_name". Only
	// consulted when Kind == "routed".
	UseCases map[string]RoutingTarget `json:"use_cases,omitempty"`
	// Fast is the fast-tier model: the provider/model that answers the
	// one-token sentinel roles (mode select, session name, goal and plan
	// evaluator verdicts) when no routing candidate does. Unset fields fall
	// back to the main provider and that provider's registry fast model. It
	// may name a different provider than the main model.
	Fast *RoutingTarget `json:"fast_model,omitempty"`
	// ModeDetection selects the intent-detection backend. "auto" (default)
	// uses the Jev detector when the user already sends traffic to
	// OpenRouter/Jev; otherwise it falls back to the LLM classifier.
	// "jev" always uses Jev when a key is available; "llm" always uses the
	// LLM classifier.
	ModeDetection string `json:"mode_detection,omitempty"`
}

// merge folds from over r, taking any non-zero field from from. UseCases merge
// key-by-key so a project layer can add one candidate without restating the
// global map.
func (r *RoutingSettings) merge(from *RoutingSettings) {
	if from == nil {
		return
	}
	if from.Kind != "" {
		r.Kind = from.Kind
	}
	if from.Fast != nil {
		f := *from.Fast
		r.Fast = &f
	}
	if from.UseCases != nil {
		if r.UseCases == nil {
			r.UseCases = map[string]RoutingTarget{}
		}
		for k, v := range from.UseCases {
			r.UseCases[k] = v
		}
	}
}

// IsZero reports whether the routing settings carry no overrides.
func (r *RoutingSettings) IsZero() bool {
	return r == nil || (r.Kind == "" && len(r.UseCases) == 0 && r.Fast == nil)
}

// MaxAttemptsOr returns MaxAttempts or the provided default.
func (r *ResilienceSettings) MaxAttemptsOr(def int) int {
	if r == nil || r.MaxAttempts == 0 {
		return def
	}
	return r.MaxAttempts
}

// MaxIterationsOr returns MaxIterations or the provided default.
func (r *ResilienceSettings) MaxIterationsOr(def int) int {
	if r == nil || r.MaxIterations == 0 {
		return def
	}
	return r.MaxIterations
}

// MaxPassesOr returns MaxPasses, or 0 (unbounded) when unset.
func (r *ResilienceSettings) MaxPassesOr() int {
	if r == nil {
		return 0
	}
	return r.MaxPasses
}

// MaxClarifyRoundsOr returns MaxClarifyRounds or the provided default. Zero
// means "use the default"; callers should pass the built-in default.
func (r *ResilienceSettings) MaxClarifyRoundsOr(def int) int {
	if r == nil || r.MaxClarifyRounds == 0 {
		return def
	}
	return r.MaxClarifyRounds
}

// MaxExploreIterationsOr returns MaxExploreIterations or the provided default.
// Zero means "use the default"; callers should pass the built-in default (8).
func (r *ResilienceSettings) MaxExploreIterationsOr(def int) int {
	if r == nil || r.MaxExploreIterations == 0 {
		return def
	}
	return r.MaxExploreIterations
}

// MaxProcessRecoveriesOr returns MaxProcessRecoveries or the provided default.
// Zero means "use the default"; callers should pass the built-in default (3).
func (r *ResilienceSettings) MaxProcessRecoveriesOr(def int) int {
	if r == nil || r.MaxProcessRecoveries == 0 {
		return def
	}
	return r.MaxProcessRecoveries
}

// MaxAgentsOr returns MaxAgents or the provided default. Zero means "use the
// default"; callers should pass the built-in default (DefaultMaxAgents).
func (r *ResilienceSettings) MaxAgentsOr(def int) int {
	if r == nil || r.MaxAgents == 0 {
		return def
	}
	return r.MaxAgents
}

// merge folds another ResilienceSettings into this one. Safety budgets
// (retry and iteration limits) tighten-only; MaxAgents is a performance
// preference and may be raised; PlanExplore and GoalExplore are replaced when
// explicitly set.
func (r *ResilienceSettings) merge(other *ResilienceSettings) {
	if other == nil {
		return
	}
	tighten := func(cur, val *int) {
		if *val == 0 {
			return
		}
		if *cur == 0 {
			*cur = *val
			return
		}
		*cur = min(*cur, *val)
	}
	tighten(&r.MaxAttempts, &other.MaxAttempts)
	tighten(&r.MaxIterations, &other.MaxIterations)
	tighten(&r.MaxPasses, &other.MaxPasses)
	// MaxClarifyRounds is tighten-only for positive values; a negative value
	// disables clarification and overrides any earlier positive or negative
	// value, because disabling is an explicit opt-out.
	if other.MaxClarifyRounds != 0 {
		if r.MaxClarifyRounds == 0 || other.MaxClarifyRounds < 0 || (r.MaxClarifyRounds > 0 && other.MaxClarifyRounds < r.MaxClarifyRounds) {
			r.MaxClarifyRounds = other.MaxClarifyRounds
		}
	}
	tighten(&r.MaxExploreIterations, &other.MaxExploreIterations)
	tighten(&r.MaxProcessRecoveries, &other.MaxProcessRecoveries)
	// MaxAgents is intentionally overridable upward.
	if other.MaxAgents != 0 {
		r.MaxAgents = other.MaxAgents
	}
	if other.PlanExplore != nil {
		r.PlanExplore = other.PlanExplore
	}
	if other.GoalExplore != nil {
		r.GoalExplore = other.GoalExplore
	}
}

// ColorsEnabled reports whether role colours are on. Default true.
func (s Settings) ColorsEnabled() bool {
	return s.UI == nil || s.UI.Colors == nil || *s.UI.Colors
}

// SpinnerEnabled reports whether the work-indicator spinner is on. Default
// true.
func (s Settings) SpinnerEnabled() bool {
	return s.UI == nil || s.UI.Spinner == nil || *s.UI.Spinner
}

// PlanExploreEnabled reports whether plan-mode's repository survey runs.
// Default false; only an explicit true enables it.
func (s Settings) PlanExploreEnabled() bool {
	return s.Resilience != nil && s.Resilience.PlanExplore != nil && *s.Resilience.PlanExplore
}

// GoalExploreEnabled reports whether a goal with references is surveyed by the
// explore fan-out before its first pass. Default false; only an explicit true
// enables it. The not-started goal survey is unaffected.
func (s Settings) GoalExploreEnabled() bool {
	return s.Resilience != nil && s.Resilience.GoalExplore != nil && *s.Resilience.GoalExplore
}

// ReasoningVisible reports whether reasoning deltas render. Default false.
func (s Settings) ReasoningVisible() bool {
	return s.UI != nil && s.UI.ShowReasoning != nil && *s.UI.ShowReasoning
}

// InternalWorkLevel returns the role-manager display granularity as a
// normalised name: "hidden", "decisions", "security", or "all". The default
// is hidden, and an unrecognised value reads as hidden (fail closed on
// display). It is display-only: every level runs exactly the same gates.
func (s Settings) InternalWorkLevel() string {
	if s.UI == nil || s.UI.ShowInternalWork == nil {
		return "hidden"
	}
	switch strings.ToLower(strings.TrimSpace(*s.UI.ShowInternalWork)) {
	case "decisions", "security", "all":
		return strings.ToLower(strings.TrimSpace(*s.UI.ShowInternalWork))
	default:
		return "hidden"
	}
}

// ToolCallsVisible reports whether tool-call rows render. Default true.
func (s Settings) ToolCallsVisible() bool {
	return s.UI == nil || s.UI.ShowToolCalls == nil || *s.UI.ShowToolCalls
}

// EditsVisible reports whether Write/Edit rows render. Default true. It is
// independent of ToolCallsVisible: turning tool chatter off keeps the file
// diffs, and turning edits off keeps the rest of the tool activity.
func (s Settings) EditsVisible() bool {
	return s.UI == nil || s.UI.ShowEdits == nil || *s.UI.ShowEdits
}

// TodosVisible reports whether the TODO panel renders. Default true.
func (s Settings) TodosVisible() bool {
	return s.UI == nil || s.UI.ShowTodos == nil || *s.UI.ShowTodos
}

// MouseEnabled reports whether the TUI captures the mouse. Default true.
func (s Settings) MouseEnabled() bool {
	return s.UI == nil || s.UI.Mouse == nil || *s.UI.Mouse
}

// SessionRetention returns the retention duration, defaulting to 28 days.
func (s Settings) SessionRetention() int {
	if s.SessionRetentionDays != nil {
		return *s.SessionRetentionDays
	}
	return 28
}

// SessionNamesVisible reports whether session names should be shown in the
// status bar. The default is true.
func (s Settings) SessionNamesVisible() bool {
	return s.ShowSessionNames == nil || *s.ShowSessionNames
}

// AllowProjectProvidersEnabled reports whether the global opt-in for
// project-layer provider definitions is set.
func (s Settings) AllowProjectProvidersEnabled() bool {
	return s.AllowProjectProviders != nil && *s.AllowProjectProviders
}

// AllowProjectWorkspaceDirsEnabled reports whether the global opt-in for
// project-layer workspace directory proposals is set.
func (s Settings) AllowProjectWorkspaceDirsEnabled() bool {
	return s.AllowProjectWorkspaceDirs != nil && *s.AllowProjectWorkspaceDirs
}

// LabelFor returns the user-facing display label for a provider name, or the
// name itself when no label is configured. It is the single read path the TUI
// uses to render provider labels.
func (s Settings) LabelFor(name string) string {
	if label, ok := s.ProviderLabels[strings.ToLower(strings.TrimSpace(name))]; ok && label != "" {
		return label
	}
	return name
}

// CanonicalProvider resolves a user-facing display label (or a slug, or a
// built-in name) back to the canonical provider slug. It is case-insensitive
// and trims surrounding whitespace. The second return reports whether the
// label matched a configured label.
func (s Settings) CanonicalProvider(label string) (string, bool) {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return "", false
	}
	for name, disp := range s.ProviderLabels {
		if strings.ToLower(strings.TrimSpace(disp)) == l {
			return name, true
		}
	}
	return "", false
}

// CavemanEnabled reports whether the caveman voice rewrite is active. The
// default (nil or false) is off.
func (s Settings) CavemanEnabled() bool {
	return s.Caveman != nil && *s.Caveman
}

// CatalogWindow returns the context-window size a custom provider profile
// declares for a model, or 0 when the provider or the model is not in the
// catalogue. It is the fallback between the user's explicit
// `context_windows` override and the built-in modelinfo registry, so a model
// that only exists in a provider profile still has a known window.
func (s Settings) CatalogWindow(provider, model string) int {
	prof, ok := s.Providers[provider]
	if !ok {
		return 0
	}
	for _, m := range prof.Models {
		if m.ID == model {
			return m.ContextWindow
		}
	}
	return 0
}

// ClassifierCavemanEnabled reports whether the classifier's prose payloads —
// the compaction summary, the session name, and the agent-profile designer —
// use the caveman voice. The default (nil or false) is off. It is independent
// of CavemanEnabled, which governs the agent's own voice, and it never reaches
// a sentinel payload.
func (s Settings) ClassifierCavemanEnabled() bool {
	return s.Classifier != nil && s.Classifier.Caveman != nil && *s.Classifier.Caveman
}

// GuardrailsEnabled reports whether the posture gates are on. Default on.
func (s Settings) GuardrailsEnabled() bool {
	return s.Guardrails == nil || *s.Guardrails
}

// AskPermissionEnabled reports whether the permission-ask gate is on. Default on.
func (s Settings) AskPermissionEnabled() bool {
	return s.AskPermission == nil || *s.AskPermission
}

// FirewallEnabled reports whether the Vulnetix AI Firewall is turned on.
// Default false: routing prompts to a third-party gateway is opt-in.
func (s Settings) FirewallEnabled() bool {
	return s.Vulnetix != nil && s.Vulnetix.FirewallEnabled != nil && *s.Vulnetix.FirewallEnabled
}

// ReadOnlyEnabled reports whether the master read-only switch is on. The
// default (nil or false) is off: the full tool set, including mutating tools.
func (s Settings) ReadOnlyEnabled() bool {
	if s.ReadOnly != nil {
		return *s.ReadOnly
	}
	return s.BashReadOnly != nil && *s.BashReadOnly
}

// Override merges project settings over the receiver (which should be the
// global settings). It returns the merged result and never mutates the
// receiver. Non-zero project fields win; nil/empty project fields fall back
// to the global value. Permissions and ContextWindows merge key-by-key (union),
// never replace, so a cloned project file cannot widen permissions.
func (s Settings) Override(proj Settings) Settings {
	out := s
	if proj.Model != "" {
		out.Model = proj.Model
	}
	if proj.Provider != "" {
		out.Provider = proj.Provider
	}
	if proj.Effort != "" {
		out.Effort = proj.Effort
	}
	if proj.Caveman != nil {
		out.Caveman = proj.Caveman
	}
	if proj.Guardrails != nil && *proj.Guardrails {
		t := true
		out.Guardrails = &t
	}
	if proj.AskPermission != nil && *proj.AskPermission {
		t := true
		out.AskPermission = &t
	}
	// A project-layer settings file may turn the firewall off but never on.
	// A repo must not be able to redirect prompts to a gateway by shipping a
	// .vulnetix/signet/settings.json.
	if proj.Vulnetix != nil && proj.Vulnetix.FirewallEnabled != nil && !*proj.Vulnetix.FirewallEnabled {
		f := false
		if out.Vulnetix == nil {
			out.Vulnetix = &VulnetixSettings{}
		}
		out.Vulnetix.FirewallEnabled = &f
	}
	// Likewise the dependency hook: a project file may turn it on, never off.
	if proj.Vulnetix != nil && proj.Vulnetix.DepWatch != nil && *proj.Vulnetix.DepWatch {
		t := true
		if out.Vulnetix == nil {
			out.Vulnetix = &VulnetixSettings{}
		} else {
			v := *out.Vulnetix
			out.Vulnetix = &v
		}
		out.Vulnetix.DepWatch = &t
	}
	if proj.BashReadOnly != nil || proj.ReadOnly != nil {
		if proj.ReadOnly != nil {
			out.ReadOnly = proj.ReadOnly
		}
		if proj.BashReadOnly != nil {
			// Deprecated alias: read_only wins when both are present.
			if out.ReadOnly == nil {
				out.ReadOnly = proj.BashReadOnly
			}
		}
		out.BashReadOnly = nil
	}
	out.Permissions = out.Permissions.Merge(proj.Permissions)
	if proj.SessionRetentionDays != nil {
		out.SessionRetentionDays = proj.SessionRetentionDays
	}
	if proj.UI != nil {
		merged := &UISettings{}
		if out.UI != nil {
			*merged = *out.UI
		}
		merged.merge(proj.UI)
		out.UI = merged
	}
	if proj.ContextWindows != nil {
		merged := make(map[string]int, len(out.ContextWindows)+len(proj.ContextWindows))
		for k, v := range out.ContextWindows {
			merged[k] = v
		}
		for k, v := range proj.ContextWindows {
			merged[k] = v
		}
		out.ContextWindows = merged
	}
	if proj.Providers != nil {
		merged := make(map[string]ProviderProfile, len(out.Providers)+len(proj.Providers))
		for k, v := range out.Providers {
			merged[k] = v
		}
		for k, v := range proj.Providers {
			merged[k] = v
		}
		out.Providers = merged
	}
	if proj.ProviderLabels != nil {
		merged := make(map[string]string, len(out.ProviderLabels)+len(proj.ProviderLabels))
		for k, v := range out.ProviderLabels {
			merged[k] = v
		}
		for k, v := range proj.ProviderLabels {
			merged[k] = v
		}
		out.ProviderLabels = merged
	}
	if proj.ShowSessionNames != nil {
		out.ShowSessionNames = proj.ShowSessionNames
	}
	if proj.UpdateCheck != nil {
		out.UpdateCheck = proj.UpdateCheck
	}
	if proj.Classifier != nil {
		merged := &ClassifierSettings{}
		if out.Classifier != nil {
			*merged = *out.Classifier
		}
		merged.merge(proj.Classifier)
		out.Classifier = merged
	}
	if proj.LSP != nil {
		merged := &LSPSettings{}
		if out.LSP != nil {
			*merged = *out.LSP
		}
		// Servers from the project layer are arbitrary code execution; drop
		// them unconditionally. The global layer has already been merged.
		if len(proj.LSP.Servers) > 0 {
			proj.LSP.Servers = nil
		}
		// Tighten-only merge for booleans.
		if proj.LSP.Enabled != nil && !*proj.LSP.Enabled {
			merged.Enabled = proj.LSP.Enabled
		}
		if proj.LSP.Fallback != nil && !*proj.LSP.Fallback {
			merged.Fallback = proj.LSP.Fallback
		}
		if proj.LSP.ClassifyDiagnostics != nil && *proj.LSP.ClassifyDiagnostics {
			merged.ClassifyDiagnostics = proj.LSP.ClassifyDiagnostics
		}
		if proj.LSP.TimeoutMS != 0 {
			if merged.TimeoutMS == 0 {
				merged.TimeoutMS = proj.LSP.TimeoutMS
			} else {
				merged.TimeoutMS = min(merged.TimeoutMS, proj.LSP.TimeoutMS)
			}
		}
		if proj.LSP.MaxDiagnostics != 0 {
			if merged.MaxDiagnostics == 0 {
				merged.MaxDiagnostics = proj.LSP.MaxDiagnostics
			} else {
				merged.MaxDiagnostics = min(merged.MaxDiagnostics, proj.LSP.MaxDiagnostics)
			}
		}
		if len(proj.LSP.Languages) > 0 {
			if merged.Languages == nil {
				merged.Languages = map[string]bool{}
			}
			for k, v := range proj.LSP.Languages {
				// Only false entries tighten; a project may not opt a language
				// in because that would run repo-chosen tooling unattended.
				if !v {
					merged.Languages[k] = false
				}
			}
		}
		out.LSP = merged
	}
	if proj.Resilience != nil {
		merged := &ResilienceSettings{}
		if out.Resilience != nil {
			*merged = *out.Resilience
		}
		merged.merge(proj.Resilience)
		out.Resilience = merged
	}
	if proj.Routing != nil {
		merged := &RoutingSettings{}
		if out.Routing != nil {
			*merged = *out.Routing
		}
		merged.merge(proj.Routing)
		out.Routing = merged
	}
	if proj.WorkspaceDirs != nil {
		out.WorkspaceDirs = proj.WorkspaceDirs
	}
	return out
}

// LoadGlobal reads the global settings file (~/.signet/settings.json).
// A missing file yields zero-value settings with no error.
func LoadGlobal() (Settings, error) {
	path, err := GlobalSettingsPath()
	if err != nil {
		return Settings{}, err
	}
	return loadSettings(path)
}

// LoadProject reads the project settings file (<workdir>/.vulnetix/settings.json).
// A missing file yields zero-value settings with no error.
func LoadProject(workdir string) (Settings, error) {
	return loadSettings(ProjectSettingsPath(workdir))
}

// LoadMerged returns global settings with project settings overriding them.
func LoadMerged(workdir string) (Settings, error) {
	global, err := LoadGlobal()
	if err != nil {
		return Settings{}, err
	}
	proj, err := LoadProject(workdir)
	if err != nil {
		return Settings{}, err
	}
	return global.Override(proj), nil
}

func loadSettings(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings %s: %w", path, err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}, fmt.Errorf("parse settings %s: %w", path, err)
	}
	return s, nil
}

// SaveGlobal writes settings to ~/.signet/settings.json, creating directories
// as needed.
func SaveGlobal(s Settings) error {
	path, err := GlobalSettingsPath()
	if err != nil {
		return err
	}
	return saveSettings(path, s)
}

// SaveProject writes settings to <workdir>/.vulnetix/settings.json, creating
// directories as needed.
func SaveProject(workdir string, s Settings) error {
	return saveSettings(ProjectSettingsPath(workdir), s)
}

func saveSettings(path string, s Settings) error {
	mode := os.FileMode(0o755)
	if gd, _ := GlobalDir(); filepath.Dir(path) == gd {
		mode = 0o700
	}
	if err := os.MkdirAll(filepath.Dir(path), mode); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write settings %s: %w", path, err)
	}
	return nil
}
