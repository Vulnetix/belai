package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vulnetix/signet/internal/wire"
)

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
	// FirewallEnabled routes the session's LLM traffic through the Vulnetix AI
	// Firewall gateway. Default false.
	FirewallEnabled *bool `json:"firewall_enabled,omitempty"`
}

// GatewayURLOrDefault returns the configured gateway URL, or the default.
func (s VulnetixSettings) GatewayURLOrDefault() string {
	if s.GatewayURL != "" {
		return s.GatewayURL
	}
	return "https://guardrails.vulnetix.com"
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
	// Provider is the classifier's provider; empty means the main provider.
	Provider string `json:"provider,omitempty"`
	// Model is the classifier's model; empty means the main model.
	Model string `json:"model,omitempty"`
	// Effort is the classifier's reasoning effort; empty means "none".
	Effort string `json:"effort,omitempty"`
	// Caveman, when non-nil and true, applies the caveman voice to the
	// classifier's prose payloads only — the compaction summary, the session
	// name, and the agent-profile designer. Sentinel payloads are never
	// voiced: their replies are parsed strictly, and a voice rewrite would
	// break the parse. It is independent of Settings.Caveman, which governs
	// the agent's own voice.
	Caveman *bool `json:"caveman,omitempty"`
	// Chunk bounds the chunked classify-all path for oversized payloads.
	Chunk ClassifierChunkSettings `json:"chunk,omitempty"`
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
	if from.Provider != "" {
		c.Provider = from.Provider
	}
	if from.Model != "" {
		c.Model = from.Model
	}
	if from.Effort != "" {
		c.Effort = from.Effort
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
}

// IsZero reports whether the classifier settings carry no overrides.
func (c *ClassifierSettings) IsZero() bool {
	if c == nil {
		return true
	}
	return c.Provider == "" && c.Model == "" && c.Effort == "" &&
		c.Caveman == nil &&
		c.Chunk.MaxBytes == 0 && c.Chunk.Concurrency == 0
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
	ShowTodos     *bool `json:"show_todos,omitempty"`
	Mouse         *bool `json:"mouse,omitempty"`
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
	if from.ShowTodos != nil {
		u.ShowTodos = from.ShowTodos
	}
	if from.Mouse != nil {
		u.Mouse = from.Mouse
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
	// agents) run at once across the whole session. It defaults to 3, today's
	// exploreConcurrency, and backs the single FIFO agent pool. Zero means the
	// default (3).
	MaxAgents int `json:"max_agents,omitempty"`
	// PlanExplore, when non-nil, toggles the plan-mode repository survey. nil
	// means on (the default), so false is honoured as an explicit opt-out.
	PlanExplore *bool `json:"plan_explore,omitempty"`
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
// default"; callers should pass the built-in default (3).
func (r *ResilienceSettings) MaxAgentsOr(def int) int {
	if r == nil || r.MaxAgents == 0 {
		return def
	}
	return r.MaxAgents
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
// Default true; only an explicit false disables it, so false is honoured as a
// deliberate opt-out rather than lost to the nil-means-default convention.
func (s Settings) PlanExploreEnabled() bool {
	return s.Resilience == nil || s.Resilience.PlanExplore == nil || *s.Resilience.PlanExplore
}

// ReasoningVisible reports whether reasoning deltas render. Default false.
func (s Settings) ReasoningVisible() bool {
	return s.UI != nil && s.UI.ShowReasoning != nil && *s.UI.ShowReasoning
}

// ToolCallsVisible reports whether tool-call rows render. Default true.
func (s Settings) ToolCallsVisible() bool {
	return s.UI == nil || s.UI.ShowToolCalls == nil || *s.UI.ShowToolCalls
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
	if proj.ShowSessionNames != nil {
		out.ShowSessionNames = proj.ShowSessionNames
	}
	if proj.Classifier != nil {
		merged := &ClassifierSettings{}
		if out.Classifier != nil {
			*merged = *out.Classifier
		}
		merged.merge(proj.Classifier)
		out.Classifier = merged
	}
	if proj.Resilience != nil {
		merged := &ResilienceSettings{}
		if out.Resilience != nil {
			*merged = *out.Resilience
		}
		if proj.Resilience.MaxAttempts != 0 {
			if merged.MaxAttempts == 0 {
				merged.MaxAttempts = proj.Resilience.MaxAttempts
			} else {
				// Project settings cannot widen the budget; take the minimum.
				merged.MaxAttempts = min(merged.MaxAttempts, proj.Resilience.MaxAttempts)
			}
		}
		if proj.Resilience.MaxIterations != 0 {
			if merged.MaxIterations == 0 {
				merged.MaxIterations = proj.Resilience.MaxIterations
			} else {
				merged.MaxIterations = min(merged.MaxIterations, proj.Resilience.MaxIterations)
			}
		}
		if proj.Resilience.MaxPasses != 0 {
			if merged.MaxPasses == 0 {
				merged.MaxPasses = proj.Resilience.MaxPasses
			} else {
				merged.MaxPasses = min(merged.MaxPasses, proj.Resilience.MaxPasses)
			}
		}
		if proj.Resilience.MaxClarifyRounds != 0 {
			if merged.MaxClarifyRounds == 0 {
				merged.MaxClarifyRounds = proj.Resilience.MaxClarifyRounds
			} else {
				merged.MaxClarifyRounds = min(merged.MaxClarifyRounds, proj.Resilience.MaxClarifyRounds)
			}
		}
		if proj.Resilience.MaxExploreIterations != 0 {
			if merged.MaxExploreIterations == 0 {
				merged.MaxExploreIterations = proj.Resilience.MaxExploreIterations
			} else {
				merged.MaxExploreIterations = min(merged.MaxExploreIterations, proj.Resilience.MaxExploreIterations)
			}
		}
		if proj.Resilience.MaxProcessRecoveries != 0 {
			if merged.MaxProcessRecoveries == 0 {
				merged.MaxProcessRecoveries = proj.Resilience.MaxProcessRecoveries
			} else {
				merged.MaxProcessRecoveries = min(merged.MaxProcessRecoveries, proj.Resilience.MaxProcessRecoveries)
			}
		}
		if proj.Resilience.MaxAgents != 0 {
			if merged.MaxAgents == 0 {
				merged.MaxAgents = proj.Resilience.MaxAgents
			} else {
				merged.MaxAgents = min(merged.MaxAgents, proj.Resilience.MaxAgents)
			}
		}
		if proj.Resilience.PlanExplore != nil {
			merged.PlanExplore = proj.Resilience.PlanExplore
		}
		out.Resilience = merged
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
