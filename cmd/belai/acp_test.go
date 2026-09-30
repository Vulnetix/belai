package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
