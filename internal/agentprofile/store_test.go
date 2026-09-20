package agentprofile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/tools"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	resetDir(t)
	p := AgentProfile{
		Name:         "test-bot",
		Description:  "A test agent",
		SystemPrompt: "You are a test agent.",
		Mode:         ModeSingle,
		Tools:        []string{"Read", "Bash"},
		Autonomy:     AutonomySupervised,
	}
	path, err := Save(p)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	loaded, err := Load("test-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Name != p.Name || loaded.Description != p.Description || loaded.SystemPrompt != p.SystemPrompt {
		t.Fatalf("loaded profile mismatch")
	}
}

func TestValidationErrors(t *testing.T) {
	resetDir(t)
	cases := []struct {
		name    string
		profile AgentProfile
		wantErr string
	}{
		{"missing name", AgentProfile{Description: "d", SystemPrompt: "s", Mode: ModeSingle}, "name is required"},
		{"missing description", AgentProfile{Name: "x", SystemPrompt: "s", Mode: ModeSingle}, "description is required"},
		{"missing system_prompt", AgentProfile{Name: "x", Description: "d", Mode: ModeSingle}, "system_prompt is required"},
		{"invalid mode", AgentProfile{Name: "x", Description: "d", SystemPrompt: "s", Mode: "fly"}, `invalid mode "fly"`},
		{"scheduled without schedule", AgentProfile{Name: "x", Description: "d", SystemPrompt: "s", Mode: ModeScheduled}, "schedule is required when mode is scheduled"},
		{"monitor without condition", AgentProfile{Name: "x", Description: "d", SystemPrompt: "s", Mode: ModeMonitor}, "monitor_condition is required when mode is monitor"},
		{"unknown tool", AgentProfile{Name: "x", Description: "d", SystemPrompt: "s", Mode: ModeSingle, Tools: []string{"Nope"}}, `unknown tool "Nope"`},
		{"invalid autonomy", AgentProfile{Name: "x", Description: "d", SystemPrompt: "s", Mode: ModeSingle, Autonomy: "rogue"}, `invalid autonomy "rogue"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Save(c.profile)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error = %q, want containing %q", err, c.wantErr)
			}
		})
	}
}

func TestListAndDeleteOrdering(t *testing.T) {
	resetDir(t)
	profiles := []AgentProfile{
		{Name: "zebra", Description: "z", SystemPrompt: "z", Mode: ModeSingle},
		{Name: "alpha", Description: "a", SystemPrompt: "a", Mode: ModeSingle},
		{Name: "mango", Description: "m", SystemPrompt: "m", Mode: ModeSingle},
	}
	for _, p := range profiles {
		if _, err := Save(p); err != nil {
			t.Fatalf("Save %q: %v", p.Name, err)
		}
	}
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) < 3 {
		t.Fatalf("len(List) = %d, want at least 3", len(list))
	}
	// The first three should be user profiles sorted by name; built-ins are
	// appended at the end.
	want := []string{"alpha", "mango", "zebra"}
	for i := 0; i < len(want); i++ {
		if list[i].Name != want[i] {
			t.Fatalf("List[%d].Name = %q, want %q", i, list[i].Name, want[i])
		}
	}
	if err := Delete("mango"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	list, err = List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(list) < 2 {
		t.Fatalf("len(List) after delete = %d, want at least 2", len(list))
	}
}

func resetDir(t *testing.T) {
	t.Helper()
	d, err := Dir()
	if err != nil {
		t.Skipf("Dir() failed: %v", err)
	}
	_ = os.RemoveAll(d)
	t.Cleanup(func() { _ = os.RemoveAll(d) })
}

// Agent profiles live under the global directory, not the legacy ~/.signet
// path, and SIGNET_HOME moves them with everything else.
func TestDirUnderGlobalDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGNET_HOME", home)

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	want := filepath.Join(home, "profiles", "agents")
	if got != want {
		t.Fatalf("Dir() = %q, want %q", got, want)
	}
}

func TestListAndLoadSetFile(t *testing.T) {
	resetDir(t)
	p := AgentProfile{
		Name:         "file-bot",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
	}
	if _, err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load("file-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.File != "file-bot.json" {
		t.Fatalf("Load().File = %q, want file-bot.json", loaded.File)
	}
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, lp := range list {
		if lp.Name == "file-bot" {
			found = true
			if lp.File != "file-bot.json" {
				t.Fatalf("List().File = %q, want file-bot.json", lp.File)
			}
		}
	}
	if !found {
		t.Fatal("file-bot missing from List")
	}
}

func TestLoadFindsProfileAfterFileNameDiverges(t *testing.T) {
	resetDir(t)
	p := AgentProfile{
		Name:         "orig-bot",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
	}
	if _, err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	dir, _ := Dir()
	// Rename the file out from under the profile without changing its Name.
	if err := os.Rename(filepath.Join(dir, "orig-bot.json"), filepath.Join(dir, "renamed.json")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	loaded, err := Load("orig-bot")
	if err != nil {
		t.Fatalf("Load by name after rename: %v", err)
	}
	if loaded.Name != "orig-bot" || loaded.File != "renamed.json" {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestSaveMovingWritesNewAndRemovesOld(t *testing.T) {
	resetDir(t)
	p := AgentProfile{
		Name:         "move-bot",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
	}
	oldPath, err := Save(p)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	p.File = "moved.json"
	newPath, err := SaveMoving(p, "move-bot.json")
	if err != nil {
		t.Fatalf("SaveMoving: %v", err)
	}
	if newPath != filepath.Join(filepath.Dir(oldPath), "moved.json") {
		t.Fatalf("newPath = %q", newPath)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old file still exists: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("new file missing: %v", err)
	}

	// Same-name save is a no-op for the removal step.
	p.File = "moved.json"
	if _, err := SaveMoving(p, "moved.json"); err != nil {
		t.Fatalf("same-name SaveMoving: %v", err)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("same-name save removed the file: %v", err)
	}
}

func TestSaveRefusesToOverwriteDifferentName(t *testing.T) {
	resetDir(t)
	first := AgentProfile{Name: "first", Description: "d", SystemPrompt: "sp", Mode: ModeSingle}
	if _, err := Save(first); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	second := AgentProfile{Name: "second", Description: "d", SystemPrompt: "sp", Mode: ModeSingle, File: "first.json"}
	if _, err := Save(second); err == nil || !strings.Contains(err.Error(), "holds profile") {
		t.Fatalf("Save second = %v, want overwrite refusal", err)
	}
}

// TestKnownToolNamesMatchesDefaultRegistry pins the hardcoded allowlist against
// the live default registry, so adding a tool can never silently strand a
// profile from it.
func TestKnownToolNamesMatchesDefaultRegistry(t *testing.T) {
	reg := tools.Default(t.TempDir(), false)
	for _, name := range reg.Names() {
		if !knownToolNames[name] {
			t.Fatalf("default registry tool %q missing from knownToolNames", name)
		}
	}
	for name := range knownToolNames {
		if _, ok := reg.Find(name); !ok {
			t.Fatalf("knownToolNames contains %q not in the default registry", name)
		}
	}
}
