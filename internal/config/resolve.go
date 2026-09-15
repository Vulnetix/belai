package config

import (
	"os"
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
	eff.apply(proj, SourceProject)

	// 4. environment.
	eff.apply(Settings{
		Provider: firstNonEmpty(env("SIGNET_PROVIDER"), env("PI_PROVIDER")),
		Model:    env("SIGNET_MODEL"),
		Effort:   env("SIGNET_EFFORT"),
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
	if s.BashReadOnly != nil {
		e.Settings.BashReadOnly = s.BashReadOnly
		e.Origin["bash_readonly"] = src
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
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
