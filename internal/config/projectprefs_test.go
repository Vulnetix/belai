package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPrefsRoundTrip(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	on := true
	off := false
	want := ProjectPrefs{
		Guardrails:      &off,
		AskPermission:   &off,
		FirewallEnabled: &on,
		Caveman:         &on,
		Mode:            "goal",
		Agent:           "nightly-audit",
	}
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) { *p = want }); err != nil {
		t.Fatalf("MutateProjectPrefs: %v", err)
	}
	got, err := LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if *got.Guardrails != *want.Guardrails ||
		*got.AskPermission != *want.AskPermission ||
		*got.FirewallEnabled != *want.FirewallEnabled ||
		*got.Caveman != *want.Caveman ||
		got.Mode != want.Mode ||
		got.Agent != want.Agent {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

func TestProjectPrefsMissingFileIsZero(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	got, err := LoadProjectPrefs(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if got != (ProjectPrefs{}) {
		t.Fatalf("missing file = %+v, want zero", got)
	}
}

func TestProjectPrefsTwoWorkdirsGetTwoFiles(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	a := t.TempDir()
	b := t.TempDir()
	on := true
	if err := MutateProjectPrefs(a, func(p *ProjectPrefs) { p.Caveman = &on }); err != nil {
		t.Fatalf("MutateProjectPrefs(a): %v", err)
	}
	pa, _ := ProjectPrefsPath(a)
	pb, _ := ProjectPrefsPath(b)
	if pa == pb {
		t.Fatalf("two workdirs mapped to the same prefs file %q", pa)
	}
	got, err := LoadProjectPrefs(b)
	if err != nil {
		t.Fatalf("LoadProjectPrefs(b): %v", err)
	}
	if got.Caveman != nil {
		t.Fatalf("workdir b saw workdir a's pref: %+v", got)
	}
}

// The prefs file is wholly owned by the TUI's toggles: the atomic write
// replaces the whole document rather than preserving unrelated keys.
func TestProjectPrefsWriteIsWholeFile(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()
	path, _ := ProjectPrefsPath(workdir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"mode":"plan","stray":"kept"}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) { p.Mode = "goal" }); err != nil {
		t.Fatalf("MutateProjectPrefs: %v", err)
	}
	got, err := LoadProjectPrefs(workdir)
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if got.Mode != "goal" {
		t.Fatalf("mode = %q, want goal", got.Mode)
	}
	data, _ := os.ReadFile(path)
	if string(data) == `{"mode":"plan","stray":"kept"}` {
		t.Fatal("stray key survived an unrelated write; the file is not wholly owned")
	}
}

// toSettings must adapt the allowlist into the shape Effective.apply consumes,
// including the nested Vulnetix.FirewallEnabled.
func TestProjectPrefsToSettings(t *testing.T) {
	on := true
	off := false
	p := ProjectPrefs{Guardrails: &off, AskPermission: &off, FirewallEnabled: &on, Caveman: &on, Mode: "plan", Agent: "a"}
	s := p.toSettings()
	if s.Guardrails == nil || *s.Guardrails {
		t.Fatalf("guardrails = %+v, want false", s.Guardrails)
	}
	if s.AskPermission == nil || *s.AskPermission {
		t.Fatalf("ask_permission = %+v, want false", s.AskPermission)
	}
	if !s.FirewallEnabled() {
		t.Fatalf("firewall should be enabled via Vulnetix.FirewallEnabled")
	}
	if !s.CavemanEnabled() {
		t.Fatalf("caveman should be enabled")
	}
}
