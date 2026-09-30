package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/acp"
	"github.com/vulnetix/belai/internal/config"
)

// An editor cannot open a session in a directory the user never trusted:
// the trust prompt never runs over ACP.
func TestACPRefusesUntrustedDirectory(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	_, err := buildACPSession(context.Background(), t.TempDir(), "id", "", "")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("err = %v", err)
	}
}

// With no flag an editor session runs on the provider the user configured,
// not on the OpenAI fallback.
func TestACPConfigHonoursSavedProvider(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_PROVIDER", "")
	t.Setenv("PI_PROVIDER", "")
	t.Setenv("BELAI_MODEL", "")
	path, err := config.GlobalSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"provider":"ollama","model":"saved-model"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, _, err := acpConfig(t.TempDir(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "ollama" || cfg.Model != "saved-model" {
		t.Fatalf("provider %q model %q, want ollama saved-model", cfg.Provider, cfg.Model)
	}
	// A flag still wins over the saved choice.
	cfg, _, _, err = acpConfig(t.TempDir(), "ollama", "flag-model")
	if err != nil || cfg.Model != "flag-model" {
		t.Fatalf("flag: %v %q", err, cfg.Model)
	}
}

// An editor's toggles reach the settings the session and its posture are
// built from, and only for that session: nothing is written.
func TestACPToggleOverridesSettings(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_PROVIDER", "ollama")
	off := acp.Toggles{Guardrails: false, Ask: false, Caveman: true}
	_, settings, pol, err := acpConfigWith(t.TempDir(), "ollama", "m", &off)
	if err != nil {
		t.Fatal(err)
	}
	if settings.GuardrailsEnabled() || settings.AskPermissionEnabled() || !settings.CavemanEnabled() {
		t.Fatalf("settings not overridden: %+v", settings)
	}
	if pol == nil {
		t.Fatal("no posture")
	}
	on := acp.Toggles{Guardrails: true, Ask: true}
	_, settings, _, err = acpConfigWith(t.TempDir(), "ollama", "m", &on)
	if err != nil || !settings.GuardrailsEnabled() || !settings.AskPermissionEnabled() || settings.CavemanEnabled() {
		t.Fatalf("on: %v %+v", err, settings)
	}
	if got := acpToggles(t.TempDir()); !got.Guardrails || !got.Ask || got.Caveman {
		t.Fatalf("defaults = %+v", got)
	}
}
