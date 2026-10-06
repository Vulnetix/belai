package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

const wrapUpPhrase = "Now update the global kanban board"

// kanbanScript answers classifier calls and scripts the main model: the loop
// calls loopTool once and then replies with report; the wrap-up calls
// wrapTool once and then replies KANBAN_DONE.
type kanbanScript struct {
	loopTool, loopArgs string // "" replies at once (a Q&A turn)
	report             string
	wrapTool, wrapArgs string

	mu        sync.Mutex
	mainCalls []string // raw bodies of main-model requests
	results   []string // tool results the model saw, in order
}

func (k *kanbanScript) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		var system string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
		}
		switch {
		case contains(system, "security classifier"):
			writeChatJSON(w, "SAFE")
			return
		case contains(system, "operating-mode classifier"):
			writeChatJSON(w, "AGENT")
			return
		}
		k.mu.Lock()
		k.mainCalls = append(k.mainCalls, string(raw))
		k.mu.Unlock()
		wrap := -1
		for i, m := range req.Messages {
			if strings.Contains(m.Content, wrapUpPhrase) {
				wrap = i
			}
		}
		toolAfter := func(from int) bool {
			for _, m := range req.Messages[from+1:] {
				if m.Role == "tool" {
					k.mu.Lock()
					k.results = append(k.results, m.Content)
					k.mu.Unlock()
					return true
				}
			}
			return false
		}
		if wrap >= 0 {
			if k.wrapTool == "" || toolAfter(wrap) {
				writeChatJSON(w, "KANBAN_DONE")
				return
			}
			writeToolCallJSON(w, k.wrapTool, k.wrapArgs)
			return
		}
		if k.loopTool == "" || toolAfter(0) {
			writeChatJSON(w, k.report)
			return
		}
		writeToolCallJSON(w, k.loopTool, k.loopArgs)
	}))
}

func kanbanSession(t *testing.T, srv *httptest.Server, store *kanban.Store, src *kanban.Source, root string) *Session {
	t.Helper()
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}
	reg := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}).WithKanban(store, src)
	sess, err := NewSession(Options{
		Cfg: cfg, Client: srv.Client(), Registry: reg, Posture: posture.Defaults(),
		Workdir: root, AllowPassLoop: true, SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func kanbanFixture(t *testing.T) (root string, store *kanban.Store, src *kanban.Source) {
	root = t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o600)
	store = kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	src = kanban.NewSource(kanban.Provenance{SessionID: "sess-a", HostID: "h", Project: "demo", ProjectKey: "demo-1", Dir: root})
	return root, store, src
}

func runObserved(t *testing.T, sess *Session, prompt string) (run.Result, []Event) {
	t.Helper()
	var mu sync.Mutex
	var events []Event
	res, err := sess.RunObserved(context.Background(), prompt, func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res, events
}

func kanbanEvents(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if e.Kind == EventKanbanKind {
			out = append(out, e)
		}
	}
	return out
}

func TestKanbanWrapUpFilesOpenWorkAfterAWorkTurn(t *testing.T) {
	root, store, src := kanbanFixture(t)
	secret, _, _ := store.Add(kanban.ItemInput{Title: "SECRET-TITLE never in a directive", List: kanban.InProgress}, src.Get())
	ks := &kanbanScript{
		loopTool: "Read", loopArgs: `{"path":"a.txt"}`,
		report:   "Read the file. Remaining: tests were not run.",
		wrapTool: "KanbanAdd", wrapArgs: `{"title":"Run the tests for a.txt","category":"unverified"}`,
	}
	srv := ks.server(t)
	defer srv.Close()
	res, events := runObserved(t, kanbanSession(t, srv, store, src, root), "read a.txt")

	if res.Reply != ks.report {
		t.Fatalf("reply = %q, want the report", res.Reply)
	}
	for _, e := range events {
		if e.Kind == EventTextKind && strings.Contains(e.Text, "KANBAN_DONE") {
			t.Fatal("the wrap-up's reply streamed into the transcript")
		}
	}
	ke := kanbanEvents(events)
	if len(ke) != 2 || ke[0].Phase != KanbanPhaseStart || ke[1].Phase != KanbanPhaseDone || ke[1].Kanban.Added != 1 {
		t.Fatalf("kanban events %+v", ke)
	}
	items, _ := store.Search(kanban.Query{Lists: []kanban.List{kanban.Review}})
	if len(items) != 1 || items[0].Title != "Run the tests for a.txt" || items[0].SessionID != "sess-a" || items[0].Project != "demo" {
		t.Fatalf("review items %+v", items)
	}

	first := ks.mainCalls[0]
	if !strings.Contains(first, "Kanban board for project demo") || !strings.Contains(first, secret.Short()) {
		t.Fatal("the loop directive is missing the board facts")
	}
	if strings.Contains(first, "SECRET-TITLE") {
		t.Fatal("an item title rode in the loop directive")
	}
	if got := strings.Join(advertised(t, first), ","); got != "Read,KanbanSearch,KanbanUpdate,ReadResult,KanbanMove" {
		t.Fatalf("loop surface = %s", got)
	}
	last := ks.mainCalls[len(ks.mainCalls)-1]
	for _, cat := range []string{"unverified", "found-not-fixed", "tech-debt", "question"} {
		if !strings.Contains(last, cat) {
			t.Fatalf("wrap-up directive lacks the %q triggers", cat)
		}
	}
	if got := strings.Join(advertised(t, last), ","); got != "Read,KanbanSearch,KanbanUpdate,ReadResult,KanbanAdd,KanbanMove" {
		t.Fatalf("wrap-up surface = %s", got)
	}
}

func TestKanbanWrapUpSkipsAQuestionTurn(t *testing.T) {
	root, store, src := kanbanFixture(t)
	ks := &kanbanScript{report: "It is a text file."}
	srv := ks.server(t)
	defer srv.Close()
	res, events := runObserved(t, kanbanSession(t, srv, store, src, root), "what is a.txt?")
	if res.Reply != ks.report {
		t.Fatalf("reply %q", res.Reply)
	}
	if len(kanbanEvents(events)) != 0 || len(ks.mainCalls) != 1 {
		t.Fatalf("a Q&A turn ran the wrap-up (%d main calls)", len(ks.mainCalls))
	}
}

// A model that stopped before its work was done can still do it in the
// wrap-up: the turn's own tools run, they are not refused.
func TestKanbanWrapUpAllowsOtherTools(t *testing.T) {
	root, store, src := kanbanFixture(t)
	ks := &kanbanScript{
		loopTool: "Read", loopArgs: `{"path":"a.txt"}`, report: "done, but the tests were not run",
		wrapTool: "Read", wrapArgs: `{"path":"a.txt"}`,
	}
	srv := ks.server(t)
	defer srv.Close()
	runObserved(t, kanbanSession(t, srv, store, src, root), "read a.txt")
	got := ks.results[len(ks.results)-1]
	if strings.Contains(got, "withheld") || !strings.Contains(got, "hello") {
		t.Fatalf("wrap-up Read result = %q, want the file", got)
	}
}

func TestKanbanAddIsRefusedDuringTheLoop(t *testing.T) {
	root, store, src := kanbanFixture(t)
	ks := &kanbanScript{loopTool: "KanbanAdd", loopArgs: `{"title":"sneak"}`, report: "done"}
	srv := ks.server(t)
	defer srv.Close()
	runObserved(t, kanbanSession(t, srv, store, src, root), "do it")
	if !strings.Contains(ks.results[0], "only offered after the final report") {
		t.Fatalf("loop KanbanAdd result = %q", ks.results[0])
	}
	if items, _ := store.Search(kanban.Query{Text: "sneak"}); len(items) != 0 {
		t.Fatal("KanbanAdd wrote during the loop")
	}
}

func TestKanbanLoopMovesItems(t *testing.T) {
	root, store, src := kanbanFixture(t)
	it, _, _ := store.Add(kanban.ItemInput{Title: "the task", List: kanban.Backlog}, src.Get())
	ks := &kanbanScript{loopTool: "KanbanMove", loopArgs: `{"id":"` + it.Short() + `","to":"in_progress"}`, report: "started"}
	srv := ks.server(t)
	defer srv.Close()
	runObserved(t, kanbanSession(t, srv, store, src, root), "work on "+it.Short())
	got, _ := store.Get(it.ID)
	if got.List != kanban.InProgress {
		t.Fatalf("list = %s", got.List)
	}
	if !strings.Contains(ks.mainCalls[0], it.Short()+" backlog (named in the prompt)") {
		t.Fatal("an item named in the prompt is missing from the directive")
	}
}

func TestSubagentSessionsGetNoKanbanWrites(t *testing.T) {
	_, store, src := kanbanFixture(t)
	reg := tools.NewRegistry().WithKanban(store, src).Without(tools.KanbanUpdateName)
	if ks := newKanbanState(reg); ks.on {
		t.Fatal("a search-only registry turned the kanban loop on")
	}
}

func TestKanbanTriggerCatalogue(t *testing.T) {
	want := []string{"unfinished", "markers", "deferred", "unverified", "blocked", "found-not-fixed", "temporary", "tech-debt", "docs", "operational", "question"}
	if len(KanbanTriggers) != len(want) {
		t.Fatalf("%d categories, want %d", len(KanbanTriggers), len(want))
	}
	for i, c := range want {
		if KanbanTriggers[i].Category != c || KanbanTriggers[i].Signals == "" {
			t.Fatalf("category %d = %q", i, KanbanTriggers[i].Category)
		}
	}
}

// advertised returns the tool names a request body offers.
func advertised(t *testing.T, body string) []string {
	t.Helper()
	var req struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, tl := range req.Tools {
		out = append(out, tl.Function.Name)
	}
	return out
}

func TestKanbanWrapUpSkipsCleanWorkOnAnEmptyBoard(t *testing.T) {
	root, store, src := kanbanFixture(t)
	ks := &kanbanScript{
		loopTool: "Read", loopArgs: `{"path":"a.txt"}`, report: "Created mathx.js and test.js. node --test: 4 pass, 0 fail.",
		wrapTool: "KanbanAdd", wrapArgs: `{"title":"x"}`,
	}
	srv := ks.server(t)
	defer srv.Close()
	_, events := runObserved(t, kanbanSession(t, srv, store, src, root), "read a.txt")
	if len(kanbanEvents(events)) != 0 || len(ks.mainCalls) != 2 {
		t.Fatalf("a clean turn on an empty board ran the wrap-up (%d main calls)", len(ks.mainCalls))
	}

	// An open item on this project's board may have been finished: run it.
	store.Add(kanban.ItemInput{Title: "open item", List: kanban.Review}, src.Get())
	ks.mainCalls = nil
	_, events = runObserved(t, kanbanSession(t, srv, store, src, root), "read a.txt")
	if len(kanbanEvents(events)) != 2 {
		t.Fatal("open board items did not run the wrap-up")
	}
}

func TestOpenWorkPattern(t *testing.T) {
	open := []string{
		"Remaining: tests were not run.", "TODO: handle overflow", "Follow-up: update the docs",
		"I left the workaround in place", "The build failed on Windows", "You might also want to add caching",
		"This is a partial fix", "Next steps are below", "Remaining:\n- wire the CLI", "**Known issues** — the parser drops comments",
	}
	for _, s := range open {
		if !reportHasOpenWork(s) {
			t.Errorf("missed open work: %q", s)
		}
	}
	clean := []string{
		"Created mathx.js, cli.js and test.js. node --test: 4 pass, 0 fail. Everything passes.",
		`It returns "hello-world-2026": trim, lowercase, strip punctuation, then hyphenate.`,
		"It removes the comma, and finally collapses the remaining whitespace into single hyphens.",
		"No code changes were required. Nothing is left open.", "There are no failing tests and no known issues.",
		"`node --test` — 4 tests passed, 0 failed.",
		"**Follow-up:** Nothing remaining. The task is complete.", "Known issues: none.",
	}
	for _, s := range clean {
		if reportHasOpenWork(s) {
			t.Errorf("flagged a clean report: %q", s)
		}
	}
}

func TestIsVerifyingBash(t *testing.T) {
	cases := []struct {
		cmd, result string
		want        bool
	}{
		{"node --test", "ℹ pass 4\nℹ fail 0", true},
		{"node cli.js 12 18", "gcd=6 lcm=36", true},
		{"go test ./...", "--- FAIL\nexit status 1", false},
		{"ls -la && cat a.js", "a.js", false},
		{"git status && git diff", "", false},
		{"npm run build", "tool result withheld: permission denied", false},
	}
	for _, c := range cases {
		if got := isVerifyingBash("Bash", map[string]any{"command": c.cmd}, c.result); got != c.want {
			t.Errorf("isVerifyingBash(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
	if isVerifyingBash("Read", map[string]any{"command": "node --test"}, "") {
		t.Error("a non-Bash tool counted as verification")
	}
}
