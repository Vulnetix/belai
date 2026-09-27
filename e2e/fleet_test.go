package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// fleetMock answers every call a worker makes: the security classifier,
// the goal evaluator (always GOAL_COMPLETE; the verification gate makes it
// ask twice), the reflection turn, and the main model, which writes
// hello.txt on its first pass and reads it on every later one.
func fleetMock(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var system, lastUser string
		wrote := false
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				lastUser = m.Content
			case "tool":
				wrote = true
			}
		}
		switch {
		case strings.Contains(system, "security classifier"):
			writeChat(w, "SAFE")
		case strings.Contains(system, "goal-progress evaluator"):
			writeChat(w, "GOAL_COMPLETE")
		case strings.Contains(lastUser, "reviewing a coding agent"):
			writeChat(w, "- hello.txt lives at the repository root")
		case strings.Contains(lastUser, "final report") || strings.Contains(lastUser, "Write a report"):
			writeChat(w, "Added hello.txt and read it back.")
		default:
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if !wrote && strings.Contains(system, "WRITER-PERSONA") {
				writeToolCallChat(w, "Write", map[string]any{"file_path": "hello.txt", "content": "hello\n"}, "Plan:\n1. Add hello.txt\n[DONE:1]\n")
				return
			}
			writeToolCallChat(w, "Read", map[string]any{"file_path": "hello.txt", "limit": 10 + n}, "Plan:\n1. Check hello.txt\n[DONE:1]\n")
		}
	}))
}

const fleetBuilderMD = `---
name: t-builder
description: Writes hello.txt for items labelled build
mode: worker
autonomy: autonomous
tools: [Read, Write, update_plan]
max_iterations: 2
kanban:
  labels: [build]
  on_success: {list: review, labels: [needs-review], drop_labels: [build]}
  lease: 5m
workspace: {isolation: worktree}
budget: {max_passes_per_item: 4}
memory: {enabled: true}
---
WRITER-PERSONA. Implement the attached item.
`

const fleetReviewerJSON = `{
  "name": "t-reviewer",
  "description": "Reads the built branch and approves it",
  "system_prompt": "REVIEWER-PERSONA. Review the attached item's branch.",
  "tools": ["Read"],
  "mode": "worker",
  "autonomy": "supervised",
  "max_iterations": 2,
  "kanban": {
    "lists": ["review"],
    "labels": ["needs-review"],
    "on_success": {"list": "done", "drop_labels": ["needs-review"]},
    "on_failure": {"list": "backlog", "labels": ["build"], "drop_labels": ["needs-review"]},
    "lease": "5m"
  },
  "workspace": {"isolation": "worktree"},
  "budget": {"max_passes_per_item": 4}
}`

type fleetEnv struct {
	t     *testing.T
	repo  string
	home  string
	wt    string
	url   string
	store *kanban.Store
}

func newFleetEnv(t *testing.T) *fleetEnv {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	srv := fleetMock(t)
	t.Cleanup(srv.Close)
	e := &fleetEnv{t: t, repo: t.TempDir(), home: t.TempDir(), wt: t.TempDir(), url: srv.URL}
	e.store = kanban.Open(filepath.Join(e.home, "kanban"))
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.invalid"},
	} {
		e.git(args...)
	}
	os.WriteFile(filepath.Join(e.repo, "README"), []byte("fleet test\n"), 0o644)
	e.git("add", "README")
	e.git("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"resilience":{"max_iterations":2}}`), 0o600)
	return e
}

func (e *fleetEnv) git(args ...string) string {
	e.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", e.repo}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// belai runs the binary in the repository with the fleet's isolated state.
func (e *fleetEnv) belai(args ...string) (string, string, int) {
	e.t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, args...)
	cmd.Dir = e.repo
	cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+e.url, "OPENAI_API_KEY=test",
		"BELAI_HOME="+e.home, "BELAI_WORKTREES_DIR="+e.wt)
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			e.t.Fatalf("run belai: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func (e *fleetEnv) mustBelai(args ...string) string {
	e.t.Helper()
	out, errOut, code := e.belai(args...)
	if code != 0 {
		e.t.Fatalf("belai %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

func (e *fleetEnv) importProfiles() {
	e.t.Helper()
	md := filepath.Join(e.t.TempDir(), "t-builder.md")
	js := filepath.Join(e.t.TempDir(), "t-reviewer.json")
	os.WriteFile(md, []byte(fleetBuilderMD), 0o600)
	os.WriteFile(js, []byte(fleetReviewerJSON), 0o600)
	e.mustBelai("agent", "validate", md)
	e.mustBelai("agent", "import", md)
	e.mustBelai("agent", "import", js)
}

func (e *fleetEnv) item(short string) kanban.Item {
	e.t.Helper()
	it, err := e.store.Get(short)
	if err != nil {
		e.t.Fatalf("get %s: %v", short, err)
	}
	return it
}

// TestFleetBuilderThenReviewer drives the whole loop through the binary:
// work is filed with `belai kanban add`, a builder claims it and commits
// hello.txt on its own branch in a worktree outside the repository, hands it
// to review, and a reviewer checks the branch out and closes the item.
func TestFleetBuilderThenReviewer(t *testing.T) {
	e := newFleetEnv(t)
	e.importProfiles()
	short := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt", "-label", "build", "-priority", "2"))
	if !strings.HasPrefix(short, "K-") {
		t.Fatalf("kanban add printed %q", short)
	}

	// An untrusted repository fails closed.
	if _, errOut, code := e.belai("agent", "run", "-once", "t-builder", "-provider", "openai", "-model", "test"); code == 0 || !strings.Contains(errOut, "not a trusted workspace") {
		t.Fatalf("untrusted run: exit %d %s", code, errOut)
	}

	e.mustBelai("agent", "run", "-trust-dir", "-once", "-provider", "openai", "-model", "test", "t-builder")
	it := e.item(short)
	if it.List != kanban.Review || it.ClaimedBy != "" || it.Branch == "" || !strings.HasPrefix(it.Branch, "belai/"+short) {
		t.Fatalf("after builder: %+v", it)
	}
	if strings.Join(it.Labels, ",") != "needs-review" {
		t.Fatalf("labels %v", it.Labels)
	}
	if got := e.git("show", it.Branch+":hello.txt"); got != "hello" {
		t.Fatalf("branch content %q", got)
	}
	if _, err := os.Stat(filepath.Join(e.repo, "hello.txt")); err == nil {
		t.Fatal("the builder wrote into the main checkout")
	}
	if strings.Contains(e.git("worktree", "list"), e.wt) {
		t.Fatal("the worktree was not removed after release")
	}
	// A transcript was written under the repository's project key.
	sessions, _ := filepath.Glob(filepath.Join(e.home, "sessions", "*", "*.jsonl"))
	if len(sessions) == 0 {
		t.Fatal("no worker transcript")
	}
	// The reflection wrote a lesson.
	if out := e.mustBelai("agent", "memory", "t-builder"); !strings.Contains(out, "hello.txt lives") {
		t.Fatalf("memory %q", out)
	}

	e.mustBelai("agent", "run", "-once", "-provider", "openai", "-model", "test", "t-reviewer")
	it = e.item(short)
	if it.List != kanban.Done || len(it.Labels) != 0 {
		t.Fatalf("after reviewer: %+v", it)
	}

	// The registry recorded both workers as stopped, one item done each.
	out := e.mustBelai("agent", "ps", "-all", "-json")
	var recs []struct {
		Profile string `json:"profile"`
		State   string `json:"state"`
		Done    int    `json:"done"`
	}
	if err := json.Unmarshal([]byte(out), &recs); err != nil || len(recs) != 2 {
		t.Fatalf("ps: %v %s", err, out)
	}
	for _, r := range recs {
		if r.State != "stopped" || r.Done != 1 {
			t.Fatalf("record %+v", r)
		}
	}
	// No work: a -once worker exits cleanly.
	e.mustBelai("agent", "run", "-once", "-provider", "openai", "-model", "test", "t-builder")
}

// TestFleetConcurrentBuildersNeverShareAnItem runs two builders at once over
// two items: each claims exactly one.
func TestFleetConcurrentBuildersNeverShareAnItem(t *testing.T) {
	e := newFleetEnv(t)
	e.importProfiles()
	a := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt one", "-label", "build"))
	b := strings.TrimSpace(e.mustBelai("kanban", "add", "Add hello.txt two", "-label", "build"))
	// Trust once, up front: a reviewer run that finds no work.
	e.mustBelai("agent", "run", "-trust-dir", "-once", "-provider", "openai", "-model", "test", "t-reviewer")
	var wg sync.WaitGroup
	errs := make([]string, 2)
	for i := range 2 {
		wg.Go(func() {
			out, errOut, code := e.belai("agent", "run", "-once", "-provider", "openai", "-model", "test", "t-builder")
			if code != 0 {
				errs[i] = fmt.Sprintf("exit %d: %s %s", code, out, errOut)
			}
		})
	}
	wg.Wait()
	for _, s := range errs {
		if s != "" {
			t.Fatal(s)
		}
	}
	ia, ib := e.item(a), e.item(b)
	if ia.List != kanban.Review || ib.List != kanban.Review {
		t.Fatalf("items %s %s", ia.List, ib.List)
	}
	claimers := map[string]bool{}
	for _, it := range []kanban.Item{ia, ib} {
		for _, m := range it.History {
			if m.To == kanban.InProgress {
				if claimers[m.SessionID] {
					t.Fatalf("worker %s claimed twice", m.SessionID)
				}
				claimers[m.SessionID] = true
			}
		}
	}
	if len(claimers) != 2 {
		t.Fatalf("claims %v", claimers)
	}
}

// TestKanbanCLI covers filing, listing, moving and importing from the
// command line.
func TestKanbanCLI(t *testing.T) {
	e := newFleetEnv(t)
	first := strings.TrimSpace(e.mustBelai("kanban", "add", "-label", "Docs", "-assignee", "belai:builder", "Write", "the", "docs"))
	e.mustBelai("kanban", "add", "Second", "-depends", first)
	jsonl := filepath.Join(t.TempDir(), "items.jsonl")
	os.WriteFile(jsonl, []byte(`{"title":"Imported","labels":["build"],"priority":3}
# a comment
{"title":"Imported two","list":"review"}
`), 0o600)
	if out := e.mustBelai("kanban", "import", jsonl); !strings.Contains(out, "2 items filed") {
		t.Fatalf("import: %s", out)
	}
	if _, errOut, code := e.belai("kanban", "import", writeTemp(t, `{"title":"x","bogus":1}`)); code == 0 || !strings.Contains(errOut, "bogus") {
		t.Fatalf("unknown field accepted: %s", errOut)
	}
	out := e.mustBelai("kanban", "list", "-label", "docs")
	if !strings.Contains(out, "Write the docs") || !strings.Contains(out, "@belai:builder") || strings.Contains(out, "Second") {
		t.Fatalf("list: %s", out)
	}
	e.mustBelai("kanban", "move", first, "done", "-note", "shipped")
	e.mustBelai("kanban", "note", first, "a", "note")
	it := e.item(first)
	if it.List != kanban.Done || it.LastNote() != "a note" {
		t.Fatalf("item %+v", it)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(e.mustBelai("kanban", "list", "-json", "-project", "all", "-list", "backlog,review,done")), &items); err != nil || len(items) != 4 {
		t.Fatalf("json list %v: %d", err, len(items))
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.jsonl")
	os.WriteFile(p, []byte(content), 0o600)
	return p
}

// TestFleetDetachedStartPsStop starts a detached worker, sees it in ps and
// its log, and stops it.
func TestFleetDetachedStartPsStop(t *testing.T) {
	e := newFleetEnv(t)
	e.importProfiles()
	out := e.mustBelai("agent", "start", "-trust-dir", "-provider", "openai", "-model", "test", "t-reviewer")
	id, state, _ := strings.Cut(strings.TrimSpace(out), "  ")
	if !strings.HasPrefix(id, "t-reviewer-") || !strings.Contains(state, "idle") {
		t.Fatalf("start printed %q", out)
	}
	var recs []map[string]any
	if err := json.Unmarshal([]byte(e.mustBelai("agent", "ps", "-json")), &recs); err != nil || len(recs) != 1 || recs[0]["id"] != id {
		t.Fatalf("ps: %v %v", err, recs)
	}
	if logs := e.mustBelai("agent", "logs", id); !strings.Contains(logs, "started in") {
		t.Fatalf("logs %q", logs)
	}
	// A second start over the cap is refused before anything runs.
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"resilience":{"max_iterations":2},"agents":{"max_workers":1}}`), 0o600)
	if _, errOut, code := e.belai("agent", "start", "t-reviewer"); code == 0 || !strings.Contains(errOut, "max_workers") {
		t.Fatalf("over-cap start: %d %s", code, errOut)
	}
	if out := e.mustBelai("agent", "stop", "-all"); !strings.Contains(out, id+" stopped") {
		t.Fatalf("stop: %s", out)
	}
	if out := e.mustBelai("agent", "ps"); !strings.Contains(out, "no workers running") {
		t.Fatalf("ps after stop: %s", out)
	}
}
