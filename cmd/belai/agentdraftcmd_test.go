package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
)

const draftCmdReply = `{"fields": {
  "name": {"value": "dep-reviewer", "why": "from the premise"},
  "description": {"value": "Reviews dependency changes"},
  "system_prompt": {"value": "Review the manifests on the branch."},
  "mode": {"value": "worker"},
  "autonomy": {"value": "supervised"},
  "tools": {"value": ["Read", "Grep", "Glob"]},
  "kanban.lists": {"value": ["review"]},
  "kanban.labels": {"value": ["deps-review"]},
  "kanban.on_success": {"value": {"list": "done"}},
  "kanban.on_failure": {"value": {"list": "backlog", "labels": ["build", "docs"]}}
}}`

// draftCmdSetup isolates Belai's state, trusts a scratch repository and
// replaces the classifier with a canned reply.
func draftCmdSetup(t *testing.T, reply string) *[]rolemanager.ClassifierPayload {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	t.Chdir(t.TempDir())
	var seen []rolemanager.ClassifierPayload
	old := draftClassifier
	draftClassifier = func(string, string, string) (rolemanager.Classifier, config.Settings, error) {
		return rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
			seen = append(seen, p)
			return reply, nil
		}), config.Settings{}, nil
	}
	t.Cleanup(func() { draftClassifier = old })
	return &seen
}

func TestAgentDraftWritesImportableMarkdown(t *testing.T) {
	seen := draftCmdSetup(t, draftCmdReply)
	var out, errb bytes.Buffer
	code := runAgentCLI(context.Background(), []string{"draft", "-trust-dir", "a", "dependency", "reviewer"}, nil, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if len(*seen) != 1 || !strings.Contains((*seen)[0].User, "a dependency reviewer") {
		t.Fatalf("payloads: %+v", *seen)
	}
	p, err := agentprofile.ParseMarkdown(out.Bytes())
	if err != nil {
		t.Fatalf("not importable: %v\n%s", err, out.String())
	}
	if p.Name != "dep-reviewer" || p.Kanban == nil || p.Kanban.Labels[0] != "deps-review" {
		t.Fatalf("profile: %+v", p)
	}
	// The drafted failure route sends docs, which nothing claims.
	if !strings.Contains(errb.String(), "crew offer: Nothing claims docs") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

func TestAgentDraftToFileAndJSON(t *testing.T) {
	draftCmdSetup(t, draftCmdReply)
	var out, errb bytes.Buffer
	file := filepath.Join(t.TempDir(), "dep.md")
	if code := runAgentCLI(context.Background(), []string{"draft", "-trust-dir", "-o", file, "reviewer"}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(data), "---\nname: \"dep-reviewer\"") || out.Len() != 0 {
		t.Fatalf("file: %q %v; stdout %q", data, err, out.String())
	}
	if !strings.Contains(errb.String(), "belai agent import "+file) {
		t.Fatalf("stderr: %s", errb.String())
	}

	out.Reset()
	if code := runAgentCLI(context.Background(), []string{"draft", "-json", "-trust-dir", "reviewer"}, nil, &out, &errb); code != 0 {
		t.Fatalf("json exit %d", code)
	}
	if !strings.Contains(out.String(), `"why": "from the premise"`) || !strings.Contains(out.String(), `"crews"`) {
		t.Fatalf("json: %s", out.String())
	}
}

func TestAgentDraftWarnsWhenBelaiWouldRefuse(t *testing.T) {
	// An autonomous worker with no pass budget drafts, but import would refuse it.
	draftCmdSetup(t, strings.Replace(draftCmdReply, `"supervised"`, `"autonomous"`, 1))
	var out, errb bytes.Buffer
	if code := runAgentCLI(context.Background(), []string{"draft", "-trust-dir", "x"}, nil, &out, &errb); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errb.String(), "Belai would refuse it as drafted") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

func TestAgentDraftFailsClosed(t *testing.T) {
	draftCmdSetup(t, draftCmdReply)
	var out, errb bytes.Buffer
	if code := runAgentCLI(context.Background(), []string{"draft", "-trust-dir"}, nil, &out, &errb); code != 2 {
		t.Fatalf("no premise: exit %d", code)
	}
	// An untrusted repository is refused before any model call.
	seen := draftCmdSetup(t, draftCmdReply)
	errb.Reset()
	if code := runAgentCLI(context.Background(), []string{"draft", "x"}, nil, &out, &errb); code != 1 || len(*seen) != 0 ||
		!strings.Contains(errb.String(), "not a trusted workspace") {
		t.Fatalf("untrusted: exit %d, %d calls, %s", code, len(*seen), errb.String())
	}
	seen = draftCmdSetup(t, "not json")
	if code := runAgentCLI(context.Background(), []string{"draft", "-trust-dir", "x"}, nil, &out, &errb); code != 1 || len(*seen) != 2 {
		t.Fatalf("unusable reply: exit %d after %d calls", code, len(*seen))
	}
}
