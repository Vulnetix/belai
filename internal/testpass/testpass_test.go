package testpass

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/testdetect"
)

func settings(postEnd, onFail string, extra ...string) config.Settings {
	return config.Settings{Tests: &config.TestsSettings{
		PostEnd: postEnd,
		OnFail:  onFail,
		Command: extra,
	}}
}

func TestShould(t *testing.T) {
	cases := []struct {
		level string
		want  map[Trigger]bool
	}{
		{"", map[Trigger]bool{TriggerGoal: false, TriggerPlan: false, TriggerSession: false}},
		{"off", map[Trigger]bool{TriggerGoal: false, TriggerPlan: false, TriggerSession: false}},
		{"goal", map[Trigger]bool{TriggerGoal: true, TriggerPlan: false, TriggerSession: false}},
		{"goal_plan", map[Trigger]bool{TriggerGoal: true, TriggerPlan: true, TriggerSession: false}},
		{"session", map[Trigger]bool{TriggerGoal: true, TriggerPlan: true, TriggerSession: true}},
		{"bogus", map[Trigger]bool{TriggerGoal: false, TriggerPlan: false, TriggerSession: false}},
	}
	for _, c := range cases {
		s := config.Settings{Tests: &config.TestsSettings{PostEnd: c.level}}
		for trig, want := range c.want {
			if got := Should(s, trig); got != want {
				t.Errorf("level %q trigger %s = %v, want %v", c.level, trig, got, want)
			}
		}
	}
	if Should(config.Settings{}, TriggerGoal) {
		t.Error("nil tests block must be off")
	}
}

func TestRunOffSkips(t *testing.T) {
	out := Run(context.Background(), Config{Settings: settings("off", "")}, TriggerGoal)
	if out.Skipped == "" || len(out.Results) != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Summary() != nil {
		t.Fatal("a skipped pass must leave no repo-map summary")
	}
}

func TestRunNoSuiteDetectedSkips(t *testing.T) {
	out := Run(context.Background(), Config{Settings: settings("goal", ""), Dir: t.TempDir()}, TriggerGoal)
	if out.Skipped != "no test suite detected" {
		t.Fatalf("skipped = %q", out.Skipped)
	}
}

func TestRunPassUsesHarnessReportWithoutClassifier(t *testing.T) {
	cfg := Config{Settings: settings("goal", "fix", "sh", "-c", "exit 0"), Dir: t.TempDir()}
	out := Run(context.Background(), cfg, TriggerGoal)
	if !out.Passed || out.ReportByModel || !strings.HasPrefix(out.Report, "Tests passed:") {
		t.Fatalf("outcome = %+v", out)
	}
	if s := out.Summary(); s == nil || !s.Passed || s.Suites != 1 {
		t.Fatalf("summary = %+v", s)
	}
	if out.Line() == "" || !strings.HasPrefix(out.Line(), "tests pass") {
		t.Fatalf("line = %q", out.Line())
	}
}

func TestRunPassAsksFastModelAndSendsGatedDigestOnly(t *testing.T) {
	var got rolemanager.ClassifierPayload
	c := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		got = p
		return "All good.", nil
	})
	gated := 0
	cfg := Config{
		Settings: settings("goal", "", "sh", "-c", "echo secret-output"),
		Dir:      t.TempDir(),
		Report:   c,
		Gate: func(_ context.Context, output string) (string, bool) {
			gated++
			return "gated:" + strings.TrimSpace(output), true
		},
	}
	out := Run(context.Background(), cfg, TriggerPlan)
	if !out.ReportByModel || out.Report != "All good." {
		t.Fatalf("outcome = %+v", out)
	}
	if gated != 1 || !strings.Contains(got.User, "gated:") {
		t.Fatalf("digest must ride the gate: gated=%d user=%q", gated, got.User)
	}
}

func TestRunPassWithheldOutputNeverReachesReportRole(t *testing.T) {
	var got rolemanager.ClassifierPayload
	c := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		got = p
		return "ok", nil
	})
	cfg := Config{
		Settings: settings("goal", "", "sh", "-c", "echo hostile-output"),
		Dir:      t.TempDir(),
		Report:   c,
		Gate:     func(context.Context, string) (string, bool) { return "", false },
	}
	Run(context.Background(), cfg, TriggerGoal)
	if strings.Contains(got.User, "hostile-output") || strings.Contains(got.User, "excerpt") {
		t.Fatalf("withheld output reached the role: %q", got.User)
	}
}

func TestRunNoGateMeansNoOutputToModels(t *testing.T) {
	var got rolemanager.ClassifierPayload
	c := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		got = p
		return "ok", nil
	})
	cfg := Config{Settings: settings("goal", "", "sh", "-c", "echo raw"), Dir: t.TempDir(), Report: c}
	Run(context.Background(), cfg, TriggerGoal)
	if strings.Contains(got.User, "raw") {
		t.Fatalf("ungated output reached the role: %q", got.User)
	}
}

func TestRunReportOffUsesHarnessLine(t *testing.T) {
	off := false
	s := settings("goal", "", "sh", "-c", "exit 0")
	s.Tests.Report = &off
	called := false
	c := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		called = true
		return "x", nil
	})
	out := Run(context.Background(), Config{Settings: s, Dir: t.TempDir(), Report: c}, TriggerGoal)
	if called || out.ReportByModel || out.Report == "" {
		t.Fatalf("report=off must not call the role: called=%v outcome=%+v", called, out)
	}
}

// The fail branch runs the fix loop with gated output and re-runs the suite;
// the loop's edit (creating the marker) turns the pass green.
func TestRunFailThenFixThenPass(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ok")
	var reqs []FixRequest
	cfg := Config{
		Settings: settings("goal", "fix", "sh", "-c", "echo failing-detail; test -f ok"),
		Dir:      dir,
		Gate:     func(_ context.Context, o string) (string, bool) { return "G:" + strings.TrimSpace(o), true },
		Fix: func(_ context.Context, r FixRequest) error {
			reqs = append(reqs, r)
			return os.WriteFile(marker, nil, 0o644)
		},
	}
	out := Run(context.Background(), cfg, TriggerGoal)
	if !out.Passed || out.FixPasses != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	if len(reqs) != 1 || reqs[0].Mode != "fix" || reqs[0].Pass != 1 || reqs[0].MaxPasses != config.DefaultTestsMaxFixPasses {
		t.Fatalf("requests = %+v", reqs)
	}
	if !strings.Contains(reqs[0].Output, "G:") || !strings.Contains(reqs[0].Output, "failing-detail") || reqs[0].OutputWithheld {
		t.Fatalf("output = %q", reqs[0].Output)
	}
	if !strings.Contains(reqs[0].Facts, "fail") || len(reqs[0].Commands) != 1 {
		t.Fatalf("facts = %q commands = %v", reqs[0].Facts, reqs[0].Commands)
	}
	if !strings.Contains(out.Line(), "after 1 fix pass") {
		t.Fatalf("line = %q", out.Line())
	}
}

func TestRunFixBudgetIsBounded(t *testing.T) {
	calls := 0
	s := settings("goal", "fix", "sh", "-c", "exit 1")
	s.Tests.MaxFixPasses = 2
	cfg := Config{Settings: s, Dir: t.TempDir(), Fix: func(context.Context, FixRequest) error { calls++; return nil }}
	out := Run(context.Background(), cfg, TriggerGoal)
	if out.Passed || calls != 2 || out.FixPasses != 2 || out.Report != "" {
		t.Fatalf("calls=%d outcome=%+v", calls, out)
	}
	if !strings.HasPrefix(out.Line(), "tests fail") {
		t.Fatalf("line = %q", out.Line())
	}
}

func TestRunDiagnoseIsOneReadOnlyPassWithNoRerun(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "runs")
	calls := 0
	cfg := Config{
		Settings: settings("goal", "diagnose", "sh", "-c", "echo x >> runs; exit 1"),
		Dir:      dir,
		Fix:      func(_ context.Context, r FixRequest) error { calls++; return nil },
	}
	out := Run(context.Background(), cfg, TriggerGoal)
	data, _ := os.ReadFile(count)
	if calls != 1 || strings.Count(string(data), "x") != 1 || out.Passed {
		t.Fatalf("calls=%d runs=%q outcome=%+v", calls, data, out)
	}
}

func TestRunOnFailOffNeverCallsLoop(t *testing.T) {
	called := false
	cfg := Config{
		Settings: settings("goal", "off", "sh", "-c", "exit 1"),
		Dir:      t.TempDir(),
		Fix:      func(context.Context, FixRequest) error { called = true; return nil },
	}
	if out := Run(context.Background(), cfg, TriggerGoal); called || out.Passed || out.FixPasses != 0 {
		t.Fatalf("called=%v outcome=%+v", called, out)
	}
}

func TestRunNilFixerNeverLoops(t *testing.T) {
	out := Run(context.Background(), Config{Settings: settings("goal", "fix", "sh", "-c", "exit 1"), Dir: t.TempDir()}, TriggerGoal)
	if out.Passed || out.FixPasses != 0 {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunFixErrorStopsTheBranch(t *testing.T) {
	calls := 0
	cfg := Config{
		Settings: settings("goal", "fix", "sh", "-c", "exit 1"),
		Dir:      t.TempDir(),
		Fix:      func(context.Context, FixRequest) error { calls++; return errors.New("boom") },
	}
	if out := Run(context.Background(), cfg, TriggerGoal); calls != 1 || out.Passed {
		t.Fatalf("calls=%d outcome=%+v", calls, out)
	}
}

func TestRunWithheldFailureOutputIsFlagged(t *testing.T) {
	var got FixRequest
	cfg := Config{
		Settings: settings("goal", "diagnose", "sh", "-c", "printf 'hos%s' tile; exit 1"),
		Dir:      t.TempDir(),
		Gate:     func(context.Context, string) (string, bool) { return "", false },
		Fix:      func(_ context.Context, r FixRequest) error { got = r; return nil },
	}
	Run(context.Background(), cfg, TriggerGoal)
	if got.Output != "" || !got.OutputWithheld || strings.Contains(got.Facts, "hostile") {
		t.Fatalf("request = %+v", got)
	}
}

func TestRunNilGateWithholdsFailureOutput(t *testing.T) {
	var got FixRequest
	cfg := Config{
		Settings: settings("goal", "diagnose", "sh", "-c", "printf 'hos%s' tile; exit 1"),
		Dir:      t.TempDir(),
		Fix:      func(_ context.Context, r FixRequest) error { got = r; return nil },
	}
	Run(context.Background(), cfg, TriggerGoal)
	if got.Output != "" || !got.OutputWithheld {
		t.Fatalf("request = %+v", got)
	}
}

// A command that never ran (denied) leaves nothing to fix, so the loop is not
// entered, and the harness never pretends it passed.
func TestRunDeniedCommandSkipsLoopAndFails(t *testing.T) {
	called := false
	cfg := Config{
		Settings: settings("goal", "fix", "sh", "-c", "exit 0"),
		Dir:      t.TempDir(),
		Perms:    permissions.From(nil, nil, []string{"Bash(sh*)"}),
		Fix:      func(context.Context, FixRequest) error { called = true; return nil },
	}
	out := Run(context.Background(), cfg, TriggerGoal)
	if called || out.Passed || len(out.Results) != 1 || out.Results[0].Status != "denied" {
		t.Fatalf("called=%v outcome=%+v", called, out)
	}
}

func TestRunDetectedSuiteScopesToChangedPaths(t *testing.T) {
	notes := []string{}
	cfg := Config{
		Settings: config.Settings{Tests: &config.TestsSettings{PostEnd: "goal"}},
		Dir:      t.TempDir(),
		Suites:   []testdetect.Suite{{Name: "go", Ecosystem: "go", Command: []string{"belai-no-such-go", "test", "./..."}}},
		Changed:  func() []string { return []string{"internal/x/a.go"} },
		Notify:   func(s string) { notes = append(notes, s) },
	}
	out := Run(context.Background(), cfg, TriggerGoal)
	if len(out.Results) != 1 || out.Results[0].Command[len(out.Results[0].Command)-1] != "./internal/x/..." {
		t.Fatalf("results = %+v", out.Results)
	}
	if len(notes) == 0 || !strings.HasPrefix(notes[0], "running tests") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestRunCancelledContextStopsTheLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	cfg := Config{
		Settings: settings("goal", "fix", "sh", "-c", "exit 1"),
		Dir:      t.TempDir(),
		Fix:      func(context.Context, FixRequest) error { calls++; cancel(); return nil },
	}
	Run(ctx, cfg, TriggerGoal)
	if calls > 1 {
		t.Fatalf("loop ran %d times after cancel", calls)
	}
}
