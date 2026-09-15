package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestMutatePreservesUnknownKeys(t *testing.T) {
	workdir := t.TempDir()
	path := ProjectSettingsPath(workdir)
	seed := `{"model":"a","vulnetix_extra":{"nested":{"x":1},"keep":true}}`
	writeFile(t, path, seed)

	if err := Mutate(ScopeProject, workdir, func(s *Settings) error {
		s.Model = "b"
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(raw["model"]) != `"b"` {
		t.Fatalf("model = %s, want b", raw["model"])
	}
	var extra map[string]any
	if err := json.Unmarshal(raw["vulnetix_extra"], &extra); err != nil {
		t.Fatalf("foreign key not valid JSON: %s (%v)", raw["vulnetix_extra"], err)
	}
	if extra["keep"] != true {
		t.Fatalf("foreign key lost: %s", raw["vulnetix_extra"])
	}
	nested, ok := extra["nested"].(map[string]any)
	if !ok || nested["x"] != float64(1) {
		t.Fatalf("foreign nested key lost: %s", raw["vulnetix_extra"])
	}
}

func TestMutateDeletesUnsetKeys(t *testing.T) {
	workdir := t.TempDir()
	path := ProjectSettingsPath(workdir)
	writeFile(t, path, `{"model":"a","effort":"high"}`)

	if err := Mutate(ScopeProject, workdir, func(s *Settings) error {
		s.Model = ""
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["model"]; ok {
		t.Fatalf("unset model key should be deleted: %s", data)
	}
	if _, ok := raw["effort"]; !ok {
		t.Fatalf("effort key should survive: %s", data)
	}
}

func TestMutateGlobalScope(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if err := Mutate(ScopeGlobal, "/ignored", func(s *Settings) error {
		s.Provider = "openai"
		return nil
	}); err != nil {
		t.Fatalf("Mutate global: %v", err)
	}
	g, err := LoadGlobal()
	if err != nil {
		t.Fatalf("LoadGlobal: %v", err)
	}
	if g.Provider != "openai" {
		t.Fatalf("global provider = %q", g.Provider)
	}
}

func TestMutateCreatesFile(t *testing.T) {
	workdir := t.TempDir()
	if err := Mutate(ScopeProject, workdir, func(s *Settings) error {
		s.Caveman = boolPtr(true)
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	got, err := LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Caveman == nil || !*got.Caveman {
		t.Fatalf("caveman = %v", got.Caveman)
	}
}

func TestSettingsFileNameAndMode(t *testing.T) {
	workdir := t.TempDir()
	if err := Mutate(ScopeProject, workdir, func(s *Settings) error {
		s.Model = "m"
		return nil
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	fi, err := os.Stat(ProjectSettingsPath(workdir))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings file mode = %o, want 600", fi.Mode().Perm())
	}
}
