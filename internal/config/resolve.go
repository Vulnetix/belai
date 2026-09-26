package config

import (
	"os"
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
	// Auto-commit per task is global: a repo-visible settings file must never
	// be able to make the harness commit on the user's behalf. Dropped
	// unconditionally, before the project layer is applied.
	if proj.AutoCommitPerTask != nil {
		proj.AutoCommitPerTask = nil
		eff.Notes = append(eff.Notes, "project auto_commit_per_task ignored (auto-commit is global)")
	}
	if proj.LSP != nil && len(proj.LSP.Servers) > 0 {
		proj.LSP.Servers = nil
		eff.Notes = append(eff.Notes, "project lsp.servers ignored (binary paths may only be set in global settings)")
	}
	eff.apply(proj, SourceProject)

	// 4. environment.
	eff.apply(Settings{
		Provider:      firstNonEmpty(env("SIGNET_PROVIDER"), env("PI_PROVIDER")),
		Model:         env("SIGNET_MODEL"),
		Effort:        env("SIGNET_EFFORT"),
		Guardrails:    envBool(env("SIGNET_GUARDRAILS")),
		AskPermission: envBool(env("SIGNET_ASK_PERMISSION")),
		Vulnetix:      &VulnetixSettings{FirewallEnabled: envBool(env("SIGNET_FIREWALL"))},
		Classifier: &ClassifierSettings{
			Kind:     env("SIGNET_CLASSIFIER_KIND"),
			Provider: env("SIGNET_CLASSIFIER_PROVIDER"),
			Model:    env("SIGNET_CLASSIFIER_MODEL"),
			Effort:   env("SIGNET_CLASSIFIER_EFFORT"),
			Caveman:  envBool(env("SIGNET_CLASSIFIER_CAVEMAN")),
			Phase1: ClassifierPhaseSettings{
				Model:     env("SIGNET_CLASSIFIER_PHASE1_MODEL"),
				Source:    env("SIGNET_CLASSIFIER_PHASE1_SOURCE"),
				Threshold: envFloat(env("SIGNET_CLASSIFIER_PHASE1_THRESHOLD")),
			},
			Phase2: ClassifierPhaseSettings{
				Model:     env("SIGNET_CLASSIFIER_PHASE2_MODEL"),
				Source:    env("SIGNET_CLASSIFIER_PHASE2_SOURCE"),
				Threshold: envFloat(env("SIGNET_CLASSIFIER_PHASE2_THRESHOLD")),
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

	return eff, nil
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
	if s.Vulnetix != nil && s.Vulnetix.FirewallEnabled != nil {
		// Firewall is the mirror image: a repo-visible project layer may only
		// turn it off, never on, because routing prompts to a gateway must not
		// be something a cloned repository can opt the user into.
		if !*s.Vulnetix.FirewallEnabled || src != SourceProject {
			if e.Settings.Vulnetix == nil {
				e.Settings.Vulnetix = &VulnetixSettings{}
			}
			e.Settings.Vulnetix.FirewallEnabled = s.Vulnetix.FirewallEnabled
			e.Origin["firewall_enabled"] = src
		}
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
		e.Settings.UI.merge(s.UI)
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
