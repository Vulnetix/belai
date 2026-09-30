package e2e

import (
	"encoding/json"
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

// gatesMock is fleetMock with a reviewer that records a manual gate: the
// GATE-REVIEWER persona calls KanbanGate on its first pass, and the
// WRITER-PERSONA builder writes work.txt. Everything else answers as
// fleetMock does.
func gatesMock(t *testing.T) *httptest.Server {
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
		sawTool := false
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				lastUser = m.Content
			case "tool":
				sawTool = true
			}
		}
		switch {
		case strings.Contains(system, "security classifier"):
			writeChat(w, "SAFE")
		case strings.Contains(system, "goal-progress evaluator"):
			writeChat(w, "GOAL_COMPLETE")
		case strings.Contains(lastUser, "reviewing a coding agent"):
			writeChat(w, "- gated cards are verified by the harness")
		case strings.Contains(lastUser, "final report") || strings.Contains(lastUser, "Write a report"):
			writeChat(w, "Done.")
		default:
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			switch {
			case !sawTool && strings.Contains(system, "WRITER-PERSONA"):
				writeToolCallChat(w, "Write", map[string]any{"file_path": "work.txt", "content": "work\n"}, "Plan:\n1. Write work.txt\n[DONE:1]\n")
			case !sawTool && strings.Contains(system, "GATE-REVIEWER"):
				writeToolCallChat(w, "KanbanGate", map[string]any{"gate": "G1", "state": "met", "evidence": "checked the wording against the request"}, "Plan:\n1. Decide the gate\n[DONE:1]\n")
			default:
				writeToolCallChat(w, "Read", map[string]any{"file_path": "go.mod", "limit": 10 + n}, "Plan:\n1. Look\n[DONE:1]\n")
			}
		}
	}))
}

const gatedBuilderMD = `---
name: t-gbuilder
description: Writes work.txt for items labelled build, verified by the harness
mode: worker
autonomy: autonomous
tools: [Read, Write, update_plan]
max_iterations: 2
kanban:
  labels: [build]
  on_success: {list: review, labels: [needs-review], drop_labels: [build]}
  gates: {verify: enforce}
  max_attempts: 3
  lease: 5m
workspace: {isolation: worktree}
budget: {max_passes_per_item: 4}
---
WRITER-PERSONA. Implement the attached item.
`

const gatedReviewerJSON = `{
  "name": "t-greviewer",
  "description": "Re-verifies the built branch and decides manual gates",
  "system_prompt": "GATE-REVIEWER. Review the attached item's branch.",
  "tools": ["Read"],
  "mode": "worker",
  "autonomy": "autonomous",
  "max_iterations": 2,
  "kanban": {
    "lists": ["review"],
    "labels": ["needs-review"],
    "on_success": {"list": "done", "drop_labels": ["needs-review"]},
    "on_failure": {"list": "backlog", "labels": ["build"], "drop_labels": ["needs-review"]},
    "gates": {"verify": "enforce", "review": true},
    "lease": "5m"
  },
  "workspace": {"isolation": "worktree"},
  "budget": {"max_passes_per_item": 4}
}`

// gatedEnv is a fleet environment over a small Go module, so the harness
// detects a real test suite and runs it.
func gatedEnv(t *testing.T) *fleetEnv {
	t.Helper()
	e := newFleetEnv(t)
	e.url = ""
	srv := gatesMock(t)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	files := map[string]string{
		"go.mod":      "module example.com/fix\n\ngo 1.21\n",
		"p/p.go":      "package p\n\nfunc Add(a, b int) int { return a + b }\n",
		"p/p_test.go": "package p\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
		".gitignore":  ".vulnetix/\n",
	}
	for name, body := range files {
		p := filepath.Join(e.repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.git("add", ".")
	e.git("commit", "-q", "-m", "fixture")
	// No OS sandbox: the point here is the harness's verification, and CI
	// runners differ in what a sandbox allows.
	os.WriteFile(filepath.Join(e.home, "settings.json"), []byte(`{"resilience":{"max_iterations":2},"sandbox":{"mode":"off"}}`), 0o600)
	md := filepath.Join(t.TempDir(), "t-gbuilder.md")
	js := filepath.Join(t.TempDir(), "t-greviewer.json")
	os.WriteFile(md, []byte(gatedBuilderMD), 0o600)
	os.WriteFile(js, []byte(gatedReviewerJSON), 0o600)
	e.mustBelai("agent", "import", md)
	e.mustBelai("agent", "import", js)
	return e
}

func (e *fleetEnv) importJSONL(lines ...string) []string {
	e.t.Helper()
	p := filepath.Join(e.t.TempDir(), "cards.jsonl")
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	var shorts []string
	for _, l := range strings.Split(strings.TrimSpace(e.mustBelai("kanban", "import", p)), "\n") {
		if strings.HasPrefix(l, "K-") {
			shorts = append(shorts, l)
		}
	}
	return shorts
}

// TestGatedDeliveryEndToEnd drives the delivery loop through the real binary
// on a Go module: the harness runs each card's gates on the builder's branch
// and again on the reviewer's, a named test that selects nothing is never a
// pass, and a reviewer decides a manual gate.
func TestGatedDeliveryEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not found")
	}
	e := gatedEnv(t)
	cards := e.importJSONL(
		`{"title":"Keep Add correct","labels":["build"],"priority":3,"gates":[{"title":"Add still adds","kind":"runnable","suite":"go","dir":"p","test":"TestAdd"}]}`,
		`{"title":"Cover the missing test","labels":["build"],"priority":2,"gates":[{"title":"a test that does not exist","kind":"runnable","suite":"go","dir":"p","test":"TestMissing"}]}`,
		`{"title":"Confirm the wording","list":"review","labels":["needs-review"],"priority":1,"gates":[{"title":"the wording matches the request","kind":"manual"}]}`,
	)
	if len(cards) != 3 {
		t.Fatalf("filed %v", cards)
	}
	good, missing, manual := cards[0], cards[1], cards[2]
	if it := e.item(good); len(it.Gates) != 1 || it.Gates[0].State != kanban.GateUnmet || it.Gates[0].ID != "G1" {
		t.Fatalf("an imported gate starts unmet with a harness id: %+v", it.Gates)
	}
	if _, errOut, code := e.belai("kanban", "import", writeTemp(t, `{"title":"bad gate","gates":[{"title":"x","kind":"runnable","suite":"go","dir":"../etc"}]}`)); code == 0 || !strings.Contains(errOut, "invalid acceptance gate") {
		t.Fatalf("an unsafe gate is refused at import: %d %s", code, errOut)
	}

	run := func(profile string) {
		t.Helper()
		e.mustBelai("agent", "run", "-trust-dir", "-once", "-provider", "openai", "-model", "test", profile)
	}
	// The builder works the highest priority card first.
	run("t-gbuilder")
	it := e.item(good)
	if it.List != kanban.Review || it.Attempts != 0 {
		t.Fatalf("a card whose gate passes goes to review: %s attempts %d note %q", it.List, it.Attempts, it.LastNote())
	}
	if g := it.Gates[0]; g.State != kanban.GateMet || len(g.Ref) != 40 || g.Note != "exit 0" {
		t.Fatalf("the harness recorded the gate: %+v", g)
	}
	if !strings.Contains(it.LastNote(), "verified at") {
		t.Fatalf("note %q", it.LastNote())
	}
	if got := e.git("show", it.Branch+":work.txt"); got != "work" {
		t.Fatalf("branch content %q", got)
	}
	recs, _ := filepath.Glob(filepath.Join(e.repo, ".vulnetix", "belai", "quality", "verify", good+"-*.json"))
	if len(recs) != 1 {
		t.Fatalf("one verification record for %s: %v", good, recs)
	}
	raw, _ := os.ReadFile(recs[0])
	if strings.Contains(string(raw), "TestAdd (") || strings.Contains(string(raw), "ok  \t") {
		t.Fatalf("test output text reached the record: %s", raw)
	}

	// The second card names a test that does not exist: go exits 0 with
	// "no tests to run", and the harness refuses to call that a pass.
	run("t-gbuilder")
	it = e.item(missing)
	if it.List != kanban.Backlog || it.Attempts != 1 {
		t.Fatalf("a vacuous gate fails the attempt: %s attempts %d", it.List, it.Attempts)
	}
	if g := it.Gates[0]; g.State != kanban.GateUnmet || !strings.Contains(g.Note, "selected no tests") {
		t.Fatalf("gate %+v", g)
	}
	if n := it.LastNote(); !strings.Contains(n, "verification failed") || !strings.Contains(n, "G1 unmet") {
		t.Fatalf("note %q", n)
	}

	// The reviewer re-verifies the built card and decides the manual one.
	run("t-greviewer")
	run("t-greviewer")
	if it := e.item(good); it.List != kanban.Done || len(it.Labels) != 0 {
		t.Fatalf("the reviewed card is done: %s %v note %q", it.List, it.Labels, it.LastNote())
	}
	it = e.item(manual)
	if it.List != kanban.Done {
		t.Fatalf("a manual gate the reviewer met lets the card close: %s note %q", it.List, it.LastNote())
	}
	if g := it.Gates[0]; g.State != kanban.GateMet || g.Note != "checked the wording against the request" {
		t.Fatalf("the reviewer's decision is on the gate: %+v", g)
	}
	// The gate's evidence stays on the gate, not in the release note.
	if strings.Contains(it.LastNote(), "checked the wording") {
		t.Fatalf("model text reached the release note: %q", it.LastNote())
	}
}
