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
// permission rule, a command, a path or a workspace directory.
//
// The display, auto-commit, tests, language-server and Jev fields are the
// per-project form of the session controls (internal/sessionctl). The website
// edits them through `belai rc --web-project-settings`; only the keys here can
// be named, and each is validated before it is written.
type ProjectPrefs struct {
	Guardrails        *bool   `json:"guardrails,omitempty"`
	AskPermission     *bool   `json:"ask_permission,omitempty"`
	FirewallEnabled   *bool   `json:"firewall_enabled,omitempty"`
	Caveman           *bool   `json:"caveman,omitempty"`
	Mode              string  `json:"mode,omitempty"`
	Agent             string  `json:"agent,omitempty"`
	ShowReasoning     *bool   `json:"show_reasoning,omitempty"`
	ShowToolCalls     *bool   `json:"show_tool_calls,omitempty"`
	ShowEdits         *bool   `json:"show_edits,omitempty"`
	ShowInternalWork  *string `json:"show_internal_work,omitempty"`
	AutoCommitPerTask *bool   `json:"auto_commit_per_task,omitempty"`
	// Tests holds the trigger and the fail branch only: the command and scope
	// stay in the user's settings.
	Tests *PrefsTests `json:"tests,omitempty"`
	LSP   *PrefsLSP   `json:"lsp,omitempty"`
	Jev   *PrefsJev   `json:"jev,omitempty"`
}

// PrefsTests is the part of the post-end test pass a project preference sets.
type PrefsTests struct {
	PostEnd string `json:"post_end,omitempty"`
	OnFail  string `json:"on_fail,omitempty"`
}

// PrefsLSP is the part of the language-server settings a project preference
// sets. It names no binary.
type PrefsLSP struct {
	Enabled   *bool           `json:"enabled,omitempty"`
	Languages map[string]bool `json:"languages,omitempty"`
}

// PrefsJev holds per-project Jev job switches and cut-offs.
type PrefsJev struct {
	Jobs       map[string]bool       `json:"jobs,omitempty"`
	Thresholds *JevThresholdSettings `json:"thresholds,omitempty"`
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
		s.Firewall = &FirewallSettings{Enabled: p.FirewallEnabled}
	}
	if p.ShowReasoning != nil || p.ShowToolCalls != nil || p.ShowEdits != nil || p.ShowInternalWork != nil {
		s.UI = &UISettings{ShowReasoning: p.ShowReasoning, ShowToolCalls: p.ShowToolCalls, ShowEdits: p.ShowEdits, ShowInternalWork: p.ShowInternalWork}
	}
	s.AutoCommitPerTask = p.AutoCommitPerTask
	if p.Tests != nil && (p.Tests.PostEnd != "" || p.Tests.OnFail != "") {
		s.Tests = &TestsSettings{PostEnd: p.Tests.PostEnd, OnFail: p.Tests.OnFail}
	}
	if p.LSP != nil && (p.LSP.Enabled != nil || len(p.LSP.Languages) > 0) {
		s.LSP = &LSPSettings{Enabled: p.LSP.Enabled, Languages: p.LSP.Languages}
	}
	if p.Jev != nil && (len(p.Jev.Jobs) > 0 || p.Jev.Thresholds != nil) {
		s.Jev = &JevSettings{Jobs: p.Jev.Jobs, Thresholds: p.Jev.Thresholds}
	}
	return s
}

// Validate refuses a value the settings validators would refuse, so a
// preference file never stops Belai from starting.
func (p ProjectPrefs) Validate() error {
	if p.ShowInternalWork != nil {
		switch *p.ShowInternalWork {
		case "hidden", "decisions", "security", "all":
		default:
			return fmt.Errorf("show_internal_work %q must be hidden, decisions, security or all", *p.ShowInternalWork)
		}
	}
	if p.Tests != nil {
		switch p.Tests.PostEnd {
		case "", "off", "goal", "goal_plan", "session":
		default:
			return fmt.Errorf("tests.post_end %q must be off, goal, goal_plan or session", p.Tests.PostEnd)
		}
		switch p.Tests.OnFail {
		case "", "off", "diagnose", "fix":
		default:
			return fmt.Errorf("tests.on_fail %q must be off, diagnose or fix", p.Tests.OnFail)
		}
	}
	s := p.toSettings()
	if err := ValidateLSP(s); err != nil {
		return err
	}
	return ValidateJev(s)
}

// ValidateOver is Validate plus the cut-offs checked together with the user's
// global ones, which they overlay: a pair of thresholds is valid only as the
// session would read it.
func (p ProjectPrefs) ValidateOver(global Settings) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Jev == nil || p.Jev.Thresholds == nil {
		return nil
	}
	merged := mergeJev(global.Jev, &JevSettings{Thresholds: p.Jev.Thresholds}, false)
	return merged.Thresholds.Validate()
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
