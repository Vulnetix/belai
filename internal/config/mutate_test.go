package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOpenSettingsMissing(t *testing.T) {
	d, err := OpenSettings(ScopeGlobal, "")
	if err != nil {
		t.Fatalf("OpenSettings missing: %v", err)
	}
	if d.Settings.Model != "" {
		t.Fatalf("expected empty settings, got %+v", d.Settings)
	}
}

func TestDocumentSaveAndRoundTrip(t *testing.T) {
	dir := t.TempDir()

	d, err := OpenSettings(ScopeProject, dir)
	if err != nil {
		t.Fatalf("OpenSettings: %v", err)
	}
	d.Settings.Model = "test-model"
	d.Settings.Provider = "openai"
	if err := d.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d2, err := OpenSettings(ScopeProject, dir)
	if err != nil {
		t.Fatalf("OpenSettings round-trip: %v", err)
	}
	if d2.Settings.Model != "test-model" {
		t.Fatalf("model mismatch: want test-model, got %q", d2.Settings.Model)
	}
	if d2.Settings.Provider != "openai" {
		t.Fatalf("provider mismatch: want openai, got %q", d2.Settings.Provider)
	}
}

func TestDocumentPreservesUnmanagedKeys(t *testing.T) {
	dir := t.TempDir()
	path := ProjectSettingsPath(dir)
	if err := os.MkdirAll(dir+"/.vulnetix", 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write a file with both managed and unmanaged keys.
	original := []byte(`{"model":"m","provider":"p","unmanaged_key":"keep_me"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	d, err := OpenSettings(ScopeProject, dir)
	if err != nil {
		t.Fatalf("OpenSettings: %v", err)
	}
	d.Settings.Model = "new-model"
	if err := d.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if m["unmanaged_key"] != "keep_me" {
		t.Fatalf("unmanaged key lost: %v", m["unmanaged_key"])
	}
	if m["model"] != "new-model" {
		t.Fatalf("managed key not updated: %v", m["model"])
	}
}

func TestMutate(t *testing.T) {
	dir := t.TempDir()

	if err := Mutate(ScopeProject, dir, func(s *Settings) error {
		s.Model = "mutated"
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	got, err := LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Model != "mutated" {
		t.Fatalf("model: want mutated, got %q", got.Model)
	}
}

func TestIsUnsetJSON(t *testing.T) {
	if !isUnsetJSON([]byte("null")) {
		t.Fatal("null should be unset")
	}
	if !isUnsetJSON([]byte("{}")) {
		t.Fatal("{} should be unset")
	}
	if isUnsetJSON([]byte(`"x"`)) {
		t.Fatal("string should not be unset")
	}
}
