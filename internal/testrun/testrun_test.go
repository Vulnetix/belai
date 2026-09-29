package testrun

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/testdetect"
)

func TestRunPassAndFailByExitCode(t *testing.T) {
	opts := Options{Dir: t.TempDir()}
	pass := Run(context.Background(), opts, "ok", []string{"sh", "-c", "echo hi"})
	if pass.Status != Passed || !pass.OK() || pass.ExitCode != 0 {
		t.Fatalf("pass = %+v", pass)
	}
	if !strings.Contains(pass.Output, "hi") {
		t.Fatalf("output = %q", pass.Output)
	}
	fail := Run(context.Background(), opts, "bad", []string{"sh", "-c", "echo boom; exit 3"})
	if fail.Status != Failed || fail.OK() || fail.ExitCode != 3 {
		t.Fatalf("fail = %+v", fail)
	}
}

// The verdict is the exit code, never the text: a passing run that prints
// "FAIL" is a pass, and a failing run that prints "PASS" is a fail.
func TestRunVerdictIgnoresOutputText(t *testing.T) {
	opts := Options{Dir: t.TempDir()}
	if r := Run(context.Background(), opts, "a", []string{"sh", "-c", "echo FAIL"}); r.Status != Passed {
		t.Fatalf("status = %v", r.Status)
	}
	if r := Run(context.Background(), opts, "b", []string{"sh", "-c", "echo PASS; exit 1"}); r.Status != Failed {
		t.Fatalf("status = %v", r.Status)
	}
}

func TestRunTimeout(t *testing.T) {
	opts := Options{Dir: t.TempDir(), Timeout: 200 * time.Millisecond}
	r := Run(context.Background(), opts, "slow", []string{"sh", "-c", "sleep 5"})
	if r.Status != TimedOut || r.OK() {
		t.Fatalf("status = %v", r.Status)
	}
}

func TestRunMissingBinaryIsErrored(t *testing.T) {
	r := Run(context.Background(), Options{Dir: t.TempDir()}, "x", []string{"belai-no-such-binary-xyz"})
	if r.Status != Errored || !strings.Contains(r.Note, "not found") {
		t.Fatalf("result = %+v", r)
	}
}

func TestRunEmptyArgv(t *testing.T) {
	if r := Run(context.Background(), Options{}, "x", nil); r.Status != Errored {
		t.Fatalf("status = %v", r.Status)
	}
}

func TestRunDenyRuleRefuses(t *testing.T) {
	perms := permissions.From(nil, nil, []string{"Bash(go test*)"})
	r := Run(context.Background(), Options{Dir: t.TempDir(), Perms: perms}, "go", []string{"go", "test", "./..."})
	if r.Status != Denied {
		t.Fatalf("status = %v note=%q", r.Status, r.Note)
	}
	if r.Output != "" {
		t.Fatalf("a denied command must not run: %q", r.Output)
	}
}

func TestRunAskRuleSkips(t *testing.T) {
	perms := permissions.From(nil, []string{"Bash(echo*)"}, nil)
	r := Run(context.Background(), Options{Dir: t.TempDir(), Perms: perms}, "e", []string{"echo", "hi"})
	if r.Status != NeedsApproval {
		t.Fatalf("status = %v", r.Status)
	}
	if r.Output != "" {
		t.Fatalf("an ask-gated command must not run: %q", r.Output)
	}
}

func TestRunEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("BELAI_TESTRUN_SECRET_TOKEN", "hunter2")
	r := Run(context.Background(), Options{Dir: t.TempDir()}, "env", []string{"sh", "-c", "env"})
	if strings.Contains(r.Output, "hunter2") {
		t.Fatalf("scrubbed environment leaked a variable: %q", r.Output)
	}
}

func TestBuildPlanOverrideRunsAlone(t *testing.T) {
	suites := []testdetect.Suite{{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}}}
	p := BuildPlan(suites, []string{"make", "verify"}, "affected", []string{"a/b.go"})
	if !reflect.DeepEqual(p.Names, []string{"override"}) || !reflect.DeepEqual(p.Argvs, [][]string{{"make", "verify"}}) {
		t.Fatalf("plan = %+v", p)
	}
}

func TestBuildPlanScope(t *testing.T) {
	suites := []testdetect.Suite{
		{Name: "go", Ecosystem: "go", Command: []string{"go", "test", "./..."}, Scope: testdetect.ScopeFull},
		{Name: "cargo", Ecosystem: "rust", Command: []string{"cargo", "test"}, Scope: testdetect.ScopeFull},
	}
	changed := []string{"internal/x/y.go"}

	aff := BuildPlan(suites, nil, "affected", changed)
	if got := aff.Argvs[0]; !reflect.DeepEqual(got, []string{"go", "test", "./internal/x/..."}) {
		t.Fatalf("affected go argv = %v", got)
	}
	if got := aff.Argvs[1]; !reflect.DeepEqual(got, []string{"cargo", "test"}) {
		t.Fatalf("non-go suite must stay full: %v", got)
	}

	full := BuildPlan(suites, nil, "full", changed)
	if got := full.Argvs[0]; !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Fatalf("full go argv = %v", got)
	}

	// No Go changes means uncertainty: the whole suite.
	none := BuildPlan(suites, nil, "affected", []string{"README.md"})
	if got := none.Argvs[0]; !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Fatalf("unmapped change must run full: %v", got)
	}
}

func TestBuildPlanDoesNotAliasInput(t *testing.T) {
	override := []string{"a", "b"}
	p := BuildPlan(nil, override, "full", nil)
	p.Argvs[0][0] = "changed"
	if override[0] != "a" {
		t.Fatal("plan aliases the caller's override")
	}
}

func TestRunPlanAndAllPassed(t *testing.T) {
	opts := Options{Dir: t.TempDir()}
	plan := Plan{Names: []string{"a", "b"}, Argvs: [][]string{{"true"}, {"false"}}}
	rs := RunPlan(context.Background(), opts, plan)
	if len(rs) != 2 || AllPassed(rs) {
		t.Fatalf("results = %+v", rs)
	}
	if !AllPassed(rs[:1]) {
		t.Fatal("single pass should be all passed")
	}
	if AllPassed(nil) {
		t.Fatal("no results is not a pass")
	}
}

func TestRunPlanStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rs := RunPlan(ctx, Options{Dir: t.TempDir()}, Plan{Names: []string{"a"}, Argvs: [][]string{{"true"}}})
	if len(rs) != 0 {
		t.Fatalf("cancelled plan ran: %+v", rs)
	}
}
