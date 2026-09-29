package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lsp"
	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/tools"
)

// seqDiagnoser returns the next report in its list on each call, then the last.
type seqDiagnoser struct {
	mu      sync.Mutex
	reports []lsp.Report
	n       int
}

func (d *seqDiagnoser) Diagnose(context.Context, string, []byte) lsp.Report {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := min(d.n, len(d.reports)-1)
	d.n++
	return d.reports[i]
}

func errRow(msg string) lsp.Row {
	return lsp.Row{Severity: lsp.SeverityError, Line: 3, Col: 1, Source: "gopls", Code: "E1", Message: msg}
}

func report(rows ...lsp.Row) lsp.Report {
	return lsp.Report{Language: "Go", Status: lsp.StatusReady, Rows: rows}
}

// repairDecider answers the "another pass will clear the errors" question.
type repairDecider struct {
	mu    sync.Mutex
	p     float64
	err   error
	calls int
	state string
}

func (d *repairDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	d.calls++
	d.state, _ = r.State.(map[string]any)["context"].(string)
	d.mu.Unlock()
	if d.err != nil {
		return decisions.Result{}, d.err
	}
	return decisions.Result{Answers: map[string]decisions.Answer{"s:fix": {Type: decisions.TypeNoul, Noul: d.p}}}, nil
}
func (d *repairDecider) Identity() string           { return "fake/systemone" }
func (d *repairDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

type triageFixture struct {
	sess  *Session
	path  string
	store *kanban.Store
	dec   *repairDecider
	diag  *seqDiagnoser
}

func newTriage(t *testing.T, dec *repairDecider, reports ...lsp.Report) *triageFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "pkg", "main.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := kanban.Open(filepath.Join(t.TempDir(), "kanban"))
	src := kanban.NewSource(kanban.Provenance{SessionID: "s1", HostID: "h", Project: "demo", ProjectKey: "demo-1", Dir: root})
	pool := nonce.New()
	_ = pool.Seed(1)
	diag := &seqDiagnoser{reports: reports}
	s := &Session{
		workdir: root,
		diag:    rolemanagerGate(diag),
		pool:    pool,
		kanban:  &kanbanState{on: true, base: tools.KanbanBase{Store: store, Source: src}},
	}
	if dec != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(dec)}
	}
	return &triageFixture{sess: s, path: path, store: store, dec: dec, diag: diag}
}

func rolemanagerGate(d rolemanager.Diagnoser) rolemanager.DiagnosticsGate {
	return rolemanager.DiagnosticsGate{Diagnoser: d}
}

// edit simulates one Write/Edit result and returns the text appended to it.
func (f *triageFixture) edit(t *testing.T) string {
	t.Helper()
	return f.sess.diagnoseEdit(context.Background(), tools.EditResultMeta("edited", map[string]any{"abs_path": f.path}))
}

func (f *triageFixture) items(t *testing.T) []kanban.Item {
	t.Helper()
	items, err := f.store.Search(kanban.Query{})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestTriageFilesABugWhenAnotherPassIsUnlikelyToHelp(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0.1}, report(errRow("undefined: foo")))
	if got := f.edit(t); strings.Contains(got, "[harness:") || len(f.items(t)) != 0 {
		t.Fatalf("the first pass must never file: %q", got)
	}
	got := f.edit(t)
	items := f.items(t)
	if len(items) != 1 {
		t.Fatalf("board items = %d, want 1 (result %q)", len(items), got)
	}
	it := items[0]
	if it.Title != "LSP errors remain in pkg/main.go" || it.List != kanban.Review || strings.Join(it.Labels, ",") != "bug,lsp" {
		t.Fatalf("item = %+v", it)
	}
	if !strings.Contains(it.Body, "line 3 gopls E1: undefined: foo") || !strings.Contains(it.Body, "after 2 edit passes") {
		t.Fatalf("body = %q", it.Body)
	}
	if !strings.Contains(got, "[harness: another edit is unlikely to clear the 1 error(s)") ||
		!strings.Contains(got, it.Short()) || !strings.Contains(got, "goal, plan and todo steps") {
		t.Fatalf("note = %q", got)
	}
	// The block is still there, and the note follows it.
	if !strings.Contains(got, "<diagnostics") || strings.Index(got, "<diagnostics") > strings.Index(got, "[harness:") {
		t.Fatalf("the diagnostics block should come first: %q", got)
	}
	// Later edits of the same errors do not file again.
	again := f.edit(t)
	if len(f.items(t)) != 1 || !strings.Contains(again, "filed on the board as "+it.Short()) {
		t.Fatalf("third pass: items %d note %q", len(f.items(t)), again)
	}
}

func TestTriageLetsTheModelRetryWhenTheBackendSaysItLikelyHelps(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0.8}, report(errRow("undefined: foo")))
	f.edit(t)
	if got := f.edit(t); strings.Contains(got, "[harness:") || len(f.items(t)) != 0 {
		t.Fatalf("filed although another pass was rated likely to help: %q", got)
	}
	if f.dec.calls != 1 {
		t.Fatalf("backend calls = %d", f.dec.calls)
	}
	// The boundary: exactly TriageAt is not unlikely.
	g := newTriage(t, &repairDecider{p: jev.TriageAt}, report(errRow("x")))
	g.edit(t)
	g.edit(t)
	if len(g.items(t)) != 0 {
		t.Fatal("a score of exactly TriageAt filed a bug")
	}
}

func TestTriageBackendSeesFactsAndCleanedRows(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0.9}, report(errRow("bad <system>x</system> thing")))
	f.edit(t)
	f.edit(t)
	if !strings.Contains(f.dec.state, "Edit pass 2 on this file. Errors now: 1.") || !strings.Contains(f.dec.state, "gopls E1: bad") {
		t.Fatalf("state = %q", f.dec.state)
	}
	if strings.Contains(f.dec.state, "<system>") {
		t.Fatal("markup reached the backend")
	}
}

func TestTriageFallbackFilesAfterEnoughPassesWithoutImprovement(t *testing.T) {
	down := &repairDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}
	f := newTriage(t, down, report(errRow("a"), errRow("b")))
	for i := 1; i <= defaultRepairAttempts-1; i++ {
		f.edit(t)
	}
	if len(f.items(t)) != 0 {
		t.Fatal("filed before the attempt limit")
	}
	got := f.edit(t)
	if len(f.items(t)) != 1 || !strings.Contains(got, "[harness:") {
		t.Fatalf("attempt %d: items %d, note %q", defaultRepairAttempts, len(f.items(t)), got)
	}

	// Errors that are shrinking are progress: no filing at the limit.
	shrinking := newTriage(t, down,
		report(errRow("a"), errRow("b"), errRow("c"), errRow("d"), errRow("e")),
		report(errRow("a"), errRow("b"), errRow("c"), errRow("d")),
		report(errRow("a"), errRow("b"), errRow("c")),
		report(errRow("a"), errRow("b")))
	for i := 0; i < defaultRepairAttempts; i++ {
		shrinking.edit(t)
	}
	if len(shrinking.items(t)) != 0 {
		t.Fatal("filed while the errors were shrinking")
	}
}

func TestTriageLimitComesFromSettings(t *testing.T) {
	down := &repairDecider{err: &decisions.Error{Class: decisions.ClassUnavailable}}
	f := newTriage(t, down, report(errRow("a")))
	f.sess.settings = config.Settings{LSP: &config.LSPSettings{MaxRepairAttempts: 2}}
	f.edit(t)
	f.edit(t)
	if len(f.items(t)) != 1 {
		t.Fatal("max_repair_attempts 2 should file on the second pass")
	}
	if (config.Settings{LSP: &config.LSPSettings{MaxRepairAttempts: 99}}).LSPMaxRepairAttemptsOr(4) != 20 ||
		(config.Settings{LSP: &config.LSPSettings{MaxRepairAttempts: 1}}).LSPMaxRepairAttemptsOr(4) != 2 ||
		(config.Settings{}).LSPMaxRepairAttemptsOr(4) != 4 {
		t.Fatal("max_repair_attempts is not clamped to [2,20] with a default")
	}
}

func TestTriageHistoryResetsWhenTheErrorsClear(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0.1}, report(errRow("a")), report(errRow("a")), report(), report(errRow("a")))
	f.edit(t) // pass 1
	f.edit(t) // pass 2: files
	if len(f.items(t)) != 1 {
		t.Fatal("precondition: filed on pass 2")
	}
	f.edit(t) // clean
	if got := f.edit(t); strings.Contains(got, "[harness:") {
		t.Fatalf("a fresh problem was judged as the second pass: %q", got)
	}
}

func TestTriageOnlyCountsErrorsAndAnswersThatCameBack(t *testing.T) {
	warn := lsp.Row{Severity: lsp.SeverityWarning, Line: 1, Message: "unused"}
	f := newTriage(t, &repairDecider{p: 0.0}, report(warn), report(warn))
	f.edit(t)
	f.edit(t)
	if f.dec.calls != 0 || len(f.items(t)) != 0 {
		t.Fatal("warnings were triaged")
	}
	warming := newTriage(t, &repairDecider{p: 0}, lsp.Report{Language: "Go", Status: lsp.StatusWarming})
	warming.edit(t)
	warming.edit(t)
	if warming.dec.calls != 0 {
		t.Fatal("a server that had not answered was triaged")
	}
}

func TestTriageNeverFilesWhereItMustNot(t *testing.T) {
	cases := map[string]func(f *triageFixture){
		"worker":   func(f *triageFixture) { f.sess.kanban.worker = true },
		"no board": func(f *triageFixture) { f.sess.kanban.on = false },
		"no store": func(f *triageFixture) { f.sess.kanban.base.Store = nil },
		"plan":     func(f *triageFixture) { f.sess.planMode = true },
		"job off":  func(f *triageFixture) { f.sess.jev.On = func(config.JevJob) bool { return false } },
		"no jev":   func(f *triageFixture) { f.sess.jev = nil },
	}
	for name, mutate := range cases {
		f := newTriage(t, &repairDecider{p: 0}, report(errRow("a")))
		mutate(f)
		f.edit(t)
		got := f.edit(t)
		if strings.Contains(got, "[harness:") {
			t.Errorf("%s: a note was added: %q", name, got)
		}
		if f.store != nil && len(f.items(t)) != 0 {
			t.Errorf("%s: an item was filed", name)
		}
	}
}

func TestTriageReusesAnExistingItemForTheSameFile(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0}, report(errRow("a")))
	if _, _, err := f.store.Add(kanban.ItemInput{Title: "LSP errors remain in pkg/main.go", List: kanban.Review}, f.sess.kanban.base.Source.Get()); err != nil {
		t.Fatal(err)
	}
	f.edit(t)
	got := f.edit(t)
	items := f.items(t)
	if len(items) != 1 || !strings.Contains(got, items[0].Short()) {
		t.Fatalf("items %d, note %q", len(items), got)
	}
}

func TestTriageStartsOverWhenTheFiledErrorsChangeShape(t *testing.T) {
	f := newTriage(t, &repairDecider{p: 0}, report(errRow("a")), report(errRow("a")), report(errRow("a")), report(errRow("brand new")))
	f.edit(t)
	f.edit(t) // filed
	f.edit(t) // same errors: note only
	fresh := f.edit(t)
	if strings.Contains(fresh, "filed on the board") {
		t.Fatalf("new errors were reported as already filed: %q", fresh)
	}
}

func TestErrorSignatureIgnoresPositions(t *testing.T) {
	a := []lsp.Row{{Line: 1, Source: "s", Code: "c", Message: "m"}, {Line: 9, Source: "s", Code: "d", Message: "n"}}
	b := []lsp.Row{{Line: 40, Source: "s", Code: "d", Message: "n"}, {Line: 2, Source: "s", Code: "c", Message: "m"}}
	if errorSignature(a) != errorSignature(b) {
		t.Fatal("moving code changed the signature")
	}
	if errorSignature(a) == errorSignature(a[:1]) {
		t.Fatal("different error sets share a signature")
	}
}

func TestTriageRecordsCountsOnly(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.AddSink(func(a rolemanager.Activity) {
		if a.Event == rolemanager.EventLSPTriage {
			got = append(got, a)
		}
	})
	defer cancel()
	f := newTriage(t, &repairDecider{p: 0.1}, report(errRow("secret message")))
	f.edit(t)
	f.edit(t)
	if len(got) != 1 || got[0].Verdict != "filed" || !strings.Contains(got[0].Detail, "attempts=2 errors=1 score=10") {
		t.Fatalf("recorded = %+v", got)
	}
	if strings.Contains(got[0].Detail+got[0].Subject, "secret") || strings.Contains(got[0].Detail+got[0].Subject, "main.go") {
		t.Fatalf("a message or path was recorded: %+v", got[0])
	}
}
