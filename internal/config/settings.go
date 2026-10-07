package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/wire"
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
	// for specific model ids. Use it for models Belai does not know.
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
	// Hooks configures user hook commands (docs/hooks.md).
	Hooks *HooksSettings `json:"hooks,omitempty"`
	// Skills configures skill loading and self-authoring (docs/skills.md).
	Skills *SkillsSettings `json:"skills,omitempty"`
	// Sandbox configures the OS sandbox for commands (docs/sandbox.md).
	Sandbox *SandboxSettings `json:"sandbox,omitempty"`
	// MCP configures Model Context Protocol servers (docs/mcp.md). Global
	// only: a server is a command to run or a URL to send data to, so the
	// project layer is dropped.
	MCP *MCPSettings `json:"mcp,omitempty"`
	// Telemetry configures OpenTelemetry export (docs/telemetry.md). Global
	// only: a repository choosing where session facts go would be an
	// exfiltration path, so the project layer is dropped.
	Telemetry *TelemetrySettings `json:"telemetry,omitempty"`
	// Notifications configures desktop notifications
	// (docs/notifications.md). A per-user preference: the project layer
	// cannot set it.
	Notifications *NotificationSettings `json:"notifications,omitempty"`
	// Teleport configures how this host takes part in a teleport's code
	// transfer (docs/teleport.md). A per-user preference: the project layer cannot
	// let a host push a branch to a forge.
	Teleport *TeleportSettings `json:"teleport,omitempty"`
	// Voice configures speech input to the composer (docs/voice.md). A
	// per-user preference: the project layer cannot turn the microphone on,
	// pick the capture device or make dictation send itself.
	Voice *VoiceSettings `json:"voice,omitempty"`
	// TTS configures reading replies aloud (docs/tts.md). A per-user
	// preference: the text is sent to Microsoft's read-aloud service, so the
	// project layer cannot turn it on or choose the voice or the cache.
	TTS *TTSSettings `json:"tts,omitempty"`
	// Knowledge sizes the retrieval store behind agent profile documents and
	// project knowledge (docs/knowledge.md). A per-user host-performance
	// preference: the project layer is dropped, so a repository cannot make a
	// host index or return more.
	Knowledge *KnowledgeSettings `json:"knowledge,omitempty"`
	// Sync mirrors session transcripts to the Vulnetix website while Belai is
	// logged in with the Vulnetix CLI (docs/session-sync.md). The project
	// layer may turn it off, never on.
	Sync *SyncSettings `json:"sync,omitempty"`
	// Git configures what Belai does to the repository around a session
	// (docs/git-sync.md). The project layer may turn it off, never on.
	Git *GitSettings `json:"git,omitempty"`
	// Sweep enables the background filesystem sweep for .vulnetix projects.
	VulnetixSweepEnabled *bool `json:"vulnetix_sweep_enabled,omitempty"`
	// SweepRoots restricts the sweep to a list of paths. Empty means $HOME and
	// the current workdir's parent.
	VulnetixSweepRoots []string `json:"vulnetix_sweep_roots,omitempty"`
	// Vulnetix holds per-project /vulnetix configuration. It is typed and
	// allowlisted so arbitrary argv can never be persisted here.
	Vulnetix *VulnetixSettings `json:"vulnetix,omitempty"`
	// Firewall configures the AI Firewall adapters (docs/firewall.md).
	// Instances and the active choice are read from the user's own layers
	// only; the project layer may turn the firewall off, never on.
	Firewall *FirewallSettings `json:"firewall,omitempty"`
	// WorkspaceDirs is a project-layer allowlist of additional directories that
	// may be added to sessions started in this project. If non-empty, only
	// directories in this list (and persisted to the project registry) are
	// attached; others are filtered out of the registry.
	WorkspaceDirs []string `json:"workspace_dirs,omitempty"`
	// UpdateCheck, when non-nil and false, disables the startup check for a
	// newer Belai release. Default true. BELAI_NO_UPDATE_CHECK=1 overrides
	// it for a single run.
	UpdateCheck *bool `json:"update_check,omitempty"`
	// AutoCommitPerTask, when non-nil and true, commits each completed goal's
	// changed files as one conventional commit. Default false: committing is a
	// repository mutation and must be an explicit user opt-in. Global only: a
	// repo-visible project settings file must never be able to make the harness
	// commit, so the project layer is dropped in Resolve.
	AutoCommitPerTask *bool `json:"auto_commit_per_task,omitempty"`
	// Tests configures the post-end test pass (docs/architecture.md): which
	// moments trigger it, the command, scoping, and the fail branch. All keys
	// live in the user layers only; the project layer may turn tests.enabled
	// off or lower the budgets, never on or change the command.
	Tests *TestsSettings `json:"tests,omitempty"`
	// TokenBudgets caps the tokens each provider+model may spend per session,
	// day or month. Global only: the project layer is dropped in Resolve.
	TokenBudgets []TokenBudget `json:"token_budgets,omitempty"`
	// GitRepos are the repositories `belai repo sync` keeps cloned under the repos
	// directory (docs/library-items.md). Global only: the project layer is dropped in
	// Resolve, so a repository cannot make a host clone another.
	GitRepos []GitRepo `json:"repos,omitempty"`
	// Intel governs session intelligence's use of provider response headers
	// (docs/token-budgets.md). Global only: the project layer is dropped in
	// Resolve, so a repository cannot change what a user learns about their own
	// account limits.
	Intel *IntelSettings `json:"intel,omitempty"`
	// Kanban turns the global kanban board on or off: the Kanban* model tools,
	// the wrap-up after a work turn, the composer pane, /kanban and board sync
	// (docs/kanban.md). Default on. The project layer may turn it off, never on.
	Kanban *bool `json:"kanban,omitempty"`
	// BashRewrite is the user's rule table that rewrites a model's Bash command
	// word before permission matching (docs/bash-rewrite.md). Rules come from
	// the user's own layers only; the project layer may turn the table off,
	// never on and never add a rule.
	BashRewrite *BashRewriteSettings `json:"bash_rewrite,omitempty"`
	// Screenshot governs the Screenshot tool (docs/screenshots.md). The
	// project layer may turn it, or whole-screen capture, off; never on.
	Screenshot *ScreenshotSettings `json:"screenshot,omitempty"`
	// Code governs code mode's script interpreter (docs/code-mode.md). A
	// project layer may turn it off and lower a limit, never the reverse.
	Code *CodeSettings `json:"code,omitempty"`
	// Agents governs the fleet: worker agents that claim kanban items and
	// run unattended (docs/fleet.md). The project layer may only tighten it.
	Agents *AgentsSettings `json:"agents,omitempty"`
	// DeferTools advertises the core tools in full and loads the rest on
	// demand through ToolSearch, which keeps every request small. Default on;
	// false sends every definition on every request. Deferral changes what is
	// advertised, never what may run.
	DeferTools *bool `json:"defer_tools,omitempty"`
	// Offload keeps oversized tool results out of the conversation: a preview
	// stays inline and ReadResult reads the rest (docs/context-offload.md).
	// Default on. It changes what rides on a request, never what is admitted,
	// so any layer may set it.
	Offload *OffloadSettings `json:"offload,omitempty"`
	// WebFetch keeps the pages a session fetched: a short-lived in-memory
	// cache and a search index over them (docs/web-fetch.md). Both default on.
	// They change what a session re-fetches, never what is admitted, so any
	// layer may set them.
	WebFetch *WebFetchSettings `json:"web_fetch,omitempty"`
	// Jev configures the relevance jobs that use a decision backend (bash swap,
	// compaction pruning, tool selection and search, option order, LSP triage,
	// explore locate; docs/jev-jobs.md). Every job defaults on and runs only
	// while a decision backend is configured. The project layer may turn a job
	// off, never on.
	Jev *JevSettings `json:"jev,omitempty"`
}

// SyncSettings configures session sync (docs/session-sync.md).
type SyncSettings struct {
	// Enabled mirrors each session's JSONL to the Vulnetix website. Default
	// true whenever a Vulnetix CLI credential resolves; false turns it off.
	Enabled *bool `json:"enabled,omitempty"`
	// RemotePrompts lets the website send prompts into a live session. Default
	// true; false shares the session view-only.
	RemotePrompts *bool `json:"remote_prompts,omitempty"`
	// RemoteAnswers lets the website answer the question a live session is
	// blocked on: a permission ask (allow once, allow always, deny), a
	// clarify questionnaire, the mode choice or a plan review. Default true;
	// false keeps every ask on the host.
	RemoteAnswers *bool `json:"remote_answers,omitempty"`
	// Profiles lets `belai rc` keep the website's agent and crew library current
	// by itself: it pushes the profile markdown and crew JSON that changed on this
	// host, never file contents (those leave only for a backup request) and never
	// over a version the website saved since this host last synced. Default true;
	// false leaves backups to requests from the website.
	Profiles *bool `json:"profiles,omitempty"`
	// Skills lets `belai rc` keep the website's skill library current by itself
	// and lets the website back up or install a skill here
	// (docs/library-items.md). Default true; false leaves skills out of both.
	Skills *bool `json:"skills,omitempty"`
	// Prompts is the same switch for the global prompt library.
	Prompts *bool `json:"prompts,omitempty"`
	// Processes is the same switch for the global process library.
	Processes *bool `json:"processes,omitempty"`
	// Repos is the same switch for the repository list (`repos`).
	Repos *bool `json:"repos,omitempty"`
	// Budgets is the same switch for the token-budget configuration.
	Budgets *bool `json:"budgets,omitempty"`
	// Rewrites is the same switch for the Bash rewrite table.
	Rewrites *bool `json:"rewrites,omitempty"`
	// Providers is the same switch for the provider set (`providers` and
	// `firewall`) and for the website's provider key requests.
	Providers *bool `json:"providers,omitempty"`
}

// GitSettings configures the repository hygiene a session does before a turn.
type GitSettings struct {
	// Sync fetches origin and rebases the session's branch onto origin's
	// default branch before the first turn and before the first turn after a
	// commit, when the tree is clean. Default true; false leaves the repository
	// alone. A session can switch it off for itself (/gitsync off, or the
	// website's session page) without changing this.
	Sync *bool `json:"sync,omitempty"`
}

// GitSyncEnabled reports whether sessions sync their branch with origin's
// default branch before a turn. Default on.
func (s Settings) GitSyncEnabled() bool {
	return s.Git == nil || s.Git.Sync == nil || *s.Git.Sync
}

// mergeGitOffOnly applies a project layer's git keys over base, honouring only
// false: a repository may opt out of the sync, and cannot opt a user back in.
func mergeGitOffOnly(base, proj *GitSettings) *GitSettings {
	out := &GitSettings{}
	if base != nil {
		*out = *base
	}
	if proj.Sync != nil && !*proj.Sync {
		f := false
		out.Sync = &f
	}
	return out
}

// mergeSyncOffOnly applies a project layer's sync keys over base, honouring
// only false. It never mutates base.
func mergeSyncOffOnly(base, proj *SyncSettings) *SyncSettings {
	out := &SyncSettings{}
	if base != nil {
		*out = *base
	}
	f := false
	if proj.Enabled != nil && !*proj.Enabled {
		out.Enabled = &f
	}
	if proj.RemotePrompts != nil && !*proj.RemotePrompts {
		out.RemotePrompts = &f
	}
	if proj.RemoteAnswers != nil && !*proj.RemoteAnswers {
		out.RemoteAnswers = &f
	}
	if proj.Profiles != nil && !*proj.Profiles {
		out.Profiles = &f
	}
	if proj.Skills != nil && !*proj.Skills {
		out.Skills = &f
	}
	if proj.Prompts != nil && !*proj.Prompts {
		out.Prompts = &f
	}
	if proj.Processes != nil && !*proj.Processes {
		out.Processes = &f
	}
	if proj.Repos != nil && !*proj.Repos {
		out.Repos = &f
	}
	if proj.Budgets != nil && !*proj.Budgets {
		out.Budgets = &f
	}
	if proj.Rewrites != nil && !*proj.Rewrites {
		out.Rewrites = &f
	}
	if proj.Providers != nil && !*proj.Providers {
		out.Providers = &f
	}
	return out
}

// SyncItemEnabled reports whether the library of one kind of item (skill,
// prompt, ...) is kept current by `belai rc` and open to the website's backup
// and install requests: sync.<kinds>, default on, and never on while sync
// itself is off. An unknown kind is off.
func (s Settings) SyncItemEnabled(kind string) bool {
	if !s.SyncEnabled() {
		return false
	}
	var v *bool
	switch kind {
	case "skill":
		if s.Sync != nil {
			v = s.Sync.Skills
		}
	case "prompt":
		if s.Sync != nil {
			v = s.Sync.Prompts
		}
	case "process":
		if s.Sync != nil {
			v = s.Sync.Processes
		}
	case "repo":
		if s.Sync != nil {
			v = s.Sync.Repos
		}
	case "budget":
		if s.Sync != nil {
			v = s.Sync.Budgets
		}
	case "rewrite":
		if s.Sync != nil {
			v = s.Sync.Rewrites
		}
	case "provider":
		if s.Sync != nil {
			v = s.Sync.Providers
		}
	default:
		return false
	}
	return v == nil || *v
}

// SyncEnabled reports whether session sync is on. Default on; it still needs
// a Vulnetix CLI credential to do anything.
func (s Settings) SyncEnabled() bool {
	return s.Sync == nil || s.Sync.Enabled == nil || *s.Sync.Enabled
}

// SyncRemotePromptsEnabled reports whether a synced session accepts prompts
// from the website. Default on, and never on while sync itself is off.
func (s Settings) SyncRemotePromptsEnabled() bool {
	return s.SyncEnabled() && (s.Sync == nil || s.Sync.RemotePrompts == nil || *s.Sync.RemotePrompts)
}

// SyncProfilesEnabled reports whether `belai rc` pushes changed profiles and
// crews to the website's library by itself. Default on, and never on while sync
// itself is off.
func (s Settings) SyncProfilesEnabled() bool {
	return s.SyncEnabled() && (s.Sync == nil || s.Sync.Profiles == nil || *s.Sync.Profiles)
}

// SyncRemoteAnswersEnabled reports whether a synced session accepts answers
// to its open asks from the website. Default on, and never on while sync
// itself is off.
func (s Settings) SyncRemoteAnswersEnabled() bool {
	return s.SyncEnabled() && (s.Sync == nil || s.Sync.RemoteAnswers == nil || *s.Sync.RemoteAnswers)
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
	// FirewallEnabled is the legacy switch for the AI Firewall, read as
	// firewall.enabled when that key is unset. Writers use firewall.enabled.
	FirewallEnabled *bool `json:"firewall_enabled,omitempty"`
	// DepWatch runs the dependency-manifest hook: a manifest the session
	// changes is checked with the Vulnetix CLI when the change added or
	// updated dependencies. Default true; false turns the hook off.
	DepWatch *bool `json:"dep_watch,omitempty"`
}

// HooksSettings configures user hook commands.
type HooksSettings struct {
	// Enabled runs hooks. Default true; a repo-visible project layer may
	// turn it off, never on.
	Enabled *bool `json:"enabled,omitempty"`
}

// TeleportSettings configures a host's part in moving a session's code.
type TeleportSettings struct {
	// Push says whether this host, as the origin of a teleport, may push the
	// session's uncommitted and unpushed changes to the forge as one
	// belai/teleport/<id> branch so the target can fetch them: "ask" (the
	// default) pushes only when the target's user agreed with -teleport-push,
	// "allow" pushes whenever a teleport asks, and "never" never does, so the
	// changes always go to the target as a patch for a replay. Any other value is
	// "never".
	Push string `json:"push,omitempty"`
}

// Teleport push policies.
const (
	TeleportPushAsk   = "ask"
	TeleportPushAllow = "allow"
	TeleportPushNever = "never"
)

// TeleportPushPolicy returns the host's push policy: ask when unset, and never
// for a value that is not one of the three.
func (s Settings) TeleportPushPolicy() string {
	if s.Teleport == nil || s.Teleport.Push == "" {
		return TeleportPushAsk
	}
	switch s.Teleport.Push {
	case TeleportPushAsk, TeleportPushAllow, TeleportPushNever:
		return s.Teleport.Push
	}
	return TeleportPushNever
}

func (s *TeleportSettings) merge(from *TeleportSettings) {
	if from.Push != "" {
		s.Push = from.Push
	}
}

// NotificationSettings configures desktop notifications.
type NotificationSettings struct {
	// Enabled turns notifications on. Default false.
	Enabled *bool `json:"enabled,omitempty"`
	// Backend is auto, osc, bell, notify-send or osascript. Empty is auto.
	Backend string `json:"backend,omitempty"`
	// Events names the moments to notify. nil means the default set.
	Events []string `json:"events,omitempty"`
	// MinTurnSeconds is how long a turn must run before turn_done notifies.
	// Zero means DefaultNotifyMinTurnSeconds.
	MinTurnSeconds int `json:"min_turn_seconds,omitempty"`
}

// DefaultNotifyMinTurnSeconds is the turn_done threshold when unset.
const DefaultNotifyMinTurnSeconds = 30

// NotificationsEnabled reports whether notifications are on. Default false.
func (s *NotificationSettings) NotificationsEnabled() bool {
	return s != nil && s.Enabled != nil && *s.Enabled
}

// MinTurnSecondsOr returns the turn_done threshold.
func (s *NotificationSettings) MinTurnSecondsOr() int {
	if s == nil || s.MinTurnSeconds <= 0 {
		return DefaultNotifyMinTurnSeconds
	}
	return s.MinTurnSeconds
}

func (s *NotificationSettings) merge(from *NotificationSettings) {
	if from.Enabled != nil {
		s.Enabled = from.Enabled
	}
	if from.Backend != "" {
		s.Backend = from.Backend
	}
	if from.Events != nil {
		// An explicit empty list means "none", so it must stay non-nil.
		s.Events = make([]string, len(from.Events))
		copy(s.Events, from.Events)
	}
	if from.MinTurnSeconds != 0 {
		s.MinTurnSeconds = from.MinTurnSeconds
	}
}

// TelemetrySettings configures OTLP export.
type TelemetrySettings struct {
	// OTLPEndpoint is the collector base URL, e.g. http://localhost:4318.
	// Empty means export is off (unless OTEL_EXPORTER_OTLP_ENDPOINT is set).
	OTLPEndpoint string `json:"otlp_endpoint,omitempty"`
	// Headers are sent with every export. "env:NAME" reads the environment.
	Headers map[string]string `json:"headers,omitempty"`
	// Traces and Metrics turn either signal off. Default on.
	Traces  *bool `json:"traces,omitempty"`
	Metrics *bool `json:"metrics,omitempty"`
}

// MCPSettings lists MCP servers by name.
type MCPSettings struct {
	Servers map[string]MCPServer `json:"servers,omitempty"`
}

const (
	// VulnetixMCPName is the mcp.servers key /vulnetix mcp manages.
	VulnetixMCPName = "vulnetix"
	// VulnetixMCPURL is the hosted Vulnetix MCP server (streamable HTTP).
	VulnetixMCPURL = "https://mcp.vulnetix.com/mcp"
	// VulnetixCLIRef is a header value that stands for the Vulnetix CLI's
	// Authorization credential. It is resolved when the server is dialled,
	// only for https://*.vulnetix.com, and never written out resolved.
	VulnetixCLIRef = "vulnetix:cli"
)

// VulnetixMCPServer is the entry /vulnetix mcp writes: the hosted server,
// authenticated with the CLI's own credential by reference.
func VulnetixMCPServer() MCPServer {
	return MCPServer{
		Transport: "http",
		URL:       VulnetixMCPURL,
		Headers:   map[string]string{"Authorization": VulnetixCLIRef},
	}
}

// MCPServer is one MCP server.
type MCPServer struct {
	// Transport is stdio (default) or http.
	Transport string `json:"transport,omitempty"`
	// Command and Args start a stdio server.
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Env is passed to a stdio server on top of the scrubbed environment. A
	// value "env:NAME" copies NAME from Belai's environment.
	Env map[string]string `json:"env,omitempty"`
	// URL and Headers reach an http server. A header value "env:NAME" is
	// read from the environment; an Authorization value VulnetixCLIRef is
	// the Vulnetix CLI's credential, sent only to https://*.vulnetix.com.
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Tools, when set, offers only these server tools.
	Tools []string `json:"tools,omitempty"`
	// Disabled keeps the server configured but not started.
	Disabled bool `json:"disabled,omitempty"`
	// Sandbox runs a stdio server under the OS sandbox.
	Sandbox bool `json:"sandbox,omitempty"`
	// TimeoutMS bounds one tool call (default 60000, at most 600000).
	TimeoutMS int `json:"timeout_ms,omitempty"`
}

// SandboxSettings configures the OS sandbox around Bash, inline !cmd and
// supervised processes.
type SandboxSettings struct {
	// Mode is off, auto (default: sandbox when a backend exists) or required.
	Mode string `json:"mode,omitempty"`
	// Network is allow (default) or deny.
	Network string `json:"network,omitempty"`
	// Caches keeps the usual tool caches under $HOME writable. Default true;
	// false is the strict policy.
	Caches *bool `json:"caches,omitempty"`
	// ExtraWritable adds absolute paths the sandbox may write. Global only.
	ExtraWritable []string `json:"extra_writable,omitempty"`
}

var sandboxModeRank = map[string]int{"off": 0, "auto": 1, "required": 2}

// ModeOr returns the mode, defaulting to auto. An unknown value is auto.
func (s *SandboxSettings) ModeOr() string {
	if s == nil {
		return "auto"
	}
	if _, ok := sandboxModeRank[s.Mode]; ok {
		return s.Mode
	}
	return "auto"
}

// NetworkOr returns the network setting, defaulting to allow. Anything but
// allow or deny is deny.
func (s *SandboxSettings) NetworkOr() string {
	if s == nil || s.Network == "" || s.Network == "allow" {
		return "allow"
	}
	return "deny"
}

// CachesOr reports whether tool caches stay writable. Default true.
func (s *SandboxSettings) CachesOr() bool {
	return s == nil || s.Caches == nil || *s.Caches
}

// mergeSandbox folds from into s. A repo-visible project layer may only
// tighten: raise the mode, deny the network, drop the caches; its
// extra_writable is ignored.
func mergeSandbox(s *SandboxSettings, from *SandboxSettings, project bool) {
	if from.Mode != "" {
		if _, ok := sandboxModeRank[from.Mode]; ok && (!project || sandboxModeRank[from.Mode] > sandboxModeRank[s.ModeOr()]) {
			s.Mode = from.Mode
		}
	}
	if from.Network != "" && (!project || from.Network == "deny") {
		s.Network = from.Network
	}
	if from.Caches != nil && (!project || !*from.Caches) {
		s.Caches = from.Caches
	}
	if !project && from.ExtraWritable != nil {
		s.ExtraWritable = append([]string(nil), from.ExtraWritable...)
	}
}

// SkillsSettings configures skills.
type SkillsSettings struct {
	// SelfAuthoring offers the SkillDraft tool. Default true; a repo-visible
	// project layer may turn it off, never on.
	SelfAuthoring *bool `json:"self_authoring,omitempty"`
}

// SelfAuthoringEnabled reports whether SkillDraft is offered. Default true.
func (s *SkillsSettings) SelfAuthoringEnabled() bool {
	return s == nil || s.SelfAuthoring == nil || *s.SelfAuthoring
}

// HooksEnabled reports whether hooks run. Default true.
func (s *HooksSettings) HooksEnabled() bool {
	return s == nil || s.Enabled == nil || *s.Enabled
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
	return DefaultVulnetixGateway
}

// AutoFixEnabled reports whether /vulnetix review may run `vulnetix fix --yes`
// unattended. Default false: the review attaches a --dry-run plan instead.
func (s *VulnetixSettings) AutoFixEnabled() bool {
	return s != nil && s.AutoFix != nil && *s.AutoFix
}

// VulnetixSubcommandNames are the names `vulnetix.subcommands` may hold: the
// nine review scanners and the post-scan fix activity. The review keeps its own
// table (commands.AllowedSubcommands); a test pins the two together, because
// config cannot import commands.
var VulnetixSubcommandNames = []string{"sca", "containers", "sast", "secrets", "iac", "malscan", "sbom", "aibom", "cbom", "fix"}

// MaxVulnetixTimeout bounds `vulnetix.timeout`, so a typo cannot hold a review
// open for days.
const MaxVulnetixTimeout = 24 * time.Hour

// ReviewTimeout is how long each review scan may run, from vulnetix.timeout.
// Zero means no limit, the default: a scan runs until it exits or is killed.
// ValidateVulnetix has already refused a value that does not parse.
func (s *VulnetixSettings) ReviewTimeout() time.Duration {
	if s == nil || s.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil || d <= 0 {
		return 0
	}
	return min(d, MaxVulnetixTimeout)
}

// ReviewSubcommands is the scanner subset vulnetix.subcommands names, or nil
// for every scanner.
func (s *VulnetixSettings) ReviewSubcommands() []string {
	if s == nil {
		return nil
	}
	return s.Subcommands
}

// ValidateVulnetix rejects a vulnetix block (and vulnetix_sweep_roots) holding a subcommand outside the
// allowlist or a timeout that is not a positive duration up to
// MaxVulnetixTimeout, naming the key. It runs when settings are loaded, so a
// bad value fails at startup rather than when a review starts.
func ValidateVulnetix(s Settings) error {
	for _, root := range s.VulnetixSweepRoots {
		if !filepath.IsAbs(root) {
			return fmt.Errorf("vulnetix_sweep_roots: %q must be an absolute path", root)
		}
	}
	v := s.Vulnetix
	if v == nil {
		return nil
	}
	for _, name := range v.Subcommands {
		if !slices.Contains(VulnetixSubcommandNames, name) {
			return fmt.Errorf("vulnetix.subcommands: %q is not one of %s", name, strings.Join(VulnetixSubcommandNames, ", "))
		}
	}
	if v.Timeout != "" {
		d, err := time.ParseDuration(v.Timeout)
		switch {
		case err != nil:
			return fmt.Errorf("vulnetix.timeout %q is not a duration such as 10m or 1h30m", v.Timeout)
		case d <= 0 || d > MaxVulnetixTimeout:
			return fmt.Errorf("vulnetix.timeout %q must be longer than zero and at most %s", v.Timeout, MaxVulnetixTimeout)
		}
	}
	return nil
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

// TestsSettings configures the post-end test pass. Defaults mirror
// auto_commit_per_task: the pass is off unless the user opts in, and the fail
// branch may diagnose and fix only when the user asks for it.
type TestsSettings struct {
	// PostEnd is the trigger level: "off" (default), "goal", "goal_plan" or
	// "session". "goal" runs after a completed goal, "goal_plan" also after a
	// completed plan, and "session" runs at session end even when the last
	// goal did not complete.
	PostEnd string `json:"post_end,omitempty"`
	// Command overrides the detected test command argv. User layers only.
	Command []string `json:"command,omitempty"`
	// Scope is "affected" (default) or "full". "affected" scopes the detected
	// command to the suites owning changed paths, falling back to full.
	Scope string `json:"scope,omitempty"`
	// OnFail is "off", "diagnose" or "fix" (default). "diagnose" runs a
	// read-only investigation; "fix" runs the agentic diagnose-and-fix loop.
	OnFail string `json:"on_fail,omitempty"`
	// MaxFixPasses bounds the diagnose-and-fix loop. Zero means the default.
	MaxFixPasses int `json:"max_fix_passes,omitempty"`
	// TimeoutSeconds bounds one suite run. Zero means the default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Report enables the fast-model report on pass. Default true; false uses
	// the harness-composed line only.
	Report *bool `json:"report,omitempty"`
}

// DefaultTestsMaxFixPasses is the diagnose-and-fix pass ceiling when unset.
const DefaultTestsMaxFixPasses = 3

// DefaultTestsTimeoutSeconds is one suite run's timeout when unset.
const DefaultTestsTimeoutSeconds = 300

// TestsPostEnd returns the trigger level, defaulting to "off".
func (s Settings) TestsPostEnd() string {
	if s.Tests == nil || s.Tests.PostEnd == "" {
		return "off"
	}
	return s.Tests.PostEnd
}

// TestsEnabled reports whether any post-end moment triggers the pass.
func (s Settings) TestsEnabled() bool {
	switch s.TestsPostEnd() {
	case "goal", "goal_plan", "session":
		return true
	}
	return false
}

// TestsCommand returns the user's command override, or nil.
func (s Settings) TestsCommand() []string {
	if s.Tests == nil || len(s.Tests.Command) == 0 {
		return nil
	}
	return append([]string(nil), s.Tests.Command...)
}

// TestsScope returns "affected" (default) or "full".
func (s Settings) TestsScope() string {
	if s.Tests != nil && s.Tests.Scope == "full" {
		return "full"
	}
	return "affected"
}

// TestsOnFail returns "off", "diagnose" or "fix" (default when unset). An
// unrecognised value fails closed to "off": a typo must not widen the fail
// branch to editing files.
func (s Settings) TestsOnFail() string {
	if s.Tests == nil || s.Tests.OnFail == "" {
		return "fix"
	}
	switch s.Tests.OnFail {
	case "off", "diagnose", "fix":
		return s.Tests.OnFail
	}
	return "off"
}

// TestsMaxFixPasses returns the diagnose-and-fix pass ceiling.
func (s Settings) TestsMaxFixPasses() int {
	if s.Tests == nil || s.Tests.MaxFixPasses <= 0 {
		return DefaultTestsMaxFixPasses
	}
	return s.Tests.MaxFixPasses
}

// TestsTimeoutSeconds returns one suite run's timeout.
func (s Settings) TestsTimeoutSeconds() int {
	if s.Tests == nil || s.Tests.TimeoutSeconds <= 0 {
		return DefaultTestsTimeoutSeconds
	}
	return s.Tests.TimeoutSeconds
}

// TestsReportEnabled reports whether the fast-model pass report is on.
// Default true: the pass itself is opt-in, and once it runs a passing result
// gets a fast-model report. Set false for the harness-composed line only.
func (s Settings) TestsReportEnabled() bool {
	return s.Tests == nil || s.Tests.Report == nil || *s.Tests.Report
}

// mergeTests folds from into dst for a user layer. All fields merge normally:
// the user controls every key.
func mergeTests(dst, from *TestsSettings) *TestsSettings {
	if from == nil {
		return dst
	}
	out := &TestsSettings{}
	if dst != nil {
		*out = *dst
	}
	if from.PostEnd != "" {
		out.PostEnd = from.PostEnd
	}
	if from.Command != nil {
		out.Command = append([]string(nil), from.Command...)
	}
	if from.Scope != "" {
		out.Scope = from.Scope
	}
	if from.OnFail != "" {
		out.OnFail = from.OnFail
	}
	if from.MaxFixPasses != 0 {
		out.MaxFixPasses = from.MaxFixPasses
	}
	if from.TimeoutSeconds != 0 {
		out.TimeoutSeconds = from.TimeoutSeconds
	}
	if from.Report != nil {
		out.Report = from.Report
	}
	return out
}

// projectTestsWidens reports whether a project layer's tests block sets
// anything tightenTests will drop: a post_end other than off, a command, a
// scope, an on_fail or a report.
func projectTestsWidens(p *TestsSettings) bool {
	if p == nil {
		return false
	}
	return (p.PostEnd != "" && p.PostEnd != "off") || len(p.Command) > 0 || p.Scope != "" || p.OnFail != "" || p.Report != nil
}

// tightenTests applies a project layer's tests block. A repo-visible settings
// file may turn the pass off (post_end "off") and lower max_fix_passes or
// timeout_seconds, never the reverse, and it may never set the command, scope,
// on_fail or report: those would let a cloned repo run arbitrary commands or
// widen the fail branch on the user's machine.
func tightenTests(dst, proj *TestsSettings) *TestsSettings {
	out := &TestsSettings{}
	if dst != nil {
		*out = *dst
	}
	if proj == nil {
		return out
	}
	if proj.PostEnd == "off" {
		out.PostEnd = "off"
	}
	if proj.MaxFixPasses > 0 {
		cur := out.MaxFixPasses
		if cur <= 0 {
			cur = DefaultTestsMaxFixPasses
		}
		if proj.MaxFixPasses < cur {
			out.MaxFixPasses = proj.MaxFixPasses
		}
	}
	if proj.TimeoutSeconds > 0 {
		cur := out.TimeoutSeconds
		if cur <= 0 {
			cur = DefaultTestsTimeoutSeconds
		}
		if proj.TimeoutSeconds < cur {
			out.TimeoutSeconds = proj.TimeoutSeconds
		}
	}
	return out
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
	// LLM sentinel), "systemone" (a /v1/systemone decision server, never a chat model; "jev" is read as it), "openrouter-decisions" (OpenRouter's Decisions API or the local decision model) or "models" (local BERT gates plus an optional narrowed
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
	// Decision tunes the decision backend (a local decision model or a
	// self-hosted Jev) when classifier.provider names one.
	Decision ClassifierDecisionSettings `json:"decision,omitempty"`
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

// ClassifierDecisionSettings tunes a decision backend. Both knobs change
// how long the harness waits and how much it sends before handing a check to
// the agent-model fallback; neither can skip a classification.
type ClassifierDecisionSettings struct {
	// TimeoutMS bounds one decision call. Zero uses the backend default
	// (20s local, 5s self-hosted, 3s OpenRouter).
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// MaxStateBytes is the largest content sent to a local decision model;
	// larger content goes to the fallback. Zero uses 16 KiB.
	MaxStateBytes int `json:"max_state_bytes,omitempty"`
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
	if from.Decision.TimeoutMS != 0 {
		c.Decision.TimeoutMS = from.Decision.TimeoutMS
	}
	if from.Decision.MaxStateBytes != 0 {
		c.Decision.MaxStateBytes = from.Decision.MaxStateBytes
	}
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
		c.Phase1.IsZero() && c.Phase2.IsZero() &&
		c.Decision.TimeoutMS == 0 && c.Decision.MaxStateBytes == 0
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
	// DecisionPath is the decision endpoint of a kind "systemone" profile, relative
	// to BaseURL; empty means /v1/systemone. The /model test stores the path
	// it found answering when the default did not.
	DecisionPath string `json:"decision_path,omitempty"`
}

// ProviderModel is one model in a custom provider's catalogue.
type ProviderModel struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	MaxTokens     int    `json:"max_tokens,omitempty"`
	// Images declares whether the model accepts image input. nil means "decide
	// from the model id" (models.Vision); true sends attached and captured
	// images to it, false keeps them off it and tells the model so.
	Images *bool `json:"images,omitempty"`
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
	// MacKeyHints adds the Mac media-key form after an F-key in the hints and
	// help (f9 shown as f9 (Fn+⏭)). nil follows the platform: on for macOS, off
	// elsewhere. It is display-only. See MacKeyHintsFor.
	MacKeyHints *bool `json:"mac_key_hints,omitempty"`
	// ShowInternalWork selects how much of the role manager's internal
	// decision-making is shown in the belai panel: "hidden", "decisions",
	// "security", or "all". It is display-only — every level runs exactly the
	// same gates.
	ShowInternalWork *string `json:"show_internal_work,omitempty"`
	// Layout selects the thread's layout: "clean" (the default) or
	// "chronological", which places every late row by the time it happened and
	// shows that time on each row. It is display-only.
	Layout *string `json:"layout,omitempty"`
	// BudgetCycleSeconds is how long the footer shows one token budget before
	// cycling to the next (default 10, minimum 2). See BudgetCycle.
	BudgetCycleSeconds *int `json:"budget_cycle_seconds,omitempty"`
	// BudgetWarn prints a system line on each model call for the selected
	// model while any of its budgets is amber or red. Default off.
	BudgetWarn *bool `json:"budget_warn,omitempty"`
	// Intel shows the session intelligence slot in the footer's budget cycle
	// and offers the intel pane on f12. Default on. See IntelEnabled.
	Intel *bool `json:"intel,omitempty"`
	// IdlePane picks the one pane shown above an empty composer while the chat
	// is idle: "none" (the default) or one of IdlePaneNames. It is display
	// only. See Settings.IdlePane.
	IdlePane *string `json:"idle_pane,omitempty"`
	// ClipboardImages lets ctrl+v and /paste-image attach an image from the
	// system clipboard. Default on. The project layer may turn it off, never
	// on (docs/image-attachments.md).
	ClipboardImages *bool `json:"clipboard_images,omitempty"`
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
	if from.MacKeyHints != nil {
		u.MacKeyHints = from.MacKeyHints
	}
	if from.Mouse != nil {
		u.Mouse = from.Mouse
	}
	if from.ShowInternalWork != nil {
		u.ShowInternalWork = from.ShowInternalWork
	}
	if from.Layout != nil {
		u.Layout = from.Layout
	}
	if from.BudgetCycleSeconds != nil {
		u.BudgetCycleSeconds = from.BudgetCycleSeconds
	}
	if from.BudgetWarn != nil {
		u.BudgetWarn = from.BudgetWarn
	}
	if from.Intel != nil {
		u.Intel = from.Intel
	}
	if from.IdlePane != nil {
		u.IdlePane = from.IdlePane
	}
	if from.ClipboardImages != nil {
		u.ClipboardImages = from.ClipboardImages
	}
}

// mergeProject folds a project layer over u. It is merge, except that a project
// file cannot turn clipboard images on: a repository must not be able to make
// the composer read the clipboard for a user who switched that off.
func (u *UISettings) mergeProject(from *UISettings) {
	if from == nil {
		return
	}
	cp := *from
	if cp.ClipboardImages != nil && *cp.ClipboardImages {
		cp.ClipboardImages = nil
	}
	u.merge(&cp)
}

// ClipboardImagesEnabled reports whether the composer may attach an image from
// the clipboard. Default on.
func (s Settings) ClipboardImagesEnabled() bool {
	return s.UI == nil || s.UI.ClipboardImages == nil || *s.UI.ClipboardImages
}

// ResilienceSettings controls the provider retry and agent-loop budgets.
// A zero value means "use the built-in default".
type ResilienceSettings struct {
	// MaxAttempts is the inclusive pre-first-byte retry budget per model
	// call. It defaults to 3.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// MaxIterations is the per-prompt tool-loop budget. It defaults to 40
	// (DefaultMaxIterations).
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
	// MaxBackgroundProcesses caps how many processes the model may have
	// running at once through Bash run_in_background. Zero means the default
	// (8). A project layer can only lower it.
	MaxBackgroundProcesses int `json:"max_background_processes,omitempty"`
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
	if from.ModeDetection != "" {
		r.ModeDetection = from.ModeDetection
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
	return r == nil || (r.Kind == "" && len(r.UseCases) == 0 && r.Fast == nil && r.ModeDetection == "")
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

// MaxBackgroundOr returns MaxBackgroundProcesses or the provided default.
func (r *ResilienceSettings) MaxBackgroundOr(def int) int {
	if r == nil || r.MaxBackgroundProcesses <= 0 {
		return def
	}
	return r.MaxBackgroundProcesses
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
	tighten(&r.MaxBackgroundProcesses, &other.MaxBackgroundProcesses)
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

// Layout values for ui.layout.
const (
	// LayoutClean is the default: rows as the thread builds them.
	LayoutClean = "clean"
	// LayoutChronological places every late row by the time it happened and
	// shows that time on each row.
	LayoutChronological = "chronological"
)

// LayoutNames lists the layouts in the order the settings screen cycles them.
var LayoutNames = []string{LayoutClean, LayoutChronological}

// Layout returns the thread layout as a normalised name. An unrecognised value
// reads as clean. It is display-only: every layout runs exactly the same gates
// and writes the same record.
func (s Settings) Layout() string {
	if s.UI == nil || s.UI.Layout == nil {
		return LayoutClean
	}
	if strings.ToLower(strings.TrimSpace(*s.UI.Layout)) == LayoutChronological {
		return LayoutChronological
	}
	return LayoutClean
}

// IdlePane values for ui.idle_pane: the pane shown above an empty composer.
// The names after IdlePaneNone are the runs panel's tabs (kanban is drawn by
// its own composer pane).
const (
	IdlePaneNone      = "none"
	IdlePaneKanban    = "kanban"
	IdlePaneActivity  = "activity"
	IdlePaneHelpers   = "helpers"
	IdlePaneProcesses = "processes"
	IdlePaneCrew      = "crew"
	IdlePaneGit       = "git"
	IdlePaneCI        = "ci"
	IdlePaneIntel     = "intel"
)

// IdlePaneNames lists the idle panes in the order the settings screen cycles
// them, none first.
var IdlePaneNames = []string{
	IdlePaneNone, IdlePaneKanban, IdlePaneActivity, IdlePaneHelpers,
	IdlePaneProcesses, IdlePaneCrew, IdlePaneGit, IdlePaneCI, IdlePaneIntel,
}

// IdlePane returns the pane shown above an empty idle composer as a
// normalised name. The default is none, and an unrecognised value reads as
// none. It is display-only: no gate, rule or record depends on it.
func (s Settings) IdlePane() string {
	if s.UI == nil || s.UI.IdlePane == nil {
		return IdlePaneNone
	}
	v := strings.ToLower(strings.TrimSpace(*s.UI.IdlePane))
	for _, n := range IdlePaneNames {
		if n == v {
			return v
		}
	}
	return IdlePaneNone
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

// MacKeyHintsFor reports whether F-key hints carry the Mac media-key form on
// the given GOOS. An unset setting follows the platform (on for darwin), and
// an explicit value wins, so a Linux VM on Mac hardware can turn it on.
func (s Settings) MacKeyHintsFor(goos string) bool {
	if s.UI != nil && s.UI.MacKeyHints != nil {
		return *s.UI.MacKeyHints
	}
	return goos == "darwin"
}

// MacKeyHints is [Settings.MacKeyHintsFor] for the running platform.
func (s Settings) MacKeyHints() bool { return s.MacKeyHintsFor(runtime.GOOS) }

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
	// A project-layer settings file may turn the firewall off but never on,
	// and never names or picks one. A repo must not be able to redirect
	// prompts to a gateway by shipping a .vulnetix/belai/settings.json.
	out = normalizeFirewall(out)
	if p := normalizeFirewall(proj); p.Firewall != nil {
		fw := FirewallSettings{}
		if out.Firewall != nil {
			fw = *out.Firewall
		}
		mergeFirewall(&fw, p.Firewall, true)
		out.Firewall = &fw
	}
	// Session sync sends transcripts off the machine: a project file may turn
	// it (or its web prompts) off, never on.
	if proj.Sync != nil {
		out.Sync = mergeSyncOffOnly(out.Sync, proj.Sync)
	}
	if proj.Git != nil {
		out.Git = mergeGitOffOnly(out.Git, proj.Git)
	}
	if proj.DeferTools != nil {
		out.DeferTools = proj.DeferTools
	}
	out.Offload = mergeOffload(out.Offload, proj.Offload)
	out.WebFetch = mergeWebFetch(out.WebFetch, proj.WebFetch)
	out.Jev = mergeJev(out.Jev, proj.Jev, true)
	// The kanban board syncs off the machine: off only.
	if proj.Kanban != nil && !*proj.Kanban {
		f := false
		out.Kanban = &f
	}
	// Screenshot capture: a project may turn it off, never on.
	out.Screenshot = mergeScreenshot(out.Screenshot, proj.Screenshot, true)
	// Code mode: a project may turn it off and lower its limits, never the reverse.
	out.Code = mergeCode(out.Code, proj.Code, true)
	// The Bash rewrite table: a project may switch it off, never add or enable.
	out.BashRewrite = mergeBashRewrite(out.BashRewrite, proj.BashRewrite, true)
	// Fleet workers run unattended: a project may only tighten them.
	if proj.Agents != nil {
		out.Agents = out.Agents.tighten(proj.Agents)
	}
	// Hooks are the user's own commands: a project file may turn them off,
	// never on.
	if proj.Hooks != nil && proj.Hooks.Enabled != nil && !*proj.Hooks.Enabled {
		f := false
		out.Hooks = &HooksSettings{Enabled: &f}
	}
	if proj.Sandbox != nil {
		merged := &SandboxSettings{}
		if out.Sandbox != nil {
			*merged = *out.Sandbox
		}
		mergeSandbox(merged, proj.Sandbox, true)
		out.Sandbox = merged
	}
	// Likewise skill self-authoring: off, never on.
	if proj.Skills != nil && proj.Skills.SelfAuthoring != nil && !*proj.Skills.SelfAuthoring {
		f := false
		out.Skills = &SkillsSettings{SelfAuthoring: &f}
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
	// Likewise the project sweep: a project file may turn it off; the roots it
	// walks stay the user's.
	if proj.VulnetixSweepEnabled != nil && !*proj.VulnetixSweepEnabled {
		f := false
		out.VulnetixSweepEnabled = &f
	}
	// Likewise autofix, which lets a review run `vulnetix fix --yes` on the
	// tree: a project file may turn it off, never on.
	if proj.Vulnetix != nil && proj.Vulnetix.AutoFix != nil && !*proj.Vulnetix.AutoFix {
		f := false
		if out.Vulnetix == nil {
			out.Vulnetix = &VulnetixSettings{}
		} else {
			v := *out.Vulnetix
			out.Vulnetix = &v
		}
		out.Vulnetix.AutoFix = &f
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
	// Tests is the post-end test pass: a project layer may turn it off and
	// lower its budgets, never on, and never touch the command.
	if proj.Tests != nil {
		out.Tests = tightenTests(out.Tests, proj.Tests)
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
		merged.mergeProject(proj.UI)
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
	// Tests is the post-end test pass: a project layer may turn it off and
	// lower its budgets, never on, and never touch the command.
	if proj.Tests != nil {
		out.Tests = tightenTests(out.Tests, proj.Tests)
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
		if proj.LSP.MaxRepairAttempts != 0 {
			if merged.MaxRepairAttempts == 0 {
				merged.MaxRepairAttempts = proj.LSP.MaxRepairAttempts
			} else {
				merged.MaxRepairAttempts = min(merged.MaxRepairAttempts, proj.LSP.MaxRepairAttempts)
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

// LoadGlobal reads the global settings file (~/.belai/settings.json).
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
	NormalizeKinds(&s)
	return s, nil
}

// SaveGlobal writes settings to ~/.belai/settings.json, creating directories
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

// DefaultMaxWorkers is how many fleet workers may run on one machine when
// agents.max_workers is unset.
const DefaultMaxWorkers = 4

// AgentsSettings governs fleet workers.
type AgentsSettings struct {
	// Enabled turns workers on or off. Default on.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxWorkers caps the workers running on this machine.
	MaxWorkers *int `json:"max_workers,omitempty"`
	// Publish allows a worker profile's workspace.publish (push a branch and
	// open a draft pull request). Default on.
	Publish *bool `json:"publish,omitempty"`
}

// tighten applies a project layer: it may turn Enabled and Publish off and
// lower MaxWorkers, never the reverse. A repository must not be able to
// start unattended agents, or let them push, on the user's account.
func (a *AgentsSettings) tighten(proj *AgentsSettings) *AgentsSettings {
	out := AgentsSettings{}
	if a != nil {
		out = *a
	}
	if proj == nil {
		return &out
	}
	f := false
	if proj.Enabled != nil && !*proj.Enabled {
		out.Enabled = &f
	}
	if proj.Publish != nil && !*proj.Publish {
		out.Publish = &f
	}
	if proj.MaxWorkers != nil {
		cur := DefaultMaxWorkers
		if out.MaxWorkers != nil {
			cur = *out.MaxWorkers
		}
		if n := *proj.MaxWorkers; n >= 0 && n < cur {
			out.MaxWorkers = &n
		}
	}
	return &out
}

// merge applies a user layer field by field.
func (a *AgentsSettings) merge(o *AgentsSettings) {
	if o.Enabled != nil {
		a.Enabled = o.Enabled
	}
	if o.MaxWorkers != nil {
		a.MaxWorkers = o.MaxWorkers
	}
	if o.Publish != nil {
		a.Publish = o.Publish
	}
}

// AgentsEnabled reports whether fleet workers may run. Default on.
func (s Settings) AgentsEnabled() bool {
	return s.Agents == nil || s.Agents.Enabled == nil || *s.Agents.Enabled
}

// AgentsPublishEnabled reports whether workers may push and open draft pull
// requests. Default on; a profile must still ask for it.
func (s Settings) AgentsPublishEnabled() bool {
	return s.Agents == nil || s.Agents.Publish == nil || *s.Agents.Publish
}

// MaxWorkers is the fleet worker cap on this machine.
func (s Settings) MaxWorkers() int {
	if s.Agents == nil || s.Agents.MaxWorkers == nil || *s.Agents.MaxWorkers < 0 {
		return DefaultMaxWorkers
	}
	return *s.Agents.MaxWorkers
}

// KanbanEnabled reports whether the global kanban board is on. Default on.
func (s Settings) KanbanEnabled() bool {
	return s.Kanban == nil || *s.Kanban
}

// DeferToolsEnabled reports whether tools outside the core set are advertised
// on demand through ToolSearch rather than on every request. Default on.
func (s Settings) DeferToolsEnabled() bool {
	return s.DeferTools == nil || *s.DeferTools
}
