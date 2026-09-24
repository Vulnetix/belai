package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGlobalDerivedPaths pins every <GlobalDir>/... derivation that was not
// already covered, so a renamed constant or a dropped segment fails the suite.
func TestGlobalDerivedPaths(t *testing.T) {
	t.Setenv("SIGNET_HOME", "/custom/signet")
	const dir = "/custom/signet"

	cases := []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"GlobalSettingsPath", GlobalSettingsPath, filepath.Join(dir, "settings.json")},
		{"GlobalStatePath", GlobalStatePath, filepath.Join(dir, "state.json")},
		{"UserCredentialsPath", UserCredentialsPath, filepath.Join(dir, "credentials.json")},
		{"SessionsDir", SessionsDir, filepath.Join(dir, "sessions")},
		{"GlobalPromptsDir", GlobalPromptsDir, filepath.Join(dir, "prompts")},
		{"GlobalProcessesDir", GlobalProcessesDir, filepath.Join(dir, "processes")},
		{"ProcessLogsDir", ProcessLogsDir, filepath.Join(dir, "logs")},
		{"GlobalSkillsDir", GlobalSkillsDir, filepath.Join(dir, "skills")},
		{"GlobalHooksDir", GlobalHooksDir, filepath.Join(dir, "hooks")},
	}
	for _, tc := range cases {
		got, err := tc.got()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestProjectProcessesAndPromptsDirs(t *testing.T) {
	const workdir = "/tmp/proj"
	if got := ProjectPromptsDir(workdir); got != "/tmp/proj/.vulnetix/prompts" {
		t.Errorf("ProjectPromptsDir = %q", got)
	}
	if got := ProjectProcessesDir(workdir); got != "/tmp/proj/.vulnetix/processes" {
		t.Errorf("ProjectProcessesDir = %q", got)
	}
}

func TestInputHistoryPath(t *testing.T) {
	t.Setenv("SIGNET_HOME", "/custom/signet")
	got, err := InputHistoryPath("/tmp/work")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/custom/signet", "inputhistory", WorkdirKey("/tmp/work")+".json")
	if got != want {
		t.Fatalf("InputHistoryPath = %q, want %q", got, want)
	}
}

func TestWorkdirKeyDeterministicAndRoot(t *testing.T) {
	if WorkdirKey("/tmp/work") != WorkdirKey("/tmp/work") {
		t.Fatal("WorkdirKey must be deterministic")
	}
	// The filesystem root collapses its base to "root" so the key never ends
	// up empty or separator-shaped.
	if got := WorkdirKey(string(filepath.Separator)); !strings.HasPrefix(got, "root-") {
		t.Fatalf("WorkdirKey(root) = %q, want root- prefix", got)
	}
}
