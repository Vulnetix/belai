package tui

import (
	"testing"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/run"
)

func TestSettingsProviderEditClearsModelAndSyncs(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		s.Provider = "openai"
		s.Model = "gpt-5"
		return nil
	}); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeGlobal
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reloadSettings: %v", err)
	}

	row, idx := settingsRowByKey(a, "provider")
	if idx < 0 {
		t.Fatal("no provider row")
	}
	if err := a.commitTextRow(row, "anthropic"); err != nil {
		t.Fatalf("commitTextRow: %v", err)
	}

	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.Provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", got.Provider)
	}
	if got.Model != "" {
		t.Fatalf("model = %q, want cleared on provider change", got.Model)
	}

	// The running config adopts the new provider and its default model once
	// synced (the old model was cleared in settings).
	_ = a.syncProviderFromSettings()
	if a.cfg.Provider != "anthropic" {
		t.Fatalf("cfg.Provider = %q, want anthropic", a.cfg.Provider)
	}
	if a.cfg.Model != run.DefaultModel("anthropic") {
		t.Fatalf("cfg.Model = %q, want anthropic default %q", a.cfg.Model, run.DefaultModel("anthropic"))
	}
	if a.requestedProvider != "anthropic" {
		t.Fatalf("requestedProvider = %q, want anthropic", a.requestedProvider)
	}
}

func TestSettingsProviderUnsetSyncs(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	workdir := t.TempDir()

	if err := config.Mutate(config.ScopeGlobal, workdir, func(s *config.Settings) error {
		s.Provider = "anthropic"
		s.Model = "claude-sonnet-4-5"
		return nil
	}); err != nil {
		t.Fatalf("seed global: %v", err)
	}

	a := New(Options{Workdir: workdir})
	a.push(viewSettings)
	a.settingsState.scope = config.ScopeGlobal
	if err := a.reloadSettings(); err != nil {
		t.Fatalf("reloadSettings: %v", err)
	}

	if err := a.unsetSetting("provider"); err != nil {
		t.Fatalf("unsetSetting: %v", err)
	}
	got, err := config.LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if got.Provider != "" || got.Model != "" {
		t.Fatalf("provider/model = %q/%q, want both cleared", got.Provider, got.Model)
	}

	_ = a.syncProviderFromSettings()
	if a.cfg.Provider != "openrouter" {
		t.Fatalf("cfg.Provider = %q, want the openrouter default after unset", a.cfg.Provider)
	}
}
