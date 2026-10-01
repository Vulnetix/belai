package config

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// Source is the provenance of a setting value, ordered lowest to highest
// precedence: default < state < global < project_prefs < project < env < flag.
type Source string

const (
	SourceDefault      Source = "default"
	SourceState        Source = "state"
	SourceGlobal       Source = "global"
	SourceProjectPrefs Source = "project_prefs"
	SourceProject      Source = "project"
	SourceEnv          Source = "env"
	SourceFlag         Source = "flag"
)

// Effective is the merged view of every setting plus per-key provenance,
// keyed by the setting's JSON name.
type Effective struct {
	Settings Settings
	Origin   map[string]Source
	Notes    []string
}

// Resolve merges every settings source into one Effective view. Precedence,
// lowest to highest, is: defaults, state.json, global settings.json, the
// per-project user preference file, project settings.json, environment, then
// CLI flags.
func Resolve(workdir string, env func(string) string, flags Settings) (Effective, error) {
	if env == nil {
		env = os.Getenv
	}
	eff := Effective{Settings: Settings{}, Origin: map[string]Source{}}

	// 1. state.json — last-used runtime values.
	st, err := LoadState()
	if err != nil {
		return eff, err
	}
	eff.apply(Settings{Model: st.Model, Provider: st.Provider, Effort: st.Effort}, SourceState)

	// 2. global settings.json.
	global, err := LoadGlobal()
	if err != nil {
		return eff, err
	}
	eff.apply(global, SourceGlobal)

	// 2.5. per-project user preferences.
	prefs, err := LoadProjectPrefs(workdir)
	if err != nil {
		return eff, err
	}
	eff.apply(prefs.toSettings(), SourceProjectPrefs)

	// 3. project settings.json.
	proj, err := LoadProject(workdir)
	if err != nil {
		return eff, err
	}
	// Project-layer provider definitions are an API-key exfiltration
	// primitive: a hostile repo's .vulnetix/settings.json could define a
	// provider whose base URL is attacker-controlled. They are ignored unless
	// the user's global settings opt in explicitly.
	if len(proj.Providers) > 0 && !global.AllowProjectProvidersEnabled() {
		proj.Providers = nil
		eff.Notes = append(eff.Notes, "project providers ignored (set allow_project_providers in global settings to use them)")
	}
	// Display labels travel with the provider map: a label naming a provider
	// that is itself gated must not smuggle the reference through.
	if len(proj.ProviderLabels) > 0 && !global.AllowProjectProvidersEnabled() {
		proj.ProviderLabels = nil
		eff.Notes = append(eff.Notes, "project provider_labels ignored (set allow_project_providers in global settings to use them)")
	}
	// Project-layer workspace directory proposals are treated the same way:
	// a cloned repo must not be able to widen the sandbox by naming sensitive
	// paths. They are ignored unless the user's global settings opt in.
	if len(proj.WorkspaceDirs) > 0 && !global.AllowProjectWorkspaceDirsEnabled() {
		proj.WorkspaceDirs = nil
		eff.Notes = append(eff.Notes, "project workspace_dirs ignored (set allow_project_workspace_dirs in global settings to use them)")
	}
	// Token budgets are global: a repository must not be able to raise or
	// remove the limits a user set for their own spend.
	if len(proj.TokenBudgets) > 0 {
		proj.TokenBudgets = nil
		eff.Notes = append(eff.Notes, "project token_budgets ignored (budgets are global; set them in /budgets)")
	}
	// Plan-limit parsing is global: what a user learns about their own account
	// limits is theirs to decide, not a repository's.
	if proj.Intel != nil {
		proj.Intel = nil
		eff.Notes = append(eff.Notes, "project intel ignored (plan-limit parsing is global)")
	}
	// Auto-commit per task is global: a repo-visible settings file must never
	// be able to make the harness commit on the user's behalf. Dropped
	// unconditionally, before the project layer is applied.
	if proj.AutoCommitPerTask != nil {
		proj.AutoCommitPerTask = nil
		eff.Notes = append(eff.Notes, "project auto_commit_per_task ignored (auto-commit is global)")
	}
	// The test pass runs commands, so a repo-visible file may only turn it off
	// and lower its budgets; tightenTests drops the rest when it is applied.
	if projectTestsWidens(proj.Tests) {
		eff.Notes = append(eff.Notes, "project tests settings ignored except post_end off and lower budgets (the post-end test pass is the user's opt-in)")
	}
	if proj.LSP != nil && len(proj.LSP.Servers) > 0 {
		proj.LSP.Servers = nil
		eff.Notes = append(eff.Notes, "project lsp.servers ignored (binary paths may only be set in global settings)")
	}
	eff.apply(proj, SourceProject)

	// 4. environment.
	eff.apply(Settings{
		Provider:      firstNonEmpty(env("BELAI_PROVIDER"), env("PI_PROVIDER")),
		Model:         env("BELAI_MODEL"),
		Effort:        env("BELAI_EFFORT"),
		Guardrails:    envBool(env("BELAI_GUARDRAILS")),
		AskPermission: envBool(env("BELAI_ASK_PERMISSION")),
		Firewall:      &FirewallSettings{Enabled: envBool(env("BELAI_FIREWALL"))},
		Classifier: &ClassifierSettings{
			Kind:     env("BELAI_CLASSIFIER_KIND"),
			Provider: env("BELAI_CLASSIFIER_PROVIDER"),
			Model:    env("BELAI_CLASSIFIER_MODEL"),
			Effort:   env("BELAI_CLASSIFIER_EFFORT"),
			Caveman:  envBool(env("BELAI_CLASSIFIER_CAVEMAN")),
			Phase1: ClassifierPhaseSettings{
				Model:     env("BELAI_CLASSIFIER_PHASE1_MODEL"),
				Source:    env("BELAI_CLASSIFIER_PHASE1_SOURCE"),
				Threshold: envFloat(env("BELAI_CLASSIFIER_PHASE1_THRESHOLD")),
			},
			Phase2: ClassifierPhaseSettings{
				Model:     env("BELAI_CLASSIFIER_PHASE2_MODEL"),
				Source:    env("BELAI_CLASSIFIER_PHASE2_SOURCE"),
				Threshold: envFloat(env("BELAI_CLASSIFIER_PHASE2_THRESHOLD")),
			},
		},
	}, SourceEnv)

	// 5. CLI flags.
	eff.apply(flags, SourceFlag)

	if err := ValidateProviders(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateLSP(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateTokenBudgets(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateRouting(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateJev(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateVoice(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateTTS(eff.Settings); err != nil {
		return eff, err
	}
	if err := ValidateVulnetix(eff.Settings); err != nil {
		return eff, err
	}
	SetActiveJevThresholds(eff.Settings.JevThresholds())
	if err := ValidateFirewall(eff.Settings); err != nil {
		return eff, err
	}

	return eff, nil
}

// vulnetix returns the effective vulnetix block, creating it on first use.
func (e *Effective) vulnetix() *VulnetixSettings {
	if e.Settings.Vulnetix == nil {
		e.Settings.Vulnetix = &VulnetixSettings{}
	}
	return e.Settings.Vulnetix
}

// apply merges a partial settings view over eff, recording the provenance of
// every non-zero field it contributes.
func (e *Effective) apply(s Settings, src Source) {
	if s.Model != "" {
		e.Settings.Model = s.Model
		e.Origin["model"] = src
	}
	if s.Provider != "" {
		e.Settings.Provider = s.Provider
		e.Origin["provider"] = src
	}
	if s.Effort != "" {
		e.Settings.Effort = s.Effort
		e.Origin["effort"] = src
	}
	if s.Caveman != nil {
		e.Settings.Caveman = s.Caveman
		e.Origin["caveman"] = src
	}
	if s.Guardrails != nil {
		// The repo-visible project layer may only tighten: a cloned
		// .vulnetix/settings.json must not be able to disable the gates. Every
		// other layer, including the user's own project prefs, sets both ways.
		if *s.Guardrails || src != SourceProject {
			e.Settings.Guardrails = s.Guardrails
			e.Origin["guardrails"] = src
		}
	}
	if s.AskPermission != nil {
		if *s.AskPermission || src != SourceProject {
			e.Settings.AskPermission = s.AskPermission
			e.Origin["ask_permission"] = src
		}
	}
	if s = normalizeFirewall(s); s.Firewall != nil {
		// Firewall is the mirror image: a repo-visible project layer may only
		// turn it off, never on, and may never name or pick a gateway,
		// because routing prompts to one must not be something a cloned
		// repository can opt the user into.
		if e.Settings.Firewall == nil {
			e.Settings.Firewall = &FirewallSettings{}
		}
		before := e.Settings.Firewall.Enabled
		if mergeFirewall(e.Settings.Firewall, s.Firewall, src == SourceProject) {
			e.Notes = append(e.Notes, "project firewall instances and active choice ignored (firewalls are configured in /firewall)")
		}
		if e.Settings.Firewall.Enabled != before {
			e.Origin["firewall_enabled"] = src
		}
		if src != SourceProject && (s.Firewall.Active != "" || len(s.Firewall.Instances) > 0) {
			e.Origin["firewall"] = src
		}
	}
	if s.Vulnetix != nil && s.Vulnetix.GatewayURL != "" && src != SourceProject {
		// The legacy self-hosted gateway URL is where prompts go: the user's
		// own layers only.
		if e.Settings.Vulnetix == nil {
			e.Settings.Vulnetix = &VulnetixSettings{}
		}
		e.Settings.Vulnetix.GatewayURL = s.Vulnetix.GatewayURL
		e.Origin["gateway_url"] = src
	}
	// The background sweep that finds other .vulnetix projects under your home
	// directory: a repo-visible project layer may turn it off, never choose
	// where it walks, so a repository cannot point it at directories of its own.
	if s.VulnetixSweepEnabled != nil && (!*s.VulnetixSweepEnabled || src != SourceProject) {
		e.Settings.VulnetixSweepEnabled = s.VulnetixSweepEnabled
		e.Origin["vulnetix_sweep_enabled"] = src
	}
	if src != SourceProject && len(s.VulnetixSweepRoots) > 0 {
		e.Settings.VulnetixSweepRoots = slices.Clone(s.VulnetixSweepRoots)
		e.Origin["vulnetix_sweep_roots"] = src
	}
	if v := s.Vulnetix; v != nil {
		// The review keys. autofix lets `/vulnetix review` run `vulnetix fix
		// --yes` on the tree, so a repo-visible project layer may turn it off
		// but never on. subcommands and timeout shape what a review scans and
		// for how long: only the user's own layers set them, so a repository
		// cannot narrow the scanners that look at it.
		if v.AutoFix != nil && (!*v.AutoFix || src != SourceProject) {
			e.vulnetix().AutoFix = v.AutoFix
			e.Origin["vulnetix.autofix"] = src
		}
		if src != SourceProject && len(v.Subcommands) > 0 {
			e.vulnetix().Subcommands = slices.Clone(v.Subcommands)
			e.Origin["vulnetix.subcommands"] = src
		}
		if src != SourceProject && v.Timeout != "" {
			e.vulnetix().Timeout = v.Timeout
			e.Origin["vulnetix.timeout"] = src
		}
	}
	if s.Sync != nil {
		// Session sync sends transcripts off the machine and lets the website
		// prompt the session and answer its asks: a repo-visible project
		// layer may turn any of them off, never on.
		if src == SourceProject {
			e.Settings.Sync = mergeSyncOffOnly(e.Settings.Sync, s.Sync)
		} else {
			t := *s.Sync
			if e.Settings.Sync != nil {
				if t.Enabled == nil {
					t.Enabled = e.Settings.Sync.Enabled
				}
				if t.RemotePrompts == nil {
					t.RemotePrompts = e.Settings.Sync.RemotePrompts
				}
				if t.RemoteAnswers == nil {
					t.RemoteAnswers = e.Settings.Sync.RemoteAnswers
				}
			}
			e.Settings.Sync = &t
		}
		e.Origin["sync"] = src
	}
	if s.Notifications != nil && src != SourceProject {
		// Notifications are a per-user preference: a repository has no say
		// in whether, or how, the desktop is interrupted.
		if e.Settings.Notifications == nil {
			e.Settings.Notifications = &NotificationSettings{}
		}
		e.Settings.Notifications.merge(s.Notifications)
		e.Origin["notifications"] = src
	}
	if s.Voice != nil && src != SourceProject {
		// The microphone is the user's alone: a repository cannot switch it
		// on, name its device or make what is dictated send itself.
		if e.Settings.Voice == nil {
			e.Settings.Voice = &VoiceSettings{}
		}
		e.Settings.Voice.merge(s.Voice)
		e.Origin["voice"] = src
	}
	if s.TTS != nil && src != SourceProject {
		// Text read aloud leaves the machine: only the user's own layers may
		// turn it on, name a voice or set the cache.
		if e.Settings.TTS == nil {
			e.Settings.TTS = &TTSSettings{}
		}
		e.Settings.TTS.merge(s.TTS)
		e.Origin["tts"] = src
	}
	if s.Telemetry != nil && src != SourceProject {
		// Where session facts are sent is the user's choice alone.
		t := *s.Telemetry
		e.Settings.Telemetry = &t
		e.Origin["telemetry"] = src
	}
	if s.MCP != nil && src != SourceProject {
		// MCP servers are commands to run and URLs to send data to: only the
		// user's own layers may name them. Later layers replace earlier ones
		// server by server.
		if e.Settings.MCP == nil {
			e.Settings.MCP = &MCPSettings{}
		}
		if e.Settings.MCP.Servers == nil {
			e.Settings.MCP.Servers = map[string]MCPServer{}
		}
		for name, srv := range s.MCP.Servers {
			e.Settings.MCP.Servers[name] = srv
		}
		e.Origin["mcp"] = src
	}
	if s.Sandbox != nil {
		// The sandbox is a boundary: a repo-visible project layer may only
		// tighten it (see mergeSandbox).
		if e.Settings.Sandbox == nil {
			e.Settings.Sandbox = &SandboxSettings{}
		}
		mergeSandbox(e.Settings.Sandbox, s.Sandbox, src == SourceProject)
		e.Origin["sandbox"] = src
	}
	if s.Skills != nil && s.Skills.SelfAuthoring != nil {
		// Self-authoring writes files after an ask; a repo-visible project
		// layer may turn it off, never on.
		if !*s.Skills.SelfAuthoring || src != SourceProject {
			e.Settings.Skills = &SkillsSettings{SelfAuthoring: s.Skills.SelfAuthoring}
			e.Origin["skills_self_authoring"] = src
		}
	}
	if s.Hooks != nil && s.Hooks.Enabled != nil {
		// Hooks run the user's own commands; a repo-visible project layer may
		// turn them off, never on.
		if !*s.Hooks.Enabled || src != SourceProject {
			e.Settings.Hooks = &HooksSettings{Enabled: s.Hooks.Enabled}
			e.Origin["hooks_enabled"] = src
		}
	}
	if s.Vulnetix != nil && s.Vulnetix.DepWatch != nil {
		// The dependency hook is a check, like the guardrails: a repo-visible
		// project layer may turn it on but never off, so a cloned repository
		// cannot silence the check on the dependencies it asks you to add.
		if *s.Vulnetix.DepWatch || src != SourceProject {
			if e.Settings.Vulnetix == nil {
				e.Settings.Vulnetix = &VulnetixSettings{}
			}
			e.Settings.Vulnetix.DepWatch = s.Vulnetix.DepWatch
			e.Origin["dep_watch"] = src
		}
	}
	if s.ReadOnly != nil || s.BashReadOnly != nil {
		if s.ReadOnly != nil {
			e.Settings.ReadOnly = s.ReadOnly
		}
		if s.BashReadOnly != nil {
			// Deprecated alias: read_only wins when both are present.
			if e.Settings.ReadOnly == nil {
				e.Settings.ReadOnly = s.BashReadOnly
			}
		}
		e.Settings.BashReadOnly = nil
		e.Origin["read_only"] = src
	}
	if !s.Permissions.IsZero() {
		e.Settings.Permissions = e.Settings.Permissions.Merge(s.Permissions)
		e.Origin["permissions"] = src
	}
	if s.SessionRetentionDays != nil {
		e.Settings.SessionRetentionDays = s.SessionRetentionDays
		e.Origin["session_retention_days"] = src
	}
	if s.UI != nil {
		if e.Settings.UI == nil {
			e.Settings.UI = &UISettings{}
		}
		if src == SourceProject {
			e.Settings.UI.mergeProject(s.UI)
		} else {
			e.Settings.UI.merge(s.UI)
		}
		e.Origin["ui"] = src
	}
	if s.ContextWindows != nil {
		if e.Settings.ContextWindows == nil {
			e.Settings.ContextWindows = map[string]int{}
		}
		for k, v := range s.ContextWindows {
			e.Settings.ContextWindows[k] = v
		}
		e.Origin["context_windows"] = src
	}
	if s.Providers != nil {
		if e.Settings.Providers == nil {
			e.Settings.Providers = map[string]ProviderProfile{}
		}
		for k, v := range s.Providers {
			e.Settings.Providers[k] = v
		}
		e.Origin["providers"] = src
	}
	if s.ProviderLabels != nil {
		if e.Settings.ProviderLabels == nil {
			e.Settings.ProviderLabels = map[string]string{}
		}
		for k, v := range s.ProviderLabels {
			e.Settings.ProviderLabels[k] = v
		}
		e.Origin["provider_labels"] = src
	}
	if s.AllowProjectProviders != nil {
		e.Settings.AllowProjectProviders = s.AllowProjectProviders
		e.Origin["allow_project_providers"] = src
	}
	if s.ShowSessionNames != nil {
		e.Settings.ShowSessionNames = s.ShowSessionNames
		e.Origin["show_session_names"] = src
	}
	if s.UpdateCheck != nil {
		e.Settings.UpdateCheck = s.UpdateCheck
		e.Origin["update_check"] = src
	}
	if s.AutoCommitPerTask != nil {
		e.Settings.AutoCommitPerTask = s.AutoCommitPerTask
		e.Origin["auto_commit_per_task"] = src
	}
	if s.Tests != nil {
		// The post-end test pass runs commands: a repo-visible project layer
		// may only turn it off and lower its budgets, never on or change the
		// command (see tightenTests).
		if src == SourceProject {
			e.Settings.Tests = tightenTests(e.Settings.Tests, s.Tests)
		} else {
			e.Settings.Tests = mergeTests(e.Settings.Tests, s.Tests)
		}
		e.Origin["tests"] = src
	}
	if s.DeferTools != nil {
		// Deferral changes only which tool definitions ride on a request,
		// never what may run, so any layer may set it.
		e.Settings.DeferTools = s.DeferTools
		e.Origin["defer_tools"] = src
	}
	if s.Offload != nil {
		// Offload changes only how much admitted content rides on a request,
		// never what is admitted or classified, so any layer may set it.
		e.Settings.Offload = mergeOffload(e.Settings.Offload, s.Offload)
		e.Origin["offload"] = src
	}
	if s.Jev != nil {
		// Jev jobs narrow or reorder what a request carries and never approve
		// anything, but a repository still may not switch one on or widen where
		// file previews go.
		e.Settings.Jev = mergeJev(e.Settings.Jev, s.Jev, src == SourceProject)
		e.Origin["jev"] = src
	}
	if s.Kanban != nil && (!*s.Kanban || src != SourceProject) {
		// The board syncs off the machine and carries text between
		// sessions: a repo-visible project layer may turn it off, never on.
		e.Settings.Kanban = s.Kanban
		e.Origin["kanban"] = src
	}
	if s.Screenshot != nil {
		e.Settings.Screenshot = mergeScreenshot(e.Settings.Screenshot, s.Screenshot, src == SourceProject)
		e.Origin["screenshot"] = src
	}
	if s.Agents != nil {
		// Fleet workers run unattended on the user's account: a project
		// layer may turn them (or their publishing) off and lower the
		// worker cap, never the reverse.
		if src == SourceProject {
			e.Settings.Agents = e.Settings.Agents.tighten(s.Agents)
		} else {
			if e.Settings.Agents == nil {
				e.Settings.Agents = &AgentsSettings{}
			}
			e.Settings.Agents.merge(s.Agents)
		}
		e.Origin["agents"] = src
	}
	if s.Classifier != nil && !s.Classifier.IsZero() {
		if e.Settings.Classifier == nil {
			e.Settings.Classifier = &ClassifierSettings{}
		}
		e.Settings.Classifier.merge(s.Classifier)
		e.Origin["classifier"] = src
	}
	if s.LSP != nil && !s.LSP.IsZero() {
		if e.Settings.LSP == nil {
			e.Settings.LSP = &LSPSettings{}
		}
		e.Settings.LSP.merge(s.LSP)
		e.Origin["lsp"] = src
	}
	if s.Resilience != nil {
		if e.Settings.Resilience == nil {
			e.Settings.Resilience = &ResilienceSettings{}
		}
		e.Settings.Resilience.merge(s.Resilience)
		e.Origin["resilience"] = src
	}
	if s.Routing != nil && !s.Routing.IsZero() {
		if e.Settings.Routing == nil {
			e.Settings.Routing = &RoutingSettings{}
		}
		e.Settings.Routing.merge(s.Routing)
		e.Origin["routing"] = src
	}
	if s.TokenBudgets != nil {
		// Replace, not append: the global list is the whole set.
		e.Settings.TokenBudgets = s.TokenBudgets
		e.Origin["token_budgets"] = src
	}
	if s.Intel != nil && s.Intel.PlanLimits != nil {
		if e.Settings.Intel == nil {
			e.Settings.Intel = &IntelSettings{}
		}
		e.Settings.Intel.PlanLimits = s.Intel.PlanLimits
		e.Origin["intel"] = src
	}
	if s.WorkspaceDirs != nil {
		// Later layers replace, not append, so a project layer can narrow the
		// set of allowed workspace directories.
		e.Settings.WorkspaceDirs = s.WorkspaceDirs
		e.Origin["workspace_dirs"] = src
	}
}

// envBool parses a boolean environment variable into a tri-state pointer: nil
// when the variable is unset or unparseable, so an absent variable never
// claims provenance over a stored setting.
func envBool(v string) *bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return nil
	}
	return &b
}

// envFloat parses a float environment variable; zero when unset or
// unparseable, so an absent variable never claims provenance.
func envFloat(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0
	}
	return f
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
