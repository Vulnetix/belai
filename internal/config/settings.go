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
	// Providers defines custom provider profiles, keyed by provider name.
	// Secrets never live here; they are resolved from api_key_env or the
	// credential backends.
	Providers map[string]ProviderProfile `json:"providers,omitempty"`
	// AllowProjectProviders opts in to project-layer provider definitions.
	// Defaults to false: a project file defining a provider is an API-key
	// exfiltration primitive, so it requires an explicit user opt-in.
	AllowProjectProviders *bool `json:"allow_project_providers,omitempty"`
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
	out.Permissions = out.Permissions.Merge(proj.Permissions)
	if proj.SessionRetentionDays != nil {
		out.SessionRetentionDays = proj.SessionRetentionDays
	}
	if proj.UI != nil {
		merged := &UISettings{}
		if out.UI != nil {
			*merged = *out.UI
		}
		if proj.UI.Banner != nil {
			merged.Banner = proj.UI.Banner
		}
		if proj.UI.StatusBar != nil {
			merged.StatusBar = proj.UI.StatusBar
		}
		if proj.UI.KittyKeyboard != nil {
			merged.KittyKeyboard = proj.UI.KittyKeyboard
		}
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
