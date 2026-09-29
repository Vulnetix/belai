package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/sandbox"
)

type fakeLauncher struct {
	started []string
	dir     string
	policy  sandbox.Policy
	out     string
	filter  string
	err     error
	list    []BackgroundInfo
}

func (f *fakeLauncher) StartBackground(cmd, dir string, p sandbox.Policy) (BackgroundInfo, error) {
	if f.err != nil {
		return BackgroundInfo{}, f.err
	}
	f.started = append(f.started, cmd)
	f.dir, f.policy = dir, p
	return BackgroundInfo{ID: "p7", State: "running"}, nil
}

func (f *fakeLauncher) ReadBackground(id, filter string, max int) (string, BackgroundInfo, error) {
	f.filter = filter
	if f.err != nil {
		return "", BackgroundInfo{}, f.err
	}
	return f.out, BackgroundInfo{ID: id, State: "exited", ExitCode: 2}, nil
}

func (f *fakeLauncher) StopBackground(id string) (BackgroundInfo, error) {
	if f.err != nil {
		return BackgroundInfo{}, f.err
	}
	return BackgroundInfo{ID: id, State: "stopped"}, nil
}

func (f *fakeLauncher) ListBackground() []BackgroundInfo { return f.list }

func TestBashRunInBackgroundUsesCallPolicyAndDir(t *testing.T) {
	l := &fakeLauncher{}
	b := &Bash{Root: t.TempDir(), Launcher: l}
	pol := sandbox.Policy{Mode: sandbox.ModeRequired}
	ctx := sandbox.WithPolicy(context.Background(), pol)
	res, err := b.Execute(ctx, map[string]any{"command": "python3 -m http.server", "run_in_background": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.started) != 1 || l.started[0] != "python3 -m http.server" {
		t.Fatalf("started = %v", l.started)
	}
	if l.policy.Mode != sandbox.ModeRequired {
		t.Fatalf("policy not carried from the call context: %+v", l.policy)
	}
	if l.dir != b.Root {
		t.Fatalf("dir = %q", l.dir)
	}
	if res.Kind != KindBash || !strings.Contains(res.Content, "p7") {
		t.Fatalf("result = %+v", res)
	}
}

func TestBashRunInBackgroundRefusals(t *testing.T) {
	ro := &Bash{Root: t.TempDir(), ReadOnly: true, Launcher: &fakeLauncher{}}
	if _, err := ro.Execute(context.Background(), map[string]any{"command": "ls", "run_in_background": true}); err == nil {
		t.Fatal("read-only Bash accepted run_in_background")
	}
	none := &Bash{Root: t.TempDir()}
	if _, err := none.Execute(context.Background(), map[string]any{"command": "sleep 1", "run_in_background": true}); err == nil {
		t.Fatal("no launcher must refuse, not fall back to a foreground run")
	}
	vx := &Bash{Root: t.TempDir(), Vulnetix: true, Launcher: &fakeLauncher{}}
	if _, err := vx.Execute(context.Background(), map[string]any{"command": "vulnetix scan", "run_in_background": true}); err == nil {
		t.Fatal("the vulnetix redirect must apply to a background launch too")
	}
}

func TestBashDefinitionAdvertisesBackgroundOnlyWhenAvailable(t *testing.T) {
	if _, ok := (&Bash{}).Definition().Properties["run_in_background"]; ok {
		t.Fatal("advertised without a launcher")
	}
	if _, ok := (&Bash{ReadOnly: true, Launcher: &fakeLauncher{}}).Definition().Properties["run_in_background"]; ok {
		t.Fatal("advertised on read-only Bash")
	}
	if _, ok := (&Bash{Launcher: &fakeLauncher{}}).Definition().Properties["run_in_background"]; !ok {
		t.Fatal("not advertised with a launcher")
	}
}

func TestBashOutput(t *testing.T) {
	l := &fakeLauncher{out: "listening on :8000\n"}
	tool := &BashOutput{Launcher: l}
	if tool.Kind() != KindProcess || tool.Mutates() {
		t.Fatal("BashOutput is a read-only, classified process kind")
	}
	res, err := tool.Execute(context.Background(), map[string]any{"bash_id": "p7", "filter": "listen"})
	if err != nil {
		t.Fatal(err)
	}
	if l.filter != "listen" || !strings.Contains(res.Content, "p7 exited (exit status 2)") || !strings.Contains(res.Content, "listening") {
		t.Fatalf("result = %q filter = %q", res.Content, l.filter)
	}
	l.out = ""
	res, _ = tool.Execute(context.Background(), map[string]any{"bash_id": "p7"})
	if !strings.Contains(res.Content, "(no new output)") {
		t.Fatalf("empty read = %q", res.Content)
	}
	if tool.Subject(map[string]any{"bash_id": "p7"}) != "p7" {
		t.Fatal("subject is the handle")
	}
}

func TestBashOutputArgumentChecks(t *testing.T) {
	tool := &BashOutput{Launcher: &fakeLauncher{}}
	for name, args := range map[string]map[string]any{
		"no id":      {},
		"bad regex":  {"bash_id": "p1", "filter": "("},
		"long regex": {"bash_id": "p1", "filter": strings.Repeat("a", maxFilterBytes+1)},
	} {
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := (&BashOutput{}).Execute(context.Background(), map[string]any{"bash_id": "p1"}); err == nil {
		t.Error("nil launcher accepted")
	}
	l := &fakeLauncher{err: fmt.Errorf("background process %q not found", "p9")}
	if _, err := (&BashOutput{Launcher: l}).Execute(context.Background(), map[string]any{"bash_id": "p9"}); err == nil {
		t.Error("launcher error swallowed")
	}
}

func TestKillShell(t *testing.T) {
	k := &KillShell{Launcher: &fakeLauncher{}}
	if k.Kind() != KindProcessCtl || k.Mutates() {
		t.Fatal("KillShell is a harness-composed, non-asking kind")
	}
	res, err := k.Execute(context.Background(), map[string]any{"shell_id": "p7"})
	if err != nil || res.Content != "stopped: p7 stopped (exit status 0)" {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if _, err := k.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("missing shell_id accepted")
	}
}

func TestProcessList(t *testing.T) {
	l := &fakeLauncher{}
	p := &ProcessList{Launcher: l}
	res, _ := p.Execute(context.Background(), nil)
	if res.Content != "no background processes" || res.Kind != KindNative {
		t.Fatalf("empty = %+v", res)
	}
	l.list = []BackgroundInfo{{ID: "p1", State: "running"}, {ID: "p2", State: "exited", ExitCode: 1}}
	res, _ = p.Execute(context.Background(), nil)
	if res.Content != "p1 running\np2 exited (exit status 1)" {
		t.Fatalf("list = %q", res.Content)
	}
}

func TestBackgroundToolsAreCore(t *testing.T) {
	for _, n := range []string{BashOutputName, KillShellName} {
		if !IsCoreTool(n) {
			t.Errorf("%s should be advertised in full", n)
		}
	}
}
