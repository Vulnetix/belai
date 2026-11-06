package fleet

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/a2alink"
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
)

// Best-of-N candidate attempts (kanban.candidates, docs/fleet.md).
//
// A worker whose profile asks for it works a fresh card as up to N independent
// candidates, one after another, each in its own worktree on its own branch,
// with its own session and claim. The harness runs the card's gates on every
// candidate branch and picks the winner by a fixed rule over numbers it
// measured (selectCandidate). A model never ranks, scores or picks, and no
// model reads another candidate's text.
//
// Isolation is kept by what the harness delivers and withholds:
//   - a candidate's worktree is removed and its branch parked (the ref is
//     deleted, the commit id is kept) before the next candidate starts, so a
//     later candidate finds neither files nor a branch to read;
//   - the claim of a candidate refuses the tools that write this attempt's text
//     to the card (tools.WorkerClaim.Candidate), and the gate states are held
//     back and written for the winner alone (verifyBranchAs);
//   - a candidate turn has no publishing tool and no A2A link;
//   - the lessons the profile's memory attaches are read once, before the first
//     candidate, and written after the winner alone.
//
// Nothing a candidate wrote is composed into a note, a record or an audit
// event: the card's notes, the candidates file and the audit hold branch names,
// gate ids and integers only. No classifier kind is added, because no arbitrary
// text crosses.

// candidateEnv is what work hands the candidate path from its own scope: the
// turn's event sink, the budget accounting and the lease renewal.
type candidateEnv struct {
	emit      func(agent.Event)
	nextRound func()
	addTokens func()
	stopRenew func()
	tokens    func() int
}

// candidateFacts are the numbers and flags the harness measured for one
// candidate. They are all selectCandidate reads, and all the candidates file
// holds besides identifiers.
type candidateFacts struct {
	K      int    `json:"k"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	// Failed is a candidate the harness judged a failed attempt: the turn did
	// not complete, the gates failed under enforce, or the scanner still reports
	// the finding. Blocked is a candidate whose gates could not be run at all, so
	// nothing is known about it. Regressed and ScannerPresent are measured.
	Failed         bool `json:"failed,omitempty"`
	Blocked        bool `json:"blocked,omitempty"`
	Regressed      bool `json:"regressed,omitempty"`
	ScannerPresent bool `json:"scanner_present,omitempty"`
	ScannerClear   bool `json:"scanner_clear,omitempty"`
	GatesMet       int  `json:"gates_met"`
	GatesTotal     int  `json:"gates_total"`
	Files          int  `json:"files"`
	Tokens         int  `json:"tokens"`
}

// Candidate statuses, the enum the audit and the card note carry.
const (
	candEligible       = "eligible"
	candFailed         = "failed"
	candBlocked        = "blocked"
	candRegressed      = "regressed"
	candScannerPresent = "scanner_present"
)

// status names why a candidate is or is not selectable, the most basic reason
// first: an unverifiable candidate says nothing, then a measured regression or
// a finding the scanner still reports, then a failed turn.
func (f candidateFacts) status() string {
	switch {
	case f.Blocked:
		return candBlocked
	case f.Regressed:
		return candRegressed
	case f.ScannerPresent:
		return candScannerPresent
	case f.Failed:
		return candFailed
	}
	return candEligible
}

// selectable reports whether the candidate may win.
func (f candidateFacts) selectable() bool { return f.status() == candEligible }

// Selection rules, in order. The reason a winner is reported with is the first
// rule that separated it from the runner-up.
const (
	ruleOnly      = "only"
	ruleGatesMet  = "gates_met"
	ruleScanner   = "scanner"
	ruleChanges   = "changes"
	ruleFiles     = "files"
	ruleTokens    = "tokens"
	ruleOrder     = "order"
	candidateNone = "none"
)

// compareCandidates orders two selectable candidates, the better first (a
// negative result), and names the rule that decided it. The order is fixed:
// most gates met, then the scanner clear, then some change over none, then
// fewest files, then fewest tokens, then the lowest number. It reads measured
// integers and flags only.
func compareCandidates(a, b candidateFacts) (int, string) {
	if c := cmp.Compare(b.GatesMet, a.GatesMet); c != 0 {
		return c, ruleGatesMet
	}
	if c := cmp.Compare(boolInt(b.ScannerClear), boolInt(a.ScannerClear)); c != 0 {
		return c, ruleScanner
	}
	if c := cmp.Compare(boolInt(b.Files > 0), boolInt(a.Files > 0)); c != 0 {
		return c, ruleChanges
	}
	if c := cmp.Compare(a.Files, b.Files); c != 0 {
		return c, ruleFiles
	}
	if c := cmp.Compare(a.Tokens, b.Tokens); c != 0 {
		return c, ruleTokens
	}
	return cmp.Compare(a.K, b.K), ruleOrder
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// selectCandidate picks the winner among the candidates: its index in cs, the
// rule that separated it from the runner-up, and false when none can be
// selected. It is pure and deterministic: the same facts always give the same
// answer, and a candidate that is blocked (unverifiable), regressed, still
// reported by the scanner or failed is never chosen.
func selectCandidate(cs []candidateFacts) (int, string, bool) {
	var idx []int
	for i, c := range cs {
		if c.selectable() {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return -1, candidateNone, false
	}
	slices.SortStableFunc(idx, func(i, j int) int {
		c, _ := compareCandidates(cs[i], cs[j])
		return c
	})
	if len(idx) == 1 {
		return idx[0], ruleOnly, true
	}
	_, rule := compareCandidates(cs[idx[0]], cs[idx[1]])
	return idx[0], rule, true
}

// candidate is one attempt's result while the loop runs.
type candidate struct {
	f candidateFacts
	// o is the attempt's outcome as the ordinary path would have released it.
	o outcome
	// res and err are the goal loop's result, for the winner's reflection.
	res     run.Result
	err     error
	session string
	// states are the gate states held back by the quiet verification.
	states []gateState
	// resumed is a candidate read back from the candidates file: it has no
	// states, and the winner's gates are decided again.
	resumed bool
	// cut is a candidate interrupted by a stop or a lost lease: its work is not a
	// result, and it is neither recorded nor chosen.
	cut bool
}

// maxCandidates is how many candidates this claim works: one unless the profile
// asks for more, agents.max_candidates allows it, and the card is a fresh item
// in a writable worktree with its gates verified. A card that already has a
// branch (a review, a later claim, a pull request head) continues on it as
// before.
func (w *Worker) maxCandidates(it kanban.Item) int {
	k := w.Profile.Kanban
	n := min(k.CandidateCount(), w.Settings.MaxCandidates())
	if n <= 1 || it.Branch != "" {
		return 1
	}
	p := w.Profile
	if p.IsolationMode() != agentprofile.IsolationWorktree || p.ReadOnlyWorkspace() || w.worktreeSession(it) || w.conflictMode(it) {
		return 1
	}
	if m := w.gatesMode(); m != agentprofile.VerifyRecord && m != agentprofile.VerifyEnforce {
		return 1
	}
	return n
}

// candidateDirective is the harness's note to a candidate turn: which attempt
// it is and what the harness, not the model, will do. Integers only.
func candidateDirective(t Turn) string {
	if !t.Candidate {
		return ""
	}
	return fmt.Sprintf("Candidates: this is attempt %d of %d independent attempts at this item, each in its own worktree and branch, and you cannot see the others. Work and commit as usual, then finish. The harness runs the card's gates on every branch and chooses the winner itself, and only the chosen branch is published. Notes, handoffs and verdicts are not recorded in an attempt, so leave what you did in your commits.", t.Attempt, t.Attempts)
}

// turnDirective is the directive a turn carries: the workspace note, and for a
// candidate attempt the candidate note after it.
func (w *Worker) turnDirective(t Turn, publish, openPRs bool) string {
	d := w.directive(t.Workspace, publish, openPRs)
	if cd := candidateDirective(t); cd != "" {
		d = strings.TrimSpace(d + " " + cd)
	}
	return d
}

// openA2A opens the turn's A2A link. A candidate attempt has none: the files it
// changes would otherwise be broadcast to the attempts after it.
func (w *Worker) openA2A(ctx context.Context, params headless.Params, t Turn) *a2alink.Link {
	if t.Candidate {
		return nil
	}
	return headless.OpenA2A(ctx, params, w.Profile.Name, t.Item.Short(), nil)
}

// finalizeRaised reports whether the item in hand was told its budget is nearly
// spent, which is the signal not to start another candidate.
func (w *Worker) finalizeRaised() bool {
	w.coordMu.Lock()
	defer w.coordMu.Unlock()
	return w.coord.finalized
}

// repoGit returns the hardened runner for the trusted repository and its root.
func (w *Worker) repoGit(ctx context.Context) (forge.Runner, string, error) {
	r := hardenedGit("", "")
	top, err := git(ctx, r, w.Repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", fmt.Errorf("candidates: %s is not a git repository: %w", w.Repo, err)
	}
	return hardenedGit("", "", identityEnv(ctx, r, top)...), top, nil
}

// candidateRecord is the candidates file: what the harness measured for the
// candidates that finished, so a claim of the same item by the same worker that
// follows a lost lease or a provider outage (neither costs an attempt) does not
// run them again. It holds identifiers and integers only.
type candidateRecord struct {
	Version  int              `json:"version"`
	Item     string           `json:"item"`
	Base     string           `json:"base"`
	Attempts int              `json:"attempts"`
	Done     []candidateFacts `json:"done"`
}

const candidateRecordVersion = 1

// maxCandidateInt bounds a count read back from the file, which is not trusted.
const maxCandidateInt = 1 << 40

// readCandidates returns the finished candidates recorded for this claim of the
// item, or nil. A file for another item, another base commit or another attempt
// number, a malformed one, and an entry whose commit the repository no longer
// holds are ignored: the candidates run again.
func (w *Worker) readCandidates(ctx context.Context, r forge.Runner, top string, it kanban.Item, base string) []candidateFacts {
	path, err := config.CandidatesPath(w.Record.ID)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var rec candidateRecord
	if json.Unmarshal(data, &rec) != nil || rec.Version != candidateRecordVersion || rec.Item != it.ID || rec.Base != base || rec.Attempts != it.Attempts {
		return nil
	}
	var out []candidateFacts
	last := 0
	for _, f := range rec.Done {
		head := kanban.CleanRef(f.Head)
		if f.K <= last || f.K > agentprofile.MaxCandidates || head == "" || head != f.Head || !ownAttemptBranch(it, f.Branch) ||
			f.GatesMet < 0 || f.GatesTotal < f.GatesMet || f.Files < 0 || f.Tokens < 0 ||
			f.GatesTotal > maxCandidateInt || f.Files > maxCandidateInt || f.Tokens > maxCandidateInt {
			return nil
		}
		if _, err := git(ctx, r, top, "cat-file", "-e", "--end-of-options", head+"^{commit}"); err != nil {
			return nil
		}
		last = f.K
		out = append(out, f)
	}
	return out
}

// writeCandidates records the finished candidates.
func (w *Worker) writeCandidates(it kanban.Item, base string, done []*candidate) {
	path, err := config.CandidatesPath(w.Record.ID)
	if err != nil {
		return
	}
	rec := candidateRecord{Version: candidateRecordVersion, Item: it.ID, Base: base, Attempts: it.Attempts}
	for _, c := range done {
		if !c.cut {
			rec.Done = append(rec.Done, c.f)
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := config.WriteGlobalFileAtomic(path, data); err != nil {
		w.logf("%s: candidates file: %v", it.Short(), err)
	}
}

// dropCandidates deletes the candidates file.
func (w *Worker) dropCandidates() {
	if path, err := config.CandidatesPath(w.Record.ID); err == nil {
		_ = os.Remove(path)
	}
}

// candidateLine is the harness-composed, integer-and-identifier account of one
// candidate, for the card note.
func candidateLine(f candidateFacts) string {
	return fmt.Sprintf("#%d %s (gates %d/%d, %d files, %d tokens)", f.K, f.status(), f.GatesMet, f.GatesTotal, f.Files, f.Tokens)
}

// parkBranch deletes the candidate's branch ref once its commit id is recorded,
// so a later candidate's git finds no branch to read. The commits stay in the
// object store, and the branch is made again for the winner.
func parkBranch(ctx context.Context, r forge.Runner, top, branch string) error {
	if !strings.HasPrefix(branch, BranchPrefix) {
		return fmt.Errorf("candidates: %q is not an attempt branch", branch)
	}
	_, err := git(ctx, r, top, "branch", "-D", branch)
	return err
}

// restoreBranch makes the branch again at the commit it was parked at.
func restoreBranch(ctx context.Context, r forge.Runner, top, branch, head string) error {
	if !strings.HasPrefix(branch, BranchPrefix) || kanban.CleanRef(head) != head {
		return fmt.Errorf("candidates: cannot restore %q at %q", branch, head)
	}
	_, err := git(ctx, r, top, "branch", branch, head)
	return err
}

// workCandidates works the item as up to n candidates and releases it. It is the
// whole of work for such an item after the claim: the loop, the choice, the
// release, the winner's publication and the cleanup.
func (w *Worker) workCandidates(ctx, itemCtx context.Context, it kanban.Item, n int, env candidateEnv) {
	bg := context.WithoutCancel(ctx)
	w.beginCoord(it, nil)
	repoRun, top, err := w.repoGit(bg)
	if err != nil {
		env.stopRenew()
		env.addTokens()
		if !w.stopped(ctx, itemCtx, it, nil) {
			w.release(ctx, it, outcome{failed: true, fault: Fault{Code: FaultWorkspace}, note: "the workspace could not be prepared: " + sanitize.Sanitize(err.Error())})
		}
		return
	}
	base, _ := git(bg, repoRun, top, "rev-parse", "--verify", "HEAD")

	memory := w.memory(ctx)
	var cands []*candidate
	for _, f := range w.readCandidates(bg, repoRun, top, it, base) {
		if f.K <= n {
			cands = append(cands, &candidate{f: f, resumed: true})
		}
	}
	if len(cands) > 0 {
		w.logf("%s: %d candidate(s) from an earlier claim are reused", it.Short(), len(cands))
	}

	var setupErr error
	for k := 1; k <= n; k++ {
		if slices.ContainsFunc(cands, func(c *candidate) bool { return c.f.K == k }) {
			continue
		}
		// Another candidate starts only while the item has budget left: the
		// candidates share the item's token, cost and wall budgets.
		if k > 1 && (itemCtx.Err() != nil || w.finalizeRaised()) {
			w.logf("%s: the item's budget is spent; %d candidate(s) were run", it.Short(), len(cands))
			break
		}
		c, stop, err := w.runCandidate(ctx, itemCtx, it, k, n, memory, repoRun, top, env)
		if err != nil {
			if k == 1 && len(cands) == 0 {
				setupErr = err
			} else {
				w.logf("%s: candidate %d: %v", it.Short(), k, err)
			}
			break
		}
		cands = append(cands, c)
		if !c.cut {
			w.writeCandidates(it, base, cands)
		}
		w.auditCandidate(it, c)
		if stop {
			break
		}
	}
	slices.SortFunc(cands, func(a, b *candidate) int { return cmp.Compare(a.f.K, b.f.K) })

	if setupErr != nil {
		// The environment, not the attempt, failed: the same setup-debug turn the
		// ordinary path runs, then the item goes back.
		claim := w.newClaim(itemCtx, it)
		note := w.setupFailureNote(itemCtx, it, claim, setupErr, env.emit)
		env.addTokens()
		env.stopRenew()
		w.dropCandidates()
		if w.stopped(ctx, itemCtx, it, nil) {
			return
		}
		w.release(ctx, it, outcome{failed: true, fault: Fault{Code: FaultWorkspace}, note: note})
		return
	}

	env.addTokens()
	if w.stopped(ctx, itemCtx, it, nil) {
		// A stop or a lost lease is not an attempt: what the finished candidates
		// measured stays in the candidates file for the next claim.
		return
	}

	facts := make([]candidateFacts, len(cands))
	for i, c := range cands {
		facts[i] = c.f
		if c.cut {
			facts[i].Failed = true
		}
	}
	win, reason, ok := selectCandidate(facts)
	if !ok {
		o := w.noCandidate(it, cands, n)
		// A blocked candidate is kept for a person: its branch is made again.
		for _, c := range cands {
			if c.f.Blocked && c.f.Head != "" {
				if err := restoreBranch(bg, repoRun, top, c.f.Branch, c.f.Head); err != nil {
					w.logf("%s: restore %s: %v", it.Short(), c.f.Branch, err)
				}
			}
		}
		w.auditSelected(it, cands, nil, candidateNone)
		env.stopRenew()
		w.releaseCandidates(ctx, it, o)
		if len(cands) > 0 {
			last := cands[len(cands)-1]
			w.reflect(ctx, it, last.res, last.err)
		}
		return
	}

	c := cands[win]
	ws, o := w.materialize(ctx, itemCtx, it, c, repoRun, top)
	w.auditSelected(it, cands, c, reason)
	// The lease is renewed until the winner is ready to release: making its
	// branch again and deciding its gates can take as long as a turn.
	env.stopRenew()
	w.finishWinner(ctx, it, c, ws, o, len(cands), n, reason)
}

// noCandidate is the outcome of a claim none of whose candidates can be
// selected. Every case is composed from counts, statuses and branch names. A
// candidate whose gates could not be run sends the card to a person; otherwise
// the claim is a failed attempt, carrying the last candidate's fault so a
// provider outage or a spent budget is treated as the ordinary path treats it.
func (w *Worker) noCandidate(it kanban.Item, cands []*candidate, n int) outcome {
	var lines []string
	blocked := false
	for _, c := range cands {
		lines = append(lines, candidateLine(c.f))
		blocked = blocked || c.f.Blocked
	}
	summary := fmt.Sprintf("%d of %d candidates ran and none could be selected: %s", len(cands), n, strings.Join(lines, "; "))
	if blocked {
		var kept []string
		for _, c := range cands {
			if c.f.Blocked {
				kept = append(kept, audit.Clean(c.f.Branch))
			}
		}
		return outcome{failed: true, blocked: true,
			note: summary + "; the harness could not run the gates on " + strings.Join(kept, ", ") + ", so a person must review them"}
	}
	if len(cands) == 0 {
		return outcome{failed: true, fault: Fault{Code: FaultIncomplete}, note: "no candidate ran"}
	}
	o := cands[len(cands)-1].o
	o.branch, o.files, o.verdict, o.to, o.addLabels, o.dropLabels = "", 0, "", "", nil, nil
	o.failed = true
	o.note = fmt.Sprintf("agent %s: %s", w.Profile.Name, summary)
	return o
}

// releaseCandidates releases the item with no winner and removes the file.
func (w *Worker) releaseCandidates(ctx context.Context, it kanban.Item, o outcome) {
	w.release(ctx, it, o)
	// A claim that ended for the provider's sake (an outage) or on a spent
	// budget keeps what was measured; any other end is final.
	if !o.transient {
		w.dropCandidates()
	}
}

// materialize makes the winner's branch again at its recorded commit and checks
// it out in a new worktree, then writes its gate states to the card (the
// states the quiet verification decided, or a verification now when they are
// not at hand or the branch moved on the way: a resumed candidate, a base
// update). A failure here is the winner's failure: the claim fails with no
// branch published.
func (w *Worker) materialize(ctx, itemCtx context.Context, it kanban.Item, c *candidate, repoRun forge.Runner, top string) (*Workspace, outcome) {
	bg := context.WithoutCancel(ctx)
	fail := func(note string) (*Workspace, outcome) {
		return nil, outcome{failed: true, fault: Fault{Code: FaultWorkspace}, note: note}
	}
	if err := restoreBranch(bg, repoRun, top, c.f.Branch, c.f.Head); err != nil {
		return fail("the chosen candidate's branch could not be made again: " + sanitize.Sanitize(err.Error()))
	}
	cit := it
	cit.Branch = c.f.Branch
	ws, err := w.workspace(ctx, cit)
	if err != nil {
		_, _ = git(bg, repoRun, top, "branch", "-D", c.f.Branch)
		return fail("the chosen candidate's workspace could not be prepared: " + sanitize.Sanitize(err.Error()))
	}
	w.Record.Branch, w.Record.Session = ws.Branch, c.session
	if c.session == "" {
		w.Record.Session = session.MustID()
	}
	w.save()
	o := c.o
	o.branch, o.files = ws.Branch, c.f.Files
	head, herr := ws.Head(bg)
	switch {
	case herr == nil && head == c.f.Head && !c.resumed:
		w.recordGates(it, c.states)
	case herr == nil && itemCtx.Err() == nil:
		// The gate states are not at hand (a candidate from an earlier claim) or
		// the branch moved (worktrees.update_mode): decide them on this tree.
		v := w.verifyBranch(itemCtx, it, ws)
		o = w.reverify(o, v)
	}
	if err := w.Store.SetBranch(it.ID, w.Record.ID, ws.Branch); err != nil {
		w.logf("%s: record the branch: %v", it.Short(), err)
	} else {
		headless.FlushKanban(w.Settings, w.Repo)
	}
	if ws.Worktree && w.coordinated() {
		ws.SetCoordinator(&Coordinator{
			Registry: w.Registry.Dir(), Worker: w.Record.ID, Item: it.ID, Title: it.Title,
			Note: func(note string) { _, _ = w.Store.Update(it.ID, kanban.Patch{Note: note}, w.Record.Session) },
		})
	}
	return ws, o
}

// reverify folds a verification run on the winner's tree into its outcome when
// the earlier states could not be used. A tree that now fails its gates under
// enforce is a failed claim; anything else only adds the harness's note.
func (w *Worker) reverify(o outcome, v gateVerdict) outcome {
	return w.applyVerification(o, v, w.gatesMode())
}

// finishWinner releases the item on the winner's outcome, publishes its branch
// when the profile sends the item to done, and cleans up. It is the tail of the
// ordinary path.
func (w *Worker) finishWinner(ctx context.Context, it kanban.Item, c *candidate, ws *Workspace, o outcome, ran, n int, reason string) {
	p := w.Profile
	if ws == nil {
		w.releaseCandidates(ctx, it, o)
		return
	}
	o.note += fmt.Sprintf("; candidate %d of %d chosen by the harness (%d ran, rule %s, gates %d/%d, %d files)",
		c.f.K, n, ran, reason, c.f.GatesMet, c.f.GatesTotal, c.f.Files)
	if o.failed {
		// A winner that failed on being materialized again leaves no branch.
		w.discard(ctx, it, ws)
		w.releaseCandidates(ctx, it, o)
		return
	}
	keep := p.Workspace != nil && p.Workspace.Keep
	discarded := false
	defer func() {
		if !keep && !discarded {
			w.discard(ctx, it, ws)
		}
	}()
	// A branch can be checked out in one worktree only: once the card is in
	// Review another worker may add its own worktree on this branch, so ours goes
	// first unless we still publish from it.
	if !keep && !(o.files > 0 && w.publishes() && w.routesToDone(o)) {
		w.discard(ctx, it, ws)
		discarded = true
	}
	released := w.release(ctx, it, o)
	w.dropCandidates()
	if o.files > 0 && released.List == kanban.Done && w.publishes() && !w.coordTookOver(it.ID) {
		w.publish(ctx, ws, released, o.files)
	}
	w.reflect(ctx, it, c.res, c.err)
}

// runCandidate runs candidate k of n: a fresh worktree and branch, one claim and
// session of its own, the item's feedback rounds, the commit, and a quiet
// verification of the branch. It then removes the worktree and parks the branch.
// stop is true when no further candidate should run: the item's context ended,
// or the isolation of the next candidate could not be kept.
func (w *Worker) runCandidate(ctx, itemCtx context.Context, it kanban.Item, k, n int, memory string, repoRun forge.Runner, top string, env candidateEnv) (*candidate, bool, error) {
	bg := context.WithoutCancel(ctx)
	// Each candidate names its own branch: the attempt number moves on by one for
	// each, so a parked branch's name is never taken by a later candidate.
	cit := it
	cit.Attempts = it.Attempts + k - 1
	ws, err := w.workspace(ctx, cit)
	if err != nil {
		return nil, true, err
	}
	w.Record.Branch = ws.Branch
	sessionID := session.MustID()
	w.Record.Session = sessionID
	w.save()
	claim := w.newClaim(itemCtx, it)
	claim.Candidate = true

	ws.Sandbox() // prepares the ref dirs and packed-refs before the snapshot
	rootBefore := ws.RootEntries()
	tokensBefore := env.tokens()
	rr := w.runRounds(itemCtx, Turn{
		Item: it, Workdir: ws.Dir, Claim: claim, SessionID: sessionID, Workspace: ws,
		Memory: memory, Emit: env.emit, Candidate: true, Attempt: k, Attempts: n,
	}, env.nextRound)
	res, runErr := rr.res, rr.err
	if removed, err := ws.Settle(rootBefore); err != nil || len(removed) > 0 {
		w.logf("%s: candidate %d: settled the git common dir: removed %v, err %v", it.Short(), k, removed, err)
	}
	cause := context.Cause(itemCtx)
	o := w.judge(it, res, runErr, cause, claim)
	o = w.applyRounds(o, rr, it)

	c := &candidate{o: o, res: res, err: runErr, session: sessionID}
	c.f = candidateFacts{K: k, Branch: ws.Branch, Tokens: max(0, env.tokens()-tokensBefore),
		ScannerPresent: rr.stillPresent, ScannerClear: rr.scanned && !rr.stillPresent}

	if ctx.Err() != nil || (itemCtx.Err() != nil && !budgetCause(cause)) {
		// The worker is stopping or the lease is lost: keep what was done on the
		// branch and hand the item back (stopped); this candidate is not counted.
		msg := fmt.Sprintf("belai: work in progress on %s (candidate %d)", it.Short(), k)
		_, _ = ws.Commit(bg, msg)
		_ = ws.Remove(bg)
		c.f.Failed, c.cut = true, true
		return c, true, nil
	}

	if itemCtx.Err() == nil {
		msg := fmt.Sprintf("belai: %s %s\n\nAgent %s, kanban item %s, attempt %d, candidate %d of %d.", it.Short(), it.Title, w.Profile.Name, it.Short(), it.Attempts+1, k, n)
		if _, err := ws.Commit(bg, msg); err != nil {
			w.logf("%s: candidate %d: commit: %v", it.Short(), k, err)
			if !o.failed {
				o = outcome{failed: true, note: "the work could not be committed: " + sanitize.Sanitize(err.Error())}
			}
		} else {
			o.files = ws.FilesChanged(bg)
			w.auditCommit(bg, it, ws, o.files, "committed")
		}
		if bad := ws.CommittedSynced(bg); len(bad) > 0 && !o.failed {
			o = outcome{failed: true, files: o.files, note: "the branch commits a crew file the harness keeps out of branches: " + sanitize.Line(strings.Join(bad, ", "), 200)}
		}
	}
	c.f.Files = o.files
	if head, err := ws.Head(bg); err == nil {
		c.f.Head = head
	} else {
		o.failed = true
		o.note = "the candidate's branch head could not be read"
	}

	if !o.failed && itemCtx.Err() == nil {
		v := w.verifyBranchAs(itemCtx, it, ws, false)
		switch {
		case itemCtx.Err() != nil:
			o = outcome{failed: true, files: o.files, fault: classifyFault(nil, context.Cause(itemCtx), ""),
				note: "the item's budget ended while its gates were verified: " + context.Cause(itemCtx).Error()}
		default:
			c.f.Blocked = v.blocked
			c.f.Regressed = v.regressed > 0
			c.f.GatesMet, c.f.GatesTotal = v.gatesMet, v.gatesTotal
			c.states = v.states
			o = w.applyVerification(o, v, w.gatesMode())
		}
	}
	c.o, c.f.Failed = o, o.failed

	// Out of sight of the next candidate: the worktree goes, then the branch ref.
	stop := itemCtx.Err() != nil
	if err := ws.Remove(bg); err != nil {
		// A worktree that stays (its teardown failed) is a place the next
		// candidate could read, so no more candidates are run.
		w.logf("%s: candidate %d: the worktree was kept (%v); no further candidate is run", it.Short(), k, err)
		stop = true
	} else if c.f.Head != "" {
		if err := parkBranch(bg, repoRun, top, ws.Branch); err != nil {
			w.logf("%s: candidate %d: park the branch: %v; no further candidate is run", it.Short(), k, err)
			stop = true
		}
	}
	return c, stop, nil
}

// auditCandidate records one candidate's measured facts.
func (w *Worker) auditCandidate(it kanban.Item, c *candidate) {
	if c.cut {
		return
	}
	w.auditItem(audit.CardCandidate, it, audit.Fact{SessionID: c.session, Branch: c.f.Branch, Commit: c.f.Head, Outcome: c.f.status(),
		Data: map[string]string{
			"candidate": strconv.Itoa(c.f.K), "met": strconv.Itoa(c.f.GatesMet), "total": strconv.Itoa(c.f.GatesTotal),
			"files": strconv.Itoa(c.f.Files), "tokens": strconv.Itoa(c.f.Tokens),
		}})
}

// auditSelected records the harness's choice: the winner and the rule that
// separated it, or none.
func (w *Worker) auditSelected(it kanban.Item, cands []*candidate, win *candidate, reason string) {
	f := audit.Fact{Outcome: "selected", Data: map[string]string{"candidates": strconv.Itoa(len(cands)), "reason": reason}}
	if win != nil {
		f.Branch, f.Commit, f.SessionID = win.f.Branch, win.f.Head, win.session
		f.Data["candidate"] = strconv.Itoa(win.f.K)
	} else {
		f.Outcome = candidateNone
	}
	w.auditItem(audit.CardCandidateSelected, it, f)
}
