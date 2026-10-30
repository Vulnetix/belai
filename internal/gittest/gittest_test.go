package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEnvDropsRedirectionAndConfig(t *testing.T) {
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_INDEX_FILE", "/elsewhere/.git/index")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_AUTHOR_NAME", "kept")
	env := Env("EXTRA=1")
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if Inherited(key) {
			t.Errorf("Env kept %s", kv)
		}
	}
	for _, want := range []string{"GIT_AUTHOR_NAME=kept", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1", "EXTRA=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("Env lacks %s", want)
		}
	}
}

// A fixture built while GIT_DIR points at another repository still lands in
// its own directory, both through Env and after Isolate.
func TestFixtureIgnoresAnInheritedGitDir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	outer := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", outer).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(outer, ".git"))

	fixture := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = fixture
	cmd.Env = Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(fixture, ".git")); err != nil {
		t.Fatalf("the fixture has no repository of its own: %v", err)
	}

	Isolate(t)
	if _, ok := os.LookupEnv("GIT_DIR"); ok {
		t.Fatal("Isolate left GIT_DIR set")
	}
	cmd = exec.Command("git", "rev-parse", "--absolute-git-dir")
	cmd.Dir = fixture
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
	want, _ := filepath.EvalSymlinks(filepath.Join(fixture, ".git"))
	if got != want {
		t.Fatalf("git resolved %s, want %s", got, want)
	}
}

// Isolate puts the inherited value back when the test ends.
func TestIsolateRestores(t *testing.T) {
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	t.Run("isolated", func(t *testing.T) {
		Isolate(t)
		if _, ok := os.LookupEnv("GIT_WORK_TREE"); ok {
			t.Fatal("GIT_WORK_TREE still set")
		}
	})
	if got := os.Getenv("GIT_WORK_TREE"); got != "/elsewhere" {
		t.Fatalf("GIT_WORK_TREE = %q after the subtest, want it restored", got)
	}
}
