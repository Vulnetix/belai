package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Settings holds user- and project-level configuration.
// Project settings override global settings field-by-field.
type Settings struct {
	// Model is the default model ID (e.g. "gpt-5", "claude-opus-4").
	Model string `json:"model,omitempty"`
	// Effort is the default effort/thinking level (e.g. "low", "medium", "high").
	Effort string `json:"effort,omitempty"`
	// Caveman, when non-nil, toggles the caveman voice rewrite.
	Caveman *bool `json:"caveman,omitempty"`
	// Permissions maps tool names to "allow", "block", or "ask".
	Permissions map[string]string `json:"permissions,omitempty"`
}

// Override merges project settings over the receiver (which should be the
// global settings). It returns the merged result and never mutates the
// receiver. Non-zero project fields win; nil/empty project fields fall back
// to the global value.
func (s Settings) Override(proj Settings) Settings {
	out := s
	if proj.Model != "" {
		out.Model = proj.Model
	}
	if proj.Effort != "" {
		out.Effort = proj.Effort
	}
	if proj.Caveman != nil {
		out.Caveman = proj.Caveman
	}
	if proj.Permissions != nil {
		if out.Permissions == nil {
			out.Permissions = make(map[string]string, len(proj.Permissions))
		}
		for k, v := range proj.Permissions {
			out.Permissions[k] = v
		}
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
