package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/checkpoint"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/gittest"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// ckptRun runs one turn whose model calls toolName once. The returned tracker
// is the one the session saved into.
func ckptRun(t *testing.T, root, toolName, args string, tr *checkpoint.Tracker, o Options) hazardRun {
	t.Helper()
	o.Checkpoints = tr
	if o.Perms.Allow == nil {
		o.Perms = permissions.From([]string{"Write", "Edit", "Bash"}, nil, nil)
	}
	if o.Settings.Sandbox == nil {
		o.Settings.Sandbox = &config.SandboxSettings{Mode: "off"}
	}
	o.AllowAsk = true
	if o.Registry == nil {
		o.Registry = tools.NewRegistry(&tools.Write{Root: root}, &tools.Edit{Root: root}, &tools.Bash{Root: root}, &tools.Read{Root: root})
	}
	return runHazard(t, root, toolName, args, o, true)
}

func openTracker(t *testing.T, root string) *checkpoint.Tracker {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	dir, err := config.CheckpointsDir(root, "sess")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := checkpoint.Open(dir, checkpoint.Limits{MaxPerSession: 50, MaxBytes: 1 << 30}, checkpoint.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	tr.BeginTurn("entry", "prompt")
	return tr
}

func resolvedTemp(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func jsonArgs(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// restoreAll restores the latest checkpoint. The fixtures' registries carry no
// working-directory tracker, so the session's own policy refuses every path as
// outside its roots; the test supplies the confinement a real registry gives.
func restoreAll(t *testing.T, tr *checkpoint.Tracker, root string) checkpoint.Result {
	t.Helper()
	tr.SetPolicy(checkpoint.PolicyFor(root, func(abs string) bool {
		return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
	}, permissions.Settings{}))
	info, ok := tr.Latest()
	if !ok {
		t.Fatal("no checkpoint was made")
	}
	plan, err := tr.Plan(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tr.Apply(plan)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestWriteIsCheckpointedBeforeItRuns(t *testing.T) {
	root := resolvedTemp(t)
	target := filepath.Join(root, "a.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := openTracker(t, root)
	r := ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "a.txt", "content": "after\n"}), tr, Options{})
	if b, _ := os.ReadFile(target); string(b) != "after\n" {
		t.Fatalf("the write did not happen: %q (%s)", b, r.result)
	}
	if res := restoreAll(t, tr, root); res.Restored != 1 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(target); string(b) != "before\n" {
		t.Fatalf("a.txt = %q after restore", b)
	}
}

func TestEditAndAFileTheCallCreatesAreCheckpointed(t *testing.T) {
	root := resolvedTemp(t)
	if err := os.WriteFile(filepath.Join(root, "e.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := openTracker(t, root)
	ckptRun(t, root, "Edit", jsonArgs(t, map[string]any{"file_path": "e.txt", "old_string": "world", "new_string": "there"}), tr, Options{})
	ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "made.txt", "content": "new\n"}), tr, Options{})
	if b, _ := os.ReadFile(filepath.Join(root, "e.txt")); string(b) != "hello there\n" {
		t.Fatalf("the edit did not happen: %q", b)
	}
	res := restoreAll(t, tr, root)
	if res.Deleted != 1 || res.Restored != 1 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "e.txt")); string(b) != "hello world\n" {
		t.Fatalf("e.txt = %q after restore", b)
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); err == nil {
		t.Fatal("the created file survived the restore")
	}
}

func TestACheckpointThatCannotBeKeptWithholdsTheWrite(t *testing.T) {
	root := resolvedTemp(t)
	tr := checkpoint.Unavailable(os.ErrPermission)
	r := ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "x.txt", "content": "x"}), tr, Options{})
	if !strings.Contains(r.result, "checkpoint unavailable") {
		t.Fatalf("result = %q", r.result)
	}
	if exists(root, "x.txt") {
		t.Fatal("the write ran although nothing could restore it")
	}
}

func TestACheckpointThatCannotBeKeptWithholdsBash(t *testing.T) {
	root := resolvedTemp(t)
	tr := checkpoint.Unavailable(os.ErrPermission)
	r := ckptRun(t, root, "Bash", jsonArgs(t, map[string]any{"command": "touch ran.txt"}), tr, Options{})
	if !strings.Contains(r.result, "checkpoint unavailable") || exists(root, "ran.txt") {
		t.Fatalf("result = %q, ran = %v", r.result, exists(root, "ran.txt"))
	}
}

func TestDisabledCheckpointsKeepNothingAndWithholdNothing(t *testing.T) {
	root := resolvedTemp(t)
	tr := openTracker(t, root)
	no := false
	o := Options{Settings: config.Settings{Checkpoints: &config.CheckpointSettings{Enabled: &no}}}
	ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "a.txt", "content": "x"}), tr, o)
	if !exists(root, "a.txt") {
		t.Fatal("the write was withheld")
	}
	if len(tr.List()) != 0 {
		t.Fatal("a disabled session kept a checkpoint")
	}
	// An unavailable store is ignored the same way.
	r := ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "b.txt", "content": "x"}), checkpoint.Unavailable(os.ErrPermission), o)
	if strings.Contains(r.result, "checkpoint unavailable") || !exists(root, "b.txt") {
		t.Fatalf("result = %q", r.result)
	}
}

func TestNoTrackerMeansNoCheckpoints(t *testing.T) {
	root := resolvedTemp(t)
	r := ckptRun(t, root, "Write", jsonArgs(t, map[string]any{"file_path": "a.txt", "content": "x"}), nil, Options{})
	if strings.Contains(r.result, "checkpoint") || !exists(root, "a.txt") {
		t.Fatalf("result = %q", r.result)
	}
}

func TestReadOnlyToolsSaveNothing(t *testing.T) {
	root := resolvedTemp(t)
	if err := os.WriteFile(filepath.Join(root, "r.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := openTracker(t, root)
	ckptRun(t, root, "Read", jsonArgs(t, map[string]any{"file_path": "r.txt"}), tr, Options{})
	if len(tr.List()) != 0 {
		t.Fatalf("a read made a checkpoint: %+v", tr.List())
	}
	// A read-only Bash is not a mutation either.
	ro := tools.NewRegistry(&tools.Bash{Root: root, ReadOnly: true})
	ckptRun(t, root, "Bash", jsonArgs(t, map[string]any{"command": "ls"}), tr, Options{Registry: ro})
	if len(tr.List()) != 0 {
		t.Fatalf("a read-only Bash made a checkpoint: %+v", tr.List())
	}
}

func TestBashChangesInARepositoryAreRestoredFromTheIndex(t *testing.T) {
	gittest.Isolate(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := resolvedTemp(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = gittest.Env("GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "--no-gpg-sign", "-qm", "initial")

	tr := openTracker(t, root)
	r := ckptRun(t, root, "Bash", jsonArgs(t, map[string]any{"command": "echo rewritten > tracked.txt && echo new > created.txt"}), tr, Options{})
	if b, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(b) != "rewritten\n" {
		t.Fatalf("the command did not run: %q (%s)", b, r.result)
	}
	res := restoreAll(t, tr, root)
	if res.Restored != 1 || res.Deleted != 1 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "tracked.txt")); string(b) != "committed\n" {
		t.Fatalf("tracked.txt = %q", b)
	}
	if exists(root, "created.txt") {
		t.Fatal("the file the command created survived the restore")
	}
}

func TestNestedWriteFromACodeScriptIsCheckpointed(t *testing.T) {
	root := resolvedTemp(t)
	target := filepath.Join(root, "n.txt")
	if err := os.WriteFile(target, []byte("orig\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := openTracker(t, root)
	script := `tools.Read({file_path: "n.txt"}); tools.Write({file_path: "n.txt", content: "changed by script\n"});`
	srv := mockSecurityServer("Code", jsonArgs(t, map[string]any{"code": script}), "done")
	defer srv.Close()
	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.Default(root, false),
		Posture:       posture.Defaults(),
		Perms:         permissions.From([]string{"Write"}, nil, nil),
		Settings:      config.Settings{Sandbox: &config.SandboxSettings{Mode: "off"}},
		Workdir:       root,
		AllowAsk:      true,
		SkipNonceSeed: true,
		Checkpoints:   tr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "go", ForceMode: modes.ModeCode}, false, func(e Event) {
		if e.Kind == EventPermissionAskKind {
			e.AskReply <- PermissionAskReply{Allow: true}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "changed by script\n" {
		t.Fatalf("the script did not write: %q", b)
	}
	// This registry has a real working-directory tracker, so the session's own
	// policy is the one that restores.
	info, ok := tr.Latest()
	if !ok {
		t.Fatal("a nested Write made no checkpoint")
	}
	plan, err := tr.Plan(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := tr.Apply(plan); err != nil || res.Restored != 1 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if b, _ := os.ReadFile(target); string(b) != "orig\n" {
		t.Fatalf("n.txt = %q after restore", b)
	}
}
