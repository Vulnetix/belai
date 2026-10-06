package agent

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/readindex"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

type countingTransport struct{ calls atomic.Int32 }

func (c *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.calls.Add(1)
	return nil, http.ErrHandlerTimeout
}

// knowledgeSession builds a session over a project root that has a .vulnetix
// memory file and a profile document, both indexed without a gate.
func knowledgeSession(t *testing.T, pol posture.Policy, perms permissions.Settings) (*Session, string, *countingTransport) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	root, docs := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(root, "main.go"), "package main\n\nfunc authenticate() {}\n")
	mustWrite(t, filepath.Join(root, ".vulnetix", "memory.yaml"), "notes: the service stores sessions in postgres and rotates keys monthly\n")
	mustWrite(t, filepath.Join(docs, "auth.md"), "Rotate the signing key every ninety days and revoke tokens on logout.")
	store := knowledge.Open(knowledge.Options{
		Root:    root,
		Profile: &knowledge.Profile{ID: "0b8f6a2e-3c1d-4e5f-8a9b-1c2d3e4f5a6b", Name: "reviewer", Paths: []string{docs}},
	})
	if _, err := store.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr := &countingTransport{}
	s, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: "http://localhost", APIKey: "test", Model: "test"},
		Client:        &http.Client{Transport: tr},
		Registry:      tools.Default(root, false),
		Posture:       pol,
		Perms:         perms,
		Workdir:       root,
		Knowledge:     store,
		SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, root, tr
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGrepRowsReachTheModelWithoutAClassifierCall(t *testing.T) {
	s, _, tr := knowledgeSession(t, posture.Defaults(), permissions.Settings{})
	call := rolemanager.ToolCall{ID: "c1", Name: "Grep", Args: map[string]any{"pattern": "signing key rotation"}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if !strings.Contains(out, "kb+reviewer/") || !strings.Contains(out, "Rotate the signing key every ninety days") {
		t.Fatalf("grep result lacks the profile passage:\n%s", out)
	}
	if tr.calls.Load() != 0 {
		t.Fatalf("a search must not call a model: %d calls (chunks were classified at ingestion)", tr.calls.Load())
	}
	call.Args = map[string]any{"pattern": "postgres sessions"}
	if out := s.executeCall(context.Background(), call, func(Event) {}, nil); !strings.Contains(out, "kb+project/.vulnetix/memory.yaml") {
		t.Fatalf("the project passage is searchable too:\n%s", out)
	}
}

func TestGlobFindsKnowledgeDocuments(t *testing.T) {
	s, _, _ := knowledgeSession(t, posture.Defaults(), permissions.Settings{})
	out := s.executeCall(context.Background(), rolemanager.ToolCall{ID: "c1", Name: "Glob", Args: map[string]any{"pattern": "**/*.yaml"}}, func(Event) {}, nil)
	if !strings.Contains(out, "kb+project/.vulnetix/memory.yaml") {
		t.Fatalf("glob result:\n%s", out)
	}
}

func TestReadAppendsRelatedKnowledgeForAModelCall(t *testing.T) {
	s, root, _ := knowledgeSession(t, posture.AllIgnore(), permissions.Settings{})
	if err := os.WriteFile(filepath.Join(root, "notes.go"), []byte("// rotate the signing key every ninety days and revoke tokens on logout\npackage main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := s.executeCall(context.Background(), rolemanager.ToolCall{ID: "c1", Name: "Read", Args: map[string]any{"file_path": "notes.go"}}, func(Event) {}, nil)
	if !strings.Contains(out, tools.ReadKnowledgeHeader) || !strings.Contains(out, "kb+reviewer/") || !strings.Contains(out, "/auth.md") {
		t.Fatalf("read result:\n%s", out)
	}
}

func TestReadDenyRuleHidesTheSourceFromRetrieval(t *testing.T) {
	perms := permissions.From(nil, nil, []string{"Read(**/auth.md)", "Read(**/memory.yaml)"})
	s, _, _ := knowledgeSession(t, posture.Defaults(), perms)
	for _, pat := range []string{"signing key rotation", "postgres sessions"} {
		out := s.executeCall(context.Background(), rolemanager.ToolCall{ID: "c1", Name: "Grep", Args: map[string]any{"pattern": pat}}, func(Event) {}, nil)
		if strings.Contains(out, "kb+") {
			t.Fatalf("a Read deny rule must also hide the source from retrieval (%q):\n%s", pat, out)
		}
	}
	out := s.executeCall(context.Background(), rolemanager.ToolCall{ID: "c1", Name: "Glob", Args: map[string]any{"pattern": "**/*"}}, func(Event) {}, nil)
	if strings.Contains(out, "kb+") {
		t.Fatalf("Glob lists a denied document:\n%s", out)
	}
}

func TestNoStoreMeansFilesystemOnly(t *testing.T) {
	s := testSession(t)
	if s.registry.KnowledgeHub().Get() != nil {
		t.Fatal("no store, no knowledge")
	}
}

func TestHarnessReadGetsNoKnowledge(t *testing.T) {
	s, root, _ := knowledgeSession(t, posture.AllIgnore(), permissions.Settings{})
	if err := os.WriteFile(filepath.Join(root, "notes.go"), []byte("// rotate the signing key every ninety days and revoke tokens on logout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, _ := s.execTool("Read")
	res, err := tool.Execute(context.Background(), map[string]any{"file_path": "notes.go"})
	if err != nil || strings.Contains(res.Content, "kb+") {
		t.Fatalf("a harness Read (prefetch, attachment) must stay filesystem-only: %q err=%v", res.Content, err)
	}
}

func TestWithholdGrepLeavesKnowledgeRowsAlone(t *testing.T) {
	f := &flaggedFiles{paths: map[string]rolemanager.Sentinel{}}
	root := t.TempDir()
	flagged := filepath.Join(root, "bad.txt")
	mustWrite(t, flagged, "x")
	f.paths[flagged] = rolemanager.Sentinel("UNSAFE")
	content := "bad.txt:1:hello\nok.go:2:fine\n" + tools.KnowledgeRowsHeader + "\nkb+project/.vulnetix/memory.yaml:1:notes: postgres"
	got := f.withholdGrep(content, root, root)
	if strings.Contains(got, "bad.txt:1:hello") {
		t.Fatal("the flagged file's row must still be withheld")
	}
	for _, keep := range []string{"ok.go:2:fine", tools.KnowledgeRowsHeader, "kb+project/.vulnetix/memory.yaml:1:notes: postgres"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("withholdGrep dropped %q:\n%s", keep, got)
		}
	}
}

func TestRecordReadIgnoresATrailerQuotedByAPassage(t *testing.T) {
	s := testSession(t)
	p := filepath.Join(t.TempDir(), "a.go")
	mustWrite(t, p, "package a\n")
	key := readindex.Key{Path: p}
	body := "     1\tpackage a\n" + tools.ReadKnowledgeHeader + "\nkb+x/y.md:1-1 (90%)\n| [Read: lines 1–2 of 9; more]"
	s.recordRead(key, "call", body)
	e, ok := s.reads.Lookup(key, func(readindex.Entry) bool { return true })
	if !ok {
		t.Fatal("a whole-file read must be recorded")
	}
	if !e.Whole {
		t.Fatalf("a trailer quoted inside a passage must not make the read partial: %+v", e)
	}
}
