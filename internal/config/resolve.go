package config

import (
	"os"
	"strconv"
	"strings"
)

// Source is the provenance of a setting value, ordered lowest to highest
// precedence: default < state < global < project < env < flag.
type Source string

const (
	SourceDefault Source = "default"
	SourceState   Source = "state"
	SourceGlobal  Source = "global"
	SourceProject Source = "project"
	SourceEnv     Source = "env"
	SourceFlag    Source = "flag"
)

// Effective is the merged view of every setting plus per-key provenance,
// keyed by the setting's JSON name.
type Effective struct {
	Settings Settings
	Origin   map[string]Source
	Notes    []string
}

// Resolve merges every settings source into one Effective view. Precedence,
// lowest to highest, is: defaults, state.json, global settings.json, project
// settings.json, environment, then CLI flags.
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
	// Project-layer workspace directory proposals are treated the same way:
	// a cloned repo must not be able to widen the sandbox by naming sensitive
	// paths. They are ignored unless the user's global settings opt in.
	if len(proj.WorkspaceDirs) > 0 && !global.AllowProjectWorkspaceDirsEnabled() {
		proj.WorkspaceDirs = nil
		eff.Notes = append(eff.Notes, "project workspace_dirs ignored (set allow_project_workspace_dirs in global settings to use them)")
	}
	eff.apply(proj, SourceProject)

	// 4. environment.
	eff.apply(Settings{
		Provider: firstNonEmpty(env("SIGNET_PROVIDER"), env("PI_PROVIDER")),
		Model:    env("SIGNET_MODEL"),
		Effort:   env("SIGNET_EFFORT"),
		Classifier: &ClassifierSettings{
			Provider: env("SIGNET_CLASSIFIER_PROVIDER"),
			Model:    env("SIGNET_CLASSIFIER_MODEL"),
			Effort:   env("SIGNET_CLASSIFIER_EFFORT"),
			Caveman:  envBool(env("SIGNET_CLASSIFIER_CAVEMAN")),
		},
	}, SourceEnv)

	// 5. CLI flags.
	eff.apply(flags, SourceFlag)

	if err := ValidateProviders(eff.Settings); err != nil {
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
	if s.Classifier != nil && !s.Classifier.IsZero() {
		if e.Settings.Classifier == nil {
			e.Settings.Classifier = &ClassifierSettings{}
		}
		e.Settings.Classifier.merge(s.Classifier)
		e.Origin["classifier"] = src
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
