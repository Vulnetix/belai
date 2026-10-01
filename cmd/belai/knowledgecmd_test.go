package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/knowledge"
)

func knowledgeRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vulnetix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vulnetix", "memory.yaml"), []byte("notes: the service stores sessions in postgres\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	return dir
}

func runKnowledge(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	code = runAgentCLI(context.Background(), append([]string{"knowledge"}, args...), strings.NewReader(""), &out, &errb)
	return out.String(), errb.String(), code
}

func TestAgentKnowledgeStatusListsAddressesAndNoText(t *testing.T) {
	dir := knowledgeRepo(t)
	out, _, code := runKnowledge(t)
	if code != 0 || !strings.Contains(out, "nothing is indexed yet") {
		t.Fatalf("empty status: %d %q", code, out)
	}
	// Index with no gate (sanitising alone), as a guardrails-off host would.
	s := knowledge.Open(knowledge.Options{Root: dir})
	if _, err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, _, code = runKnowledge(t)
	if code != 0 || !strings.Contains(out, "kb+project/.vulnetix/memory.yaml") || !strings.Contains(out, "project: ") {
		t.Fatalf("status: %d %q", code, out)
	}
	if strings.Contains(out, "postgres") {
		t.Fatalf("status must print counts and addresses, never chunk text: %q", out)
	}
	out, _, code = runKnowledge(t, "-json")
	var rep knowledgeReport
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || len(rep.Indexes) != 1 || rep.Indexes[0].Documents[0].Chunks != 1 {
		t.Fatalf("json: %d %q", code, out)
	}
	if rep.Indexes[0].Cap <= 0 || rep.Indexes[0].Tokens <= 0 {
		t.Fatalf("report = %+v", rep.Indexes[0])
	}
}

func TestAgentKnowledgeNeedsAProfileThatListsDocuments(t *testing.T) {
	knowledgeRepo(t)
	if _, err := agentprofile.Save(agentprofile.AgentProfile{Name: "plain", Description: "d", SystemPrompt: "s", Mode: agentprofile.ModeSingle}); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runKnowledge(t, "plain"); code == 0 || !strings.Contains(stderr, "lists no documents") {
		t.Fatalf("%d %q", code, stderr)
	}
	if _, stderr, code := runKnowledge(t, "missing"); code == 0 {
		t.Fatalf("an unknown profile must fail: %q", stderr)
	}
	if _, stderr, code := runKnowledge(t, "a", "b"); code != 2 || !strings.Contains(stderr, "usage") {
		t.Fatalf("two names: %d %q", code, stderr)
	}
}
