package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectPrefsRoundTrip(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
	got, err := LoadProjectPrefs(t.TempDir())
	if err != nil {
		t.Fatalf("LoadProjectPrefs: %v", err)
	}
	if got != (ProjectPrefs{}) {
		t.Fatalf("missing file = %+v, want zero", got)
	}
}

func TestProjectPrefsTwoWorkdirsGetTwoFiles(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
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
	t.Setenv("BELAI_HOME", t.TempDir())
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
		t.Fatalf("firewall should be enabled via firewall.enabled")
	}
	if !s.CavemanEnabled() {
		t.Fatalf("caveman should be enabled")
	}
}

func TestProjectPrefsSessionControlKeysResolve(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on, off := true, false
	all := "all"
	simple := 0.85
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) {
		p.ShowReasoning = &on
		p.ShowEdits = &off
		p.ShowInternalWork = &all
		p.AutoCommitPerTask = &on
		p.Tests = &PrefsTests{PostEnd: "goal", OnFail: "diagnose"}
		p.LSP = &PrefsLSP{Enabled: &on, Languages: map[string]bool{"go": false}}
		p.Jev = &PrefsJev{Jobs: map[string]bool{string(JevBashSwap): false}, Thresholds: &JevThresholdSettings{SimpleAt: &simple}}
	}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	s := eff.Settings
	switch {
	case !s.ReasoningVisible(), s.EditsVisible(), s.InternalWorkLevel() != "all":
		t.Fatal("display keys")
	case !s.AutoCommitPerTaskEnabled():
		t.Fatal("auto-commit")
	case s.TestsPostEnd() != "goal", s.TestsOnFail() != "diagnose":
		t.Fatal("tests")
	case s.JevJobSet(JevBashSwap), s.JevThresholds().SimpleAt != 0.85:
		t.Fatal("jev")
	}
	if on, explicit := s.LSPLanguageEnabled("go"); on || !explicit {
		t.Fatal("lsp language")
	}
	if eff.Origin["auto_commit_per_task"] != SourceProjectPrefs {
		t.Fatalf("origin = %q", eff.Origin["auto_commit_per_task"])
	}
}

// A preference the validators refuse is left out with a note; it never stops
// Belai from starting, and the gate preferences beside it still apply.
func TestProjectPrefsInvalidKeysAreDropped(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()
	on := true
	allow := 0.5
	if err := MutateProjectPrefs(workdir, func(p *ProjectPrefs) {
		p.Caveman = &on
		p.AutoCommitPerTask = &on
		// Valid alone, but closes the band against the default deny_at once
		// moved past it.
		p.Jev = &PrefsJev{Thresholds: &JevThresholdSettings{AllowAt: &allow, DenyAt: &allow}}
	}); err != nil {
		t.Fatal(err)
	}
	eff, err := Resolve(workdir, func(string) string { return "" }, Settings{})
	if err != nil {
		t.Fatalf("Resolve failed on a bad preference: %v", err)
	}
	if !eff.Settings.CavemanEnabled() || eff.Settings.AutoCommitPerTaskEnabled() {
		t.Fatal("gate preferences kept, the rest dropped")
	}
	if len(eff.Notes) == 0 {
		t.Fatal("no note")
	}
}

func TestProjectPrefsFlatKeys(t *testing.T) {
	var p ProjectPrefs
	for k, v := range map[string]any{
		"guardrails": false, "mode": "plan", "show_internal_work": "all", "tests.on_fail": "fix",
		"lsp.enabled": true, "lsp.languages.rust": false, "jev.jobs.bash_swap": false, "jev.thresholds.keep_at": 0.456,
	} {
		if err := p.SetFlat(k, v); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
	}
	f := p.Flat()
	if f["jev.thresholds.keep_at"] != 0.46 || f["mode"] != "plan" || f["lsp.languages.rust"] != false {
		t.Fatalf("flat = %v", f)
	}
	for _, k := range []string{"lsp.languages.rust", "lsp.enabled", "jev.jobs.bash_swap", "jev.thresholds.keep_at", "tests.on_fail"} {
		if err := p.UnsetFlat(k); err != nil {
			t.Fatal(err)
		}
	}
	if p.LSP != nil || p.Jev != nil || p.Tests != nil {
		t.Fatalf("empty blocks kept: %+v", p)
	}
	for k, v := range map[string]any{
		"providers": "x", "agent": "nightly", "tests.command": "rm", "mode": "yolo", "guardrails": "off",
		"jev.thresholds.keep_at": 2.0, "jev.jobs.nope": true, "lsp.servers.go": "/bin/sh",
	} {
		if err := p.SetFlat(k, v); err == nil {
			t.Fatalf("%s=%v accepted", k, v)
		}
	}
	keys := PrefKeys()
	if len(keys) < 13+len(KnownLSPLanguages)+len(JevJobs) {
		t.Fatalf("keys = %d", len(keys))
	}
}
