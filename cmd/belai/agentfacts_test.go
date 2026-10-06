package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func TestAgentValidateWarnsAboutAFactTypo(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	dir := t.TempDir()
	file := filepath.Join(dir, "ops.md")
	md := "---\nname: ops\ndescription: d\nfacts:\n  aws_role_arns: arn:aws:iam::123456789012:role/ReadOnly\n  environment: prod\n---\nPrompt\n"
	if err := os.WriteFile(file, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := runAgentCLI(context.Background(), []string{"validate", file}, strings.NewReader(""), &out, &errb)
	if code != 0 {
		t.Fatalf("a typo in a fact key must not fail validation; exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "valid") {
		t.Errorf("stdout = %q", out.String())
	}
	if !strings.Contains(errb.String(), "warning") || !strings.Contains(errb.String(), `"aws_role_arn"`) {
		t.Errorf("stderr should name the likely key, got %q", errb.String())
	}

	bad := filepath.Join(dir, "bad.md")
	if err := os.WriteFile(bad, []byte("---\nname: bad\ndescription: d\nfacts:\n  db_password: x\n---\nPrompt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errb.Reset()
	if code := runAgentCLI(context.Background(), []string{"validate", bad}, strings.NewReader(""), &out, &errb); code == 0 {
		t.Fatal("a secret-named fact must fail validation")
	}
}

// A crew started in a folder that is not a git repository is refused before
// any worker starts, so no item is claimed or filed there.
func TestAgentStartAndRunRefuseAWorktreeProfileOutsideGit(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	p := agentprofile.AgentProfile{
		Name: "wt-worker", Description: "d", SystemPrompt: "sp", Mode: agentprofile.ModeWorker,
		Tools: []string{"Read"},
		Kanban: &agentprofile.KanbanSpec{
			Labels:    []string{"wt"},
			OnSuccess: agentprofile.Route{List: "done"},
		},
		Workspace: &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree},
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatal(err)
	}
	plain := t.TempDir()
	t.Chdir(plain)

	for _, args := range [][]string{
		{"start", "-trust-dir", "wt-worker"},
		{"run", "-once", "-trust-dir", "wt-worker"},
	} {
		var out, errb bytes.Buffer
		code := runAgentCLI(context.Background(), args, strings.NewReader(""), &out, &errb)
		if code == 0 {
			t.Fatalf("%v: exit 0, want a refusal", args)
		}
		if !strings.Contains(errb.String(), "needs a git repository") || !strings.Contains(errb.String(), "wt-worker") {
			t.Errorf("%v: stderr %q", args, errb.String())
		}
		if strings.Contains(out.String(), "started") {
			t.Errorf("%v: a worker started: %q", args, out.String())
		}
	}
}
