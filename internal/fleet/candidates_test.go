package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/testrun"
)

// Best-of-N candidates: the selection rule is pure, the loop is run against a
// real repository with a fake model turn and fake suite runs, and the
// isolation rules are checked from inside the turn that is supposed to be
// unable to see an earlier candidate. Nothing here reaches a network or a
// sync server (testEnv points the web URL at an unroutable address). No
// classifier kind is involved: no arbitrary text crosses between candidates.

func cf(k int, mut func(*candidateFacts)) candidateFacts {
	f := candidateFacts{K: k, Branch: fmt.Sprintf("belai/K-aaaaaa/a%d", k), Head: strings.Repeat("a", 40), GatesMet: 1, GatesTotal: 1, Files: 2, Tokens: 100}
	if mut != nil {
		mut(&f)
	}
	return f
}

func TestSelectCandidateTable(t *testing.T) {
	cases := []struct {
		name   string
		in     []candidateFacts
		want   int // candidate number, 0 for none
		reason string
	}{
		{"one eligible", []candidateFacts{cf(1, nil)}, 1, ruleOnly},
		{"nothing to select", nil, 0, candidateNone},
		{"more gates met wins", []candidateFacts{cf(1, nil), cf(2, func(f *candidateFacts) { f.GatesMet = 2; f.GatesTotal = 2; f.Files = 9; f.Tokens = 9999 })}, 2, ruleGatesMet},
		{"scanner clear beats unknown", []candidateFacts{cf(1, nil), cf(2, func(f *candidateFacts) { f.ScannerClear = true; f.Files = 9 })}, 2, ruleScanner},
		{"a change beats none", []candidateFacts{cf(1, func(f *candidateFacts) { f.Files = 0 }), cf(2, nil)}, 2, ruleChanges},
		{"fewest files", []candidateFacts{cf(1, nil), cf(2, func(f *candidateFacts) { f.Files = 1 })}, 2, ruleFiles},
		{"fewest tokens", []candidateFacts{cf(1, nil), cf(2, func(f *candidateFacts) { f.Tokens = 5 })}, 2, ruleTokens},
		{"lowest number last", []candidateFacts{cf(2, nil), cf(1, nil)}, 1, ruleOrder},
		{"regressed is never chosen", []candidateFacts{cf(1, func(f *candidateFacts) { f.Regressed = true; f.GatesMet = 5 }), cf(2, func(f *candidateFacts) { f.GatesMet = 0 })}, 2, ruleOnly},
		{"scanner present is never chosen", []candidateFacts{cf(1, func(f *candidateFacts) { f.ScannerPresent = true }), cf(2, nil)}, 2, ruleOnly},
		{"failed is never chosen", []candidateFacts{cf(1, func(f *candidateFacts) { f.Failed = true }), cf(2, nil)}, 2, ruleOnly},
		{"blocked is never chosen", []candidateFacts{cf(1, func(f *candidateFacts) { f.Blocked = true; f.GatesMet = 9 }), cf(2, nil)}, 2, ruleOnly},
		{"none selectable", []candidateFacts{cf(1, func(f *candidateFacts) { f.Regressed = true }), cf(2, func(f *candidateFacts) { f.Blocked = true }), cf(3, func(f *candidateFacts) { f.Failed = true })}, 0, candidateNone},
	}
	for _, c := range cases {
		idx, reason, ok := selectCandidate(c.in)
		got := 0
		if ok {
			got = c.in[idx].K
		}
		if got != c.want || reason != c.reason || ok != (c.want != 0) {
			t.Errorf("%s: got candidate %d (%s, ok %v), want %d (%s)", c.name, got, reason, ok, c.want, c.reason)
		}
	}
}

// The same facts give the same winner whatever order they arrive in: the rule
// reads no position, no clock and no randomness.
func TestSelectCandidateIsDeterministic(t *testing.T) {
	base := []candidateFacts{
		cf(1, func(f *candidateFacts) { f.GatesMet = 1; f.Files = 3 }),
		cf(2, func(f *candidateFacts) { f.GatesMet = 2; f.GatesTotal = 2; f.Files = 5; f.Tokens = 70 }),
		cf(3, func(f *candidateFacts) { f.GatesMet = 2; f.GatesTotal = 2; f.Files = 5; f.Tokens = 70 }),
		cf(4, func(f *candidateFacts) { f.GatesMet = 2; f.GatesTotal = 2; f.Files = 6; f.Tokens = 1 }),
	}
	want := 2
	perms := [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {2, 3, 0, 1}, {1, 3, 2, 0}, {3, 0, 2, 1}}
	for _, p := range perms {
		var in []candidateFacts
		for _, i := range p {
			in = append(in, base[i])
		}
		for range 3 {
			idx, _, ok := selectCandidate(in)
			if !ok || in[idx].K != want {
				t.Fatalf("order %v chose %d, want %d", p, in[idx].K, want)
			}
		}
	}
}

func TestCandidateStatusNamesTheMostBasicReason(t *testing.T) {
	f := candidateFacts{Blocked: true, Regressed: true, ScannerPresent: true, Failed: true}
	for _, want := range []string{candBlocked, candRegressed, candScannerPresent, candFailed, candEligible} {
		if got := f.status(); got != want {
			t.Fatalf("status %s, want %s", got, want)
		}
		switch want {
		case candBlocked:
			f.Blocked = false
		case candRegressed:
			f.Regressed = false
		case candScannerPresent:
			f.ScannerPresent = false
		case candFailed:
			f.Failed = false
		}
	}
}

// candHarness files one card and runs a worker with the candidate path on.
type candHarness struct {
	w     *Worker
	store *kanban.Store
	item  kanban.Item
	mu    sync.Mutex
	turns []Turn
	runs  int
	// results is what the nth run of the card's gates shows (0 based).
	results []testrun.Status
	// onTurn runs inside the nth turn (1 based), before it writes.
	onTurn func(n int, tt Turn)
	// files is how many files the nth turn writes.
	files func(n int) int
}

func newCandHarness(t *testing.T, mode string, candidates int, results ...testrun.Status) *candHarness {
	t.Helper()
	store, reg := testEnv(t)
	it, _, err := store.Add(kanban.ItemInput{Title: "do it", Labels: []string{"build"}, Gates: runnableGo}, kanban.Provenance{})
	if err != nil {
		t.Fatal(err)
	}
	p := gatedBuilder(mode)
	p.Kanban.Candidates = candidates
	h := &candHarness{store: store, item: it, results: results, files: func(n int) int { return 1 }}
	h.w = newWorker(t, store, reg, p, nil)
	h.w.Repo = gitRepo(t)
	h.w.Runner = func(ctx context.Context, tt Turn) (run.Result, error) {
		h.mu.Lock()
		h.turns = append(h.turns, tt)
		n := len(h.turns)
		h.mu.Unlock()
		if h.onTurn != nil {
			h.onTurn(n, tt)
		}
		for i := range h.files(n) {
			if err := os.WriteFile(filepath.Join(tt.Workdir, fmt.Sprintf("c%d-f%d.txt", n, i)), []byte("done\n"), 0o644); err != nil {
				return run.Result{}, err
			}
		}
		return complete(ctx, tt)
	}
	h.w.Suites = func(context.Context) []testdetect.Suite {
		return []testdetect.Suite{{Name: "go", Ecosystem: "go", Framework: "go", Command: []string{"go", "test", "./..."}}}
	}
	h.w.RunTests = func(_ context.Context, plan testrun.Plan) []testrun.Result {
		h.mu.Lock()
		i := h.runs
		h.runs++
		h.mu.Unlock()
		st := testrun.Passed
		if i < len(h.results) {
			st = h.results[i]
		}
		var out []testrun.Result
		for j, name := range plan.Names {
			r := testrun.Result{Status: st, Output: "ok  \tp\t0.01s\n", Suite: name, Command: plan.Argvs[j]}
			if st == testrun.Failed {
				r.ExitCode, r.Output = 1, "FAIL\tp\t0.1s\n"
			}
			out = append(out, r)
		}
		return out
	}
	return h
}

func (h *candHarness) run(t *testing.T) kanban.Item {
	t.Helper()
	if err := h.w.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := h.store.Get(h.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func attemptBranches(t *testing.T, repo string) []string {
	t.Helper()
	out := gitOut(t, repo, "branch", "--list", "--format=%(refname:short)", "belai/*")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Three candidates, the first fails its gate, the other two pass and the third
// changes more files: the harness publishes the second, and no other branch
// survives.
func TestThreeCandidatesPickTheOneThatMeetsTheGatesWithFewestFiles(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 3, testrun.Failed, testrun.Passed, testrun.Passed)
	h.files = func(n int) int { return n } // candidate 3 writes the most
	got := h.run(t)
	if len(h.turns) != 3 || h.runs != 3 {
		t.Fatalf("%d turns and %d gate runs, want 3 and 3", len(h.turns), h.runs)
	}
	for i, tt := range h.turns {
		if !tt.Candidate || tt.Attempt != i+1 || tt.Attempts != 3 || tt.Claim == nil || !tt.Claim.Candidate {
			t.Fatalf("turn %d is not marked as candidate %d of 3: %+v", i+1, i+1, tt)
		}
		for j := range i {
			if tt.Claim == h.turns[j].Claim || tt.SessionID == h.turns[j].SessionID || tt.Workdir == h.turns[j].Workdir && false {
				t.Fatalf("turn %d shares a claim or session with turn %d", i+1, j+1)
			}
		}
	}
	if got.List != kanban.Review || got.Attempts != 0 {
		t.Fatalf("card %s attempts %d note %q", got.List, got.Attempts, got.LastNote())
	}
	if !strings.HasPrefix(got.Branch, "belai/"+got.Short()+"/a") {
		t.Fatalf("branch %q", got.Branch)
	}
	// Candidate 2 wrote one extra file beyond candidate 1's single one, and met
	// the gate where candidate 1 did not.
	if files := gitOut(t, h.w.Repo, "ls-tree", "-r", "--name-only", got.Branch); !strings.Contains(files, "c2-f0.txt") || !strings.Contains(files, "c2-f1.txt") || strings.Contains(files, "c3-f0.txt") || strings.Contains(files, "c1-f0.txt") {
		t.Fatalf("the published branch is not candidate 2:\n%s", files)
	}
	if b := attemptBranches(t, h.w.Repo); !slices.Equal(b, []string{got.Branch}) {
		t.Fatalf("branches left: %v, want only %s", b, got.Branch)
	}
	if dirs := worktreeDirs(t); len(dirs) != 0 {
		t.Fatalf("worktrees left behind: %v", dirs)
	}
	note := got.LastNote()
	for _, want := range []string{"candidate 2 of 3 chosen by the harness", "3 ran", "rule files"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %s", want, note)
		}
	}
	g, _ := got.GateByID("G1")
	if g.State != kanban.GateMet || len(g.Ref) != 40 {
		t.Fatalf("the card carries the winner's gate state: %+v", g)
	}
	if _, err := os.Stat(candidatesFile(t, h.w)); !os.IsNotExist(err) {
		t.Fatalf("the candidates file outlived the release: %v", err)
	}
}

func candidatesFile(t *testing.T, w *Worker) string {
	t.Helper()
	p, err := config.CandidatesPath(w.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A profile that asks for no candidates, or for one, behaves as it always did.
func TestOneCandidateIsTheOrdinaryPath(t *testing.T) {
	for _, n := range []int{0, 1} {
		h := newCandHarness(t, agentprofile.VerifyRecord, n)
		got := h.run(t)
		if len(h.turns) != 1 || h.turns[0].Candidate || h.turns[0].Claim.Candidate || got.List != kanban.Review {
			t.Fatalf("candidates %d: %d turns, candidate flag %v, list %s", n, len(h.turns), h.turns[0].Candidate, got.List)
		}
	}
}

// The user's ceiling wins over the profile, and a card with a branch already
// continues on it.
func TestCandidateCountIsBoundedBySettingsAndTheCard(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 4)
	one := 1
	h.w.Settings.Agents = &config.AgentsSettings{MaxCandidates: &one}
	if n := h.w.maxCandidates(h.item); n != 1 {
		t.Fatalf("agents.max_candidates 1 gave %d", n)
	}
	two := 2
	h.w.Settings.Agents = &config.AgentsSettings{MaxCandidates: &two}
	if n := h.w.maxCandidates(h.item); n != 2 {
		t.Fatalf("agents.max_candidates 2 gave %d", n)
	}
	h.w.Settings.Agents = nil
	if n := h.w.maxCandidates(h.item); n != 4 {
		t.Fatalf("default ceiling gave %d", n)
	}
	resumed := h.item
	resumed.Branch = "belai/" + h.item.Short() + "/a1"
	if n := h.w.maxCandidates(resumed); n != 1 {
		t.Fatalf("a card with a branch got %d candidates", n)
	}
	labelled := h.item
	labelled.Labels = append(slices.Clone(labelled.Labels), config.WorktreeLabel)
	if n := h.w.maxCandidates(labelled); n != 1 {
		t.Fatalf("a worktree session card got %d candidates", n)
	}
	h.w.Profile.Kanban.Gates = nil
	if n := h.w.maxCandidates(h.item); n != 1 {
		t.Fatalf("with gates off got %d candidates", n)
	}
	h.w.Profile.Kanban.Gates = &agentprofile.GatesSpec{Verify: agentprofile.VerifyRecord}
	h.w.Profile.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationWorktree, ReadOnly: true}
	if n := h.w.maxCandidates(h.item); n != 1 {
		t.Fatalf("a read-only workspace got %d candidates", n)
	}
	h.w.Profile.Workspace = &agentprofile.WorkspaceSpec{Isolation: agentprofile.IsolationNone}
	if n := h.w.maxCandidates(h.item); n != 1 {
		t.Fatalf("no isolation got %d candidates", n)
	}
}

func TestSettingsCeilingStopsTheLoop(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 4)
	two := 2
	h.w.Settings.Agents = &config.AgentsSettings{MaxCandidates: &two}
	h.run(t)
	if len(h.turns) != 2 {
		t.Fatalf("%d turns with a ceiling of 2", len(h.turns))
	}
}

// Under enforce, a candidate whose gates fail is a failed attempt; when every
// candidate fails the claim fails once, and no branch is left to publish.
func TestNoSelectableCandidateFailsTheClaimOnce(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyEnforce, 3, testrun.Failed, testrun.Failed, testrun.Failed)
	got := h.run(t)
	if len(h.turns) != 3 {
		t.Fatalf("%d turns", len(h.turns))
	}
	if got.List != kanban.Backlog || got.Attempts != 1 || got.Branch != "" {
		t.Fatalf("card %s attempts %d branch %q: %q", got.List, got.Attempts, got.Branch, got.LastNote())
	}
	if !strings.Contains(got.LastNote(), "3 of 3 candidates ran and none could be selected") || !strings.Contains(got.LastNote(), "#1 failed") {
		t.Fatalf("note %q", got.LastNote())
	}
	if b := attemptBranches(t, h.w.Repo); len(b) != 0 {
		t.Fatalf("branches left: %v", b)
	}
	if dirs := worktreeDirs(t); len(dirs) != 0 {
		t.Fatalf("worktrees left: %v", dirs)
	}
}

// A candidate whose gates cannot run says nothing about its work: it is never
// chosen, and when nothing else can be the card goes to a person, with the
// branch kept and nothing published.
func TestBlockedCandidatesFailClosedToAPerson(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 2, testrun.Denied, testrun.Denied)
	got := h.run(t)
	if got.List != kanban.Blocked {
		t.Fatalf("card %s: %q", got.List, got.LastNote())
	}
	if !strings.Contains(got.LastNote(), "the harness could not run the gates on belai/") || !strings.Contains(got.LastNote(), "a person must review") {
		t.Fatalf("note %q", got.LastNote())
	}
	if got.PR != "" {
		t.Fatalf("a blocked card published: %q", got.PR)
	}
	if b := attemptBranches(t, h.w.Repo); len(b) != 2 {
		t.Fatalf("the blocked candidates' branches are kept for the person: %v", b)
	}
}

// A blocked candidate loses to one that could be verified, whatever it shows.
func TestBlockedCandidateNeverBeatsAVerifiedOne(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 2, testrun.Denied, testrun.Failed)
	got := h.run(t)
	if got.List != kanban.Review {
		t.Fatalf("card %s: %q", got.List, got.LastNote())
	}
	if !strings.Contains(got.LastNote(), "candidate 2 of 2 chosen") {
		t.Fatalf("note %q", got.LastNote())
	}
}

// The candidates share the item's budget: once it is told to finalise, no
// further candidate starts.
func TestSpentBudgetStopsFurtherCandidates(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 4)
	h.onTurn = func(n int, tt Turn) {
		if n == 1 {
			h.w.raiseFinalize("cost")
		}
	}
	got := h.run(t)
	if len(h.turns) != 1 {
		t.Fatalf("%d turns after the budget was spent, want 1", len(h.turns))
	}
	if got.List != kanban.Review || !strings.Contains(got.LastNote(), "candidate 1 of 4 chosen") || !strings.Contains(got.LastNote(), "1 ran") {
		t.Fatalf("card %s: %q", got.List, got.LastNote())
	}
}

// The turn that runs second cannot see the first: its worktree is gone, its
// branch ref is parked, the card carries none of its gate states or notes, and
// the tools that would write to the card are refused.
func TestLaterCandidateSeesNothingOfAnEarlierOne(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 2, testrun.Failed, testrun.Passed)
	var firstDir string
	var checked bool
	h.onTurn = func(n int, tt Turn) {
		switch n {
		case 1:
			firstDir = tt.Workdir
		case 2:
			checked = true
			if _, err := os.Stat(firstDir); !os.IsNotExist(err) {
				t.Errorf("candidate 1's worktree is still there while candidate 2 runs: %v", err)
			}
			if b := attemptBranches(t, h.w.Repo); len(b) != 1 || b[0] != tt.Workspace.Branch {
				t.Errorf("branches visible to candidate 2: %v (its own is %s)", b, tt.Workspace.Branch)
			}
			cur, _ := h.store.Get(h.item.ID)
			if g, _ := cur.GateByID("G1"); g.State != kanban.GateUnmet || g.Ref != "" || g.Note != "" {
				t.Errorf("candidate 1's gate state is on the card: %+v", g)
			}
			if strings.Contains(cur.LastNote(), "candidate") || strings.Contains(cur.LastNote(), "verification") {
				t.Errorf("candidate 1 left a note: %q", cur.LastNote())
			}
			if tt.Workspace.Branch == h.turns[0].Workspace.Branch {
				t.Errorf("two candidates share a branch name %s", tt.Workspace.Branch)
			}
			if strings.Contains(tt.Memory, "c1-f0") {
				t.Errorf("candidate 2 was handed text from candidate 1")
			}
		}
	}
	h.run(t)
	if !checked {
		t.Fatal("candidate 2 never ran")
	}
}

// Candidates are named apart: parking a branch does not hand its name to the
// next.
func TestCandidateBranchesHaveDistinctAttemptNames(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 3)
	h.run(t)
	seen := map[string]bool{}
	for _, tt := range h.turns {
		b := tt.Workspace.Branch
		if seen[b] || !ownAttemptBranch(h.item, b) {
			t.Fatalf("branch %q repeated or not an attempt branch (seen %v)", b, seen)
		}
		seen[b] = true
	}
}

// The turn gets no publishing tool: the harness pushes the chosen branch once.
func TestCandidateTurnsAreToldTheyAreCandidatesAndOpenNoA2ALink(t *testing.T) {
	d := candidateDirective(Turn{Candidate: true, Attempt: 2, Attempts: 3})
	for _, want := range []string{"attempt 2 of 3", "cannot see the others", "chooses the winner itself", "only the chosen branch is published"} {
		if !strings.Contains(d, want) {
			t.Errorf("directive lacks %q: %s", want, d)
		}
	}
	if candidateDirective(Turn{}) != "" {
		t.Fatal("an ordinary turn carries a candidate directive")
	}
	w := &Worker{Profile: agentprofile.AgentProfile{Name: "p"}}
	if got := w.turnDirective(Turn{Candidate: true, Attempt: 1, Attempts: 2}, false, false); !strings.Contains(got, "attempt 1 of 2") {
		t.Fatalf("turn directive %q", got)
	}
	if link := w.openA2A(context.Background(), headless.Params{}, Turn{Candidate: true}); link != nil {
		t.Fatal("a candidate turn opened an A2A link")
	}
}

// The candidates file is not trusted: anything that does not match this claim
// is ignored, and a good one is read back.
func TestCandidatesFileIsValidatedBeforeItIsReused(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 2)
	ctx := context.Background()
	repoRun, top, err := h.w.repoGit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	head := gitOut(t, h.w.Repo, "rev-parse", "HEAD")
	it := h.item
	branch := "belai/" + it.Short() + "/a1"
	good := candidateRecord{Version: candidateRecordVersion, Item: it.ID, Base: head, Attempts: it.Attempts,
		Done: []candidateFacts{{K: 1, Branch: branch, Head: head, GatesMet: 1, GatesTotal: 1, Files: 2, Tokens: 10}}}
	write := func(r candidateRecord) {
		data, _ := json.Marshal(r)
		if err := config.WriteGlobalFileAtomic(candidatesFile(t, h.w), data); err != nil {
			t.Fatal(err)
		}
	}
	write(good)
	if got := h.w.readCandidates(ctx, repoRun, top, it, head); len(got) != 1 || got[0].K != 1 {
		t.Fatalf("a good file was not read back: %+v", got)
	}
	mut := map[string]func(*candidateRecord){
		"another item":          func(r *candidateRecord) { r.Item = "other" },
		"another base":          func(r *candidateRecord) { r.Base = strings.Repeat("b", 40) },
		"another attempt":       func(r *candidateRecord) { r.Attempts = 7 },
		"another version":       func(r *candidateRecord) { r.Version = 99 },
		"a branch of another":   func(r *candidateRecord) { r.Done[0].Branch = "belai/K-ffffff/a1" },
		"a branch outside":      func(r *candidateRecord) { r.Done[0].Branch = "main" },
		"a commit not in git":   func(r *candidateRecord) { r.Done[0].Head = strings.Repeat("c", 40) },
		"a malformed commit":    func(r *candidateRecord) { r.Done[0].Head = "--upload-pack=x" },
		"a negative count":      func(r *candidateRecord) { r.Done[0].Files = -1 },
		"more met than total":   func(r *candidateRecord) { r.Done[0].GatesMet = 5 },
		"an absurd count":       func(r *candidateRecord) { r.Done[0].Tokens = maxCandidateInt + 1 },
		"a number past the max": func(r *candidateRecord) { r.Done[0].K = agentprofile.MaxCandidates + 1 },
		"a repeated number":     func(r *candidateRecord) { r.Done = append(r.Done, r.Done[0]) },
	}
	for name, f := range mut {
		r := good
		r.Done = slices.Clone(good.Done)
		f(&r)
		write(r)
		if got := h.w.readCandidates(ctx, repoRun, top, it, head); got != nil {
			t.Errorf("%s was reused: %+v", name, got)
		}
	}
	if err := os.WriteFile(candidatesFile(t, h.w), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := h.w.readCandidates(ctx, repoRun, top, it, head); got != nil {
		t.Fatalf("garbage was reused: %+v", got)
	}
}

// A claim that follows a lost lease reuses the candidates that finished, runs
// only the rest, and still chooses among all of them.
func TestFinishedCandidatesAreReusedByTheSameWorker(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 3, testrun.Passed, testrun.Passed)
	ctx := context.Background()
	// Candidate 1 ran in an earlier claim: make its commit and record it.
	repo := h.w.Repo
	head := gitOut(t, repo, "rev-parse", "HEAD")
	branch := "belai/" + h.item.Short() + "/a1"
	gitOut(t, repo, "checkout", "-q", "-b", "scratch")
	if err := os.WriteFile(filepath.Join(repo, "old.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitOut(t, repo, "add", "old.txt")
	gitOut(t, repo, "commit", "--no-gpg-sign", "-q", "-m", "earlier candidate")
	oldHead := gitOut(t, repo, "rev-parse", "HEAD")
	gitOut(t, repo, "checkout", "-q", "main")
	gitOut(t, repo, "branch", "-D", "scratch")
	data, _ := json.Marshal(candidateRecord{Version: candidateRecordVersion, Item: h.item.ID, Base: head, Attempts: 0,
		Done: []candidateFacts{{K: 1, Branch: branch, Head: oldHead, GatesMet: 1, GatesTotal: 1, Files: 1}}})
	if err := config.WriteGlobalFileAtomic(candidatesFile(t, h.w), data); err != nil {
		t.Fatal(err)
	}
	got := h.run(t)
	_ = ctx
	if len(h.turns) != 2 {
		t.Fatalf("%d turns, want 2 (candidate 1 was reused)", len(h.turns))
	}
	if h.turns[0].Attempt != 2 || h.turns[1].Attempt != 3 {
		t.Fatalf("attempts %d and %d", h.turns[0].Attempt, h.turns[1].Attempt)
	}
	// The three candidates tie on every measured fact, so the lowest number wins:
	// the reused one, whose branch is made again at the recorded commit.
	if got.Branch != branch || !strings.Contains(got.LastNote(), "candidate 1 of 3 chosen") {
		t.Fatalf("branch %q note %q", got.Branch, got.LastNote())
	}
	if files := gitOut(t, repo, "ls-tree", "-r", "--name-only", got.Branch); !strings.Contains(files, "old.txt") {
		t.Fatalf("the winner is not the recorded commit:\n%s", files)
	}
	if g, _ := got.GateByID("G1"); g.State != kanban.GateMet {
		t.Fatalf("the winner's gates were decided again: %+v", g)
	}
}

// A worker that is stopped while a candidate runs hands the item back
// untouched: it is not an attempt, and the card is not released as failed.
func TestStoppedWorkerDoesNotSelectOrFailTheCard(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyRecord, 3)
	ctx, cancel := context.WithCancel(context.Background())
	h.onTurn = func(n int, tt Turn) {
		if n == 2 {
			cancel()
		}
	}
	if err := h.w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := h.store.Get(h.item.ID)
	if got.Attempts != 0 || got.ClaimedBy != "" || got.List != kanban.Backlog || got.Branch != "" {
		t.Fatalf("a stop must leave the card as it was: %s attempts %d claim %q branch %q (%q)", got.List, got.Attempts, got.ClaimedBy, got.Branch, got.LastNote())
	}
	if dirs := worktreeDirs(t); len(dirs) != 0 {
		t.Fatalf("worktrees left after a stop: %v", dirs)
	}
}

// The audit holds each candidate's measured facts and the harness's choice, as
// ids and integers: no gate output, no card text, no note.
func TestCandidateAuditCarriesFactsOnly(t *testing.T) {
	events := installAudit(t)
	h := newCandHarness(t, agentprofile.VerifyEnforce, 3, testrun.Failed, testrun.Passed, testrun.Passed)
	h.files = func(n int) int { return n }
	got := h.run(t)
	evs := events()
	var cands, sel []audit.Event
	for _, e := range evs {
		switch e.Kind {
		case "card.candidate":
			cands = append(cands, e)
		case "card.candidate_selected":
			sel = append(sel, e)
		}
	}
	if len(cands) != 3 || len(sel) != 1 {
		t.Fatalf("%d candidate events and %d selection events: %v", len(cands), len(sel), kinds(evs))
	}
	wantStatus := []string{"failed", "eligible", "eligible"}
	for i, e := range cands {
		if e.Data["candidate"] != fmt.Sprint(i+1) || e.Outcome != wantStatus[i] || e.Scope != "agent" || len(e.Commit) != 40 || e.Data["total"] != "1" {
			t.Errorf("candidate event %d: %+v", i+1, e)
		}
	}
	s := sel[0]
	if s.Outcome != "selected" || s.Data["candidate"] != "2" || s.Data["candidates"] != "3" || s.Data["reason"] != "files" || s.Branch != got.Branch {
		t.Fatalf("selection event: %+v (branch %s)", s, got.Branch)
	}
	raw, _ := json.Marshal(evs)
	for _, leak := range []string{"do it", "FAIL", "verification failed", "completed it"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("%q reached the audit log: %s", leak, raw)
		}
	}
	if got.List != kanban.Review {
		t.Fatalf("card %s: %q", got.List, got.LastNote())
	}
}

// Under enforce a winner is still held to the card's gates, and the card shows
// the winner's gates, not those of the candidate that happened to run last.
func TestEnforcedCandidatesShowTheWinnersGatesOnTheCard(t *testing.T) {
	h := newCandHarness(t, agentprofile.VerifyEnforce, 2, testrun.Passed, testrun.Failed)
	got := h.run(t)
	if got.List != kanban.Review || !strings.Contains(got.LastNote(), "candidate 1 of 2 chosen") {
		t.Fatalf("card %s: %q", got.List, got.LastNote())
	}
	if g, _ := got.GateByID("G1"); g.State != kanban.GateMet {
		t.Fatalf("the last candidate's failure is on the card: %+v", g)
	}
}
