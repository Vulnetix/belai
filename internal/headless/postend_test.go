package headless

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testpass"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git unavailable: %v %s", err, out)
		}
	}
	return dir
}

func testsSettings(postEnd string, command ...string) config.Settings {
	return config.Settings{Tests: &config.TestsSettings{PostEnd: postEnd, Command: command}}
}

func TestShouldPostEndHonoursTheLevel(t *testing.T) {
	dir := gitRepo(t)
	ctx := context.Background()
	if ShouldPostEnd(ctx, testsSettings("off"), dir, testpass.TriggerGoal) {
		t.Error("off must never run")
	}
	if !ShouldPostEnd(ctx, testsSettings("goal"), dir, testpass.TriggerGoal) {
		t.Error("goal level must run after a completed goal")
	}
	if ShouldPostEnd(ctx, testsSettings("goal"), dir, testpass.TriggerSession) {
		t.Error("goal level must not run at session end")
	}
}

// A session-end pass only runs when the tree has something to test.
func TestShouldPostEndSessionNeedsAChangedTree(t *testing.T) {
	dir := gitRepo(t)
	ctx := context.Background()
	s := testsSettings("session")
	if ShouldPostEnd(ctx, s, dir, testpass.TriggerSession) {
		t.Fatal("a clean tree must not spend a suite at session end")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !ShouldPostEnd(ctx, s, dir, testpass.TriggerSession) {
		t.Fatal("a dirty tree must run the session-end pass")
	}
}

func TestRunPostEndPassesWithHarnessReport(t *testing.T) {
	dir := gitRepo(t)
	out := RunPostEnd(context.Background(), PostEnd{
		Cfg: run.Config{}, Posture: posture.Defaults(), Workdir: dir,
		Settings: testsSettings("goal", "sh", "-c", "exit 0"), Trigger: testpass.TriggerGoal,
	})
	if !out.Passed || out.Skipped != "" || !strings.HasPrefix(out.Report, "Tests passed:") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunPostEndFailureWithoutSessionHasNoLoop(t *testing.T) {
	dir := gitRepo(t)
	var notes []string
	out := RunPostEnd(context.Background(), PostEnd{
		Cfg: run.Config{}, Posture: posture.Defaults(), Workdir: dir,
		Settings: testsSettings("goal", "sh", "-c", "exit 1"), Trigger: testpass.TriggerGoal,
		Notify: func(s string) { notes = append(notes, s) },
	})
	if out.Passed || out.FixPasses != 0 || out.Report != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[0], "running tests") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestRunPostEndUsesTheSuppliedFixer(t *testing.T) {
	dir := gitRepo(t)
	calls := 0
	s := testsSettings("goal", "sh", "-c", "test -f fixed")
	out := RunPostEnd(context.Background(), PostEnd{
		Cfg: run.Config{}, Posture: posture.Defaults(), Workdir: dir, Settings: s, Trigger: testpass.TriggerGoal,
		Fix: func(_ context.Context, req testpass.FixRequest) error {
			calls++
			return os.WriteFile(filepath.Join(dir, "fixed"), nil, 0o644)
		},
	})
	if calls != 1 || !out.Passed || out.FixPasses != 1 {
		t.Fatalf("calls=%d outcome=%+v", calls, out)
	}
}

func TestRunPostEndOnFailOffNeverLoops(t *testing.T) {
	dir := gitRepo(t)
	s := testsSettings("goal", "sh", "-c", "exit 1")
	s.Tests.OnFail = "off"
	called := false
	RunPostEnd(context.Background(), PostEnd{
		Cfg: run.Config{}, Posture: posture.Defaults(), Workdir: dir, Settings: s, Trigger: testpass.TriggerGoal,
		Fix: func(context.Context, testpass.FixRequest) error { called = true; return nil },
	})
	if called {
		t.Fatal("on_fail off must not run the loop")
	}
}
