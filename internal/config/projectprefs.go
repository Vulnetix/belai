package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ProjectPrefs is the per-project, user-authored preference layer written by
// the TUI's own toggles. It lives under the user's global directory keyed by
// workdir, so a repository can never supply one. Its fields are an explicit
// allowlist rather than a second Settings: it can never define a provider, a
// permission rule or a workspace directory.
type ProjectPrefs struct {
	Guardrails      *bool  `json:"guardrails,omitempty"`
	AskPermission   *bool  `json:"ask_permission,omitempty"`
	FirewallEnabled *bool  `json:"firewall_enabled,omitempty"`
	Caveman         *bool  `json:"caveman,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Agent           string `json:"agent,omitempty"`
}

// toSettings adapts the allowlist into the Settings shape apply consumes.
// Mode and Agent are not settings keys and are read directly by the TUI.
func (p ProjectPrefs) toSettings() Settings {
	s := Settings{
		Guardrails:    p.Guardrails,
		AskPermission: p.AskPermission,
		Caveman:       p.Caveman,
	}
	if p.FirewallEnabled != nil {
		s.Vulnetix = &VulnetixSettings{FirewallEnabled: p.FirewallEnabled}
	}
	return s
}

// LoadProjectPrefs reads the per-project preference file for a working
// directory. A missing file yields the zero value with no error, matching
// LoadProject.
func LoadProjectPrefs(workdir string) (ProjectPrefs, error) {
	path, err := ProjectPrefsPath(workdir)
	if err != nil {
		return ProjectPrefs{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ProjectPrefs{}, nil
	}
	if err != nil {
		return ProjectPrefs{}, fmt.Errorf("read project prefs %s: %w", path, err)
	}
	var p ProjectPrefs
	if err := json.Unmarshal(data, &p); err != nil {
		return ProjectPrefs{}, fmt.Errorf("parse project prefs %s: %w", path, err)
	}
	return p, nil
}

// MutateProjectPrefs reads, applies fn, and atomically writes the per-project
// preference file. The file is wholly owned by the TUI's toggles, so the whole
// document is rewritten (unlike the shared settings namespace, which preserves
// unmanaged keys).
func MutateProjectPrefs(workdir string, fn func(*ProjectPrefs)) error {
	p, err := LoadProjectPrefs(workdir)
	if err != nil {
		return err
	}
	fn(&p)
	path, err := ProjectPrefsPath(workdir)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal project prefs: %w", err)
	}
	return writeFileAtomic(path, data, 0o700)
}
