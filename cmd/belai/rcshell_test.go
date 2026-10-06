package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rc"
	"github.com/vulnetix/belai/internal/run"
)

func testShellRunner(t *testing.T, settings config.Settings) (rc.ShellRunner, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Guardrails ignored: no classifier call, no OS sandbox, so the test needs
	// neither a model nor bwrap.
	eff := func() (config.Settings, posture.Policy) { return settings, posture.AllIgnore() }
	return rcShellRunner(root, eff, run.Config{}, nil, nil), root
}

func TestRCShellRunsInTheSessionDirectory(t *testing.T) {
	runner, root := testShellRunner(t, config.Settings{})
	out := runner(context.Background(), rc.ShellRequest{Command: "pwd"})
	if out.Refused != "" || out.Err != nil {
		t.Fatalf("outcome = %+v", out)
	}
	if strings.TrimSpace(out.Output) != root || out.Cwd != root || !out.Safe || out.ExitCode != 0 {
		t.Fatalf("outcome = %+v, want pwd %s", out, root)
	}
}

func TestRCShellRunsInTheRequestedSubdirectory(t *testing.T) {
	runner, root := testShellRunner(t, config.Settings{})
	sub := filepath.Join(root, "sub")
	out := runner(context.Background(), rc.ShellRequest{Command: "pwd", Cwd: sub})
	if strings.TrimSpace(out.Output) != sub || out.Cwd != sub {
		t.Fatalf("outcome = %+v, want %s", out, sub)
	}
}

func TestRCShellReportsExitCode(t *testing.T) {
	runner, _ := testShellRunner(t, config.Settings{})
	out := runner(context.Background(), rc.ShellRequest{Command: "echo out; exit 3"})
	if out.ExitCode != 3 || !strings.Contains(out.Output, "out") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRCShellRefusesADirectoryOutsideTheWorkspace(t *testing.T) {
	runner, root := testShellRunner(t, config.Settings{})
	for _, cwd := range []string{"/etc", filepath.Dir(root), filepath.Join(root, "missing")} {
		out := runner(context.Background(), rc.ShellRequest{Command: "pwd", Cwd: cwd})
		if out.Refused == "" {
			t.Errorf("cwd %q ran: %+v", cwd, out)
		}
	}
	// A symlink out of the workspace is not a way out.
	link := filepath.Join(root, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	if out := runner(context.Background(), rc.ShellRequest{Command: "pwd", Cwd: link}); out.Refused == "" {
		t.Errorf("symlinked cwd ran: %+v", out)
	}
}

func TestRCShellCdResolvesWithoutAProcess(t *testing.T) {
	runner, root := testShellRunner(t, config.Settings{})
	sub := filepath.Join(root, "sub")
	cases := []struct {
		cwd, line, want string
		failed          bool
	}{
		{root, "cd sub", sub, false},
		{sub, "cd deep", filepath.Join(sub, "deep"), false},
		{filepath.Join(sub, "deep"), "cd ..", sub, false},
		{sub, "cd", root, false},
		{sub, "cd ~", root, false},
		{sub, "cd " + root, root, false},
		{root, "cd ..", root, true},
		{root, "cd /etc", root, true},
		{root, "cd nowhere", root, true},
	}
	for _, c := range cases {
		out := runner(context.Background(), rc.ShellRequest{Command: c.line, Cwd: c.cwd})
		if out.Refused != "" || out.Cwd != c.want || (out.ExitCode != 0) != c.failed {
			t.Errorf("%q from %s = %+v, want cwd %s failed=%v", c.line, c.cwd, out, c.want, c.failed)
		}
	}
	// Anything with shell syntax is a command, not a move.
	if _, ok := rcShellCd("cd sub && ls"); ok {
		t.Error("`cd sub && ls` treated as a bare cd")
	}
}

func TestRCShellPlanModeRefusesAWrite(t *testing.T) {
	runner, _ := testShellRunner(t, config.Settings{})
	out := runner(context.Background(), rc.ShellRequest{Command: "touch x", PlanMode: true})
	if !strings.Contains(out.Refused, "plan mode") {
		t.Fatalf("outcome = %+v, want a plan mode refusal", out)
	}
}

func TestRCShellPermissionRules(t *testing.T) {
	var s config.Settings
	// Harmless commands only: a rule that fails to match would run the line.
	s.Permissions.Deny = []string{"Bash(echo denied*)"}
	s.Permissions.Ask = []string{"Bash(echo asked*)"}
	runner, _ := testShellRunner(t, s)
	if out := runner(context.Background(), rc.ShellRequest{Command: "echo denied"}); !strings.Contains(out.Refused, "blocked") {
		t.Errorf("deny rule: %+v", out)
	}
	if out := runner(context.Background(), rc.ShellRequest{Command: "echo asked"}); !strings.Contains(out.Refused, "nobody can answer") {
		t.Errorf("ask rule: %+v", out)
	}
	if out := runner(context.Background(), rc.ShellRequest{Command: "echo fine"}); out.Refused != "" || !strings.Contains(out.Output, "fine") {
		t.Errorf("no rule: %+v", out)
	}
}
