package fleet

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/attribution"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/builddetect"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/lintdetect"
	"github.com/vulnetix/belai/internal/mergefix"
	"github.com/vulnetix/belai/internal/quality"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/tools"
)

// The merge-conflict loop (kanban.conflicts, docs/merge-conflicts.md).
//
// A card labelled merge-conflict names a pull request. The harness, not the
// model, runs the loop: up to max_checkouts times it makes a fresh worktree at
// the pull request's head, merges the base into it without committing, and
// gives the model one turn with the conflicts, the ledger of earlier attempts
// and a strategy to try, all composed by the harness from git's index and its
// own records. After the turn the harness classifies what the model left,
// hunk by hunk, refuses a resolution whose fingerprint an earlier attempt
// already produced (once with a second turn, then as a spent attempt), and
// runs the checks: leftover markers, duplicate imports, the build, the linters
// and the tests. A pass is committed as a two-parent merge and pushed to the
// pull request's head as a fast-forward; a failure is one ledger note and a
// discarded worktree, and the next attempt starts from scratch. Every note,
// commit message and comment is composed from identifiers: no model text
// reaches any of them.

// ConflictRefs are the two commits a pull request's merge is made from.
type ConflictRefs struct {
	Head, Base string
}

// ConflictForge is the forge side of the loop: fetching the pull request's
// two sides, pushing a resolution, publishing a fork's resolution as a branch
// of origin, and commenting on the pull request. nil on the worker uses origin
// through git and the forge CLI, with the credential the vault grants this
// machine.
type ConflictForge interface {
	Fetch(ctx context.Context, repo string, c mergefix.Card) (ConflictRefs, error)
	// Push moves the pull request's head to sha, which descends from it. It
	// never forces.
	Push(ctx context.Context, ws *Workspace, c mergefix.Card, sha string) error
	// Publish pushes the worktree's own branch to origin.
	Publish(ctx context.Context, ws *Workspace) error
	Comment(ctx context.Context, ws *Workspace, c mergefix.Card, body string) error
}

// conflictMode reports whether this claim runs the merge-conflict loop.
func (w *Worker) conflictMode(it kanban.Item) bool {
	k := w.Profile.Kanban
	return k != nil && k.Conflicts != nil && slices.Contains(it.Labels, agentprofile.MergeConflictLabel) &&
		w.Profile.IsolationMode() == agentprofile.IsolationWorktree
}

func (w *Worker) conflictForge() ConflictForge {
	if w.Conflicts != nil {
		return w.Conflicts
	}
	return originForge{}
}

// conflictLedgerDir is where the ledger files live: the state directory, which
// the OS sandbox hides from every command.
func conflictLedgerDir() (string, error) {
	dir, err := config.AgentsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "conflicts"), nil
}

// buildsAt returns the build checks detected at dir.
func (w *Worker) buildsAt(ctx context.Context, dir string) []builddetect.Build {
	if w.Builds != nil {
		return w.Builds(ctx)
	}
	return builddetect.Detect(dir)
}

// Limits on what one merge may hold.
const (
	maxConflictFiles = 200
	maxConflictBytes = 2 << 20
)

// conflictFile is one conflicted file as the harness captured it right after
// the merge: its hunks, and both sides' text for the marker and import
// baselines (nil for a whole-file conflict).
type conflictFile struct {
	file         mergefix.File
	ours, theirs []byte
	// merged is the file as the merge left it, markers and all.
	merged []byte
}

// mergeState is the uncommitted merge as git left it: the worktree's index
// with its unmerged entries, the MERGE_* files, and the text conflicts with
// their markers. A turn after a refused repeat starts from it again, so the
// model resolves the conflicts afresh rather than editing its own repeat.
type mergeState struct {
	admin map[string][]byte
	files map[string][]byte
}

var mergeAdminFiles = []string{"index", "MERGE_HEAD", "MERGE_MODE", "MERGE_MSG"}

func snapshotMerge(ws *Workspace, tree conflictTree) mergeState {
	s := mergeState{admin: map[string][]byte{}, files: map[string][]byte{}}
	for _, name := range mergeAdminFiles {
		if data, err := os.ReadFile(filepath.Join(ws.gitDir, name)); err == nil {
			s.admin[name] = data
		}
	}
	for _, cf := range tree.files {
		if cf.merged != nil {
			s.files[cf.file.Path] = cf.merged
		}
	}
	return s
}

// restore puts the merge back: the branch at the head again (the model may
// have committed on it), the index and MERGE_* files, and each text conflict
// with its markers. A path whose directory the model replaced with a link is
// refused, so the write can never land outside the worktree.
func (s mergeState) restore(ctx context.Context, ws *Workspace, head string) error {
	if err := ws.intact(); err != nil {
		return err
	}
	if _, err := git(ctx, ws.run, ws.Dir, "update-ref", "refs/heads/"+ws.Branch, head); err != nil {
		return err
	}
	for name, data := range s.admin {
		if err := os.WriteFile(filepath.Join(ws.gitDir, name), data, 0o644); err != nil {
			return err
		}
	}
	for rel, data := range s.files {
		if err := writeInTree(ws.Dir, rel, data); err != nil {
			return err
		}
	}
	return nil
}

// writeInTree writes a file under root without following a link on the way:
// every directory of rel must be a real directory, and whatever is at rel is
// removed first.
func writeInTree(root, rel string, data []byte) error {
	if !safeRel(rel) {
		return errors.New("not a plain relative path")
	}
	parts := strings.Split(rel, "/")
	dir := root
	for _, seg := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, seg)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case !fi.IsDir():
			return fmt.Errorf("%s is not a directory", rel)
		}
	}
	p := filepath.Join(dir, parts[len(parts)-1])
	if fi, err := os.Lstat(p); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("%s is a directory", rel)
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return os.WriteFile(p, data, 0o644)
}

type conflictTree struct {
	files []conflictFile
}

func (t conflictTree) parsed() []mergefix.File {
	out := make([]mergefix.File, 0, len(t.files))
	for _, f := range t.files {
		out = append(out, f.file)
	}
	return out
}

func (t conflictTree) paths() []string {
	out := make([]string, 0, len(t.files))
	for _, f := range t.files {
		out = append(out, f.file.Path)
	}
	return out
}

// workConflicts runs the loop for one claimed card and returns how it is
// released. It writes the ledger notes itself; the release note is a harness
// sentence beside them.
func (w *Worker) workConflicts(ctx context.Context, it kanban.Item, claim *tools.WorkerClaim, emit func(agent.Event), nextRound func()) outcome {
	p := w.Profile
	spec := *p.Kanban.Conflicts
	max := spec.MaxCheckoutsOr()
	block := func(why string) outcome {
		return outcome{failed: true, blocked: true, note: "merge-conflict card " + it.Short() + ": " + why + "; a person must look"}
	}
	card, err := mergefix.ParseCard(it.Body)
	if err != nil {
		return block(err.Error())
	}
	if remote := kanban.RemoteFor(w.Repo); remote != card.Repo {
		if remote == "" {
			remote = "not a GitHub or GitLab remote"
		}
		return block(fmt.Sprintf("the card names %s, but this repository's origin is %s", card.Repo, remote))
	}
	if it.PR != "" && !strings.EqualFold(it.PR, card.PR) {
		return block("the card's pull request link and its pull request block differ")
	}
	for _, ref := range []string{card.Head, card.Base} {
		if err := forge.ValidBranchName(ctx, hardenedGit("", ""), w.Repo, ref); err != nil {
			return block("the head or base is not a valid branch name")
		}
	}
	cf := w.conflictForge()
	refs, err := cf.Fetch(ctx, w.Repo, card)
	if err != nil || kanban.CleanRef(refs.Head) == "" || kanban.CleanRef(refs.Base) == "" {
		why := "no commit"
		if err != nil {
			why = sanitize.Line(err.Error(), 200)
		}
		return outcome{failed: true, note: "the pull request's head and base could not be fetched: " + why}
	}
	refs.Head, refs.Base = kanban.CleanRef(refs.Head), kanban.CleanRef(refs.Base)
	dir, err := conflictLedgerDir()
	if err != nil {
		return outcome{failed: true, note: "the ledger directory could not be located"}
	}
	led, ok := mergefix.ReadLedger(dir, it.ID)
	if !ok {
		// Another machine may have made the earlier attempts: their notes are on
		// the card, and their fingerprints are excluded all the same.
		var notes []string
		for _, m := range it.History {
			notes = append(notes, m.Note)
		}
		led = mergefix.LedgerFromNotes(it.ID, refs.Head, refs.Base, notes)
	}
	if slices.ContainsFunc(it.Labels, func(l string) bool { return strings.HasPrefix(l, kanban.CheckpointPrefix) }) {
		// The claim never hands over a held card; a new round is a person's
		// decision, made by clearing the checkpoint.
		return block("the card is still held at a checkpoint")
	}
	led, retry := led.For(it.ID, refs.Head, refs.Base, max)
	loop := conflictRun{w: w, it: it, claim: claim, card: card, refs: refs, max: max, dir: dir,
		prefer: mergefix.Choice(spec.PreferOr()), emit: emit, nextRound: nextRound, forge: cf, led: &led}
	switch {
	case retry:
		// The last round ended and a person sent the card back: a new round of
		// checkouts, numbered from 1, that still excludes every fingerprint tried.
		loop.note(mergefix.RetryNote(led.Round))
		loop.save()
		w.logf("%s: conflict retry: round %d, %d fingerprints already tried", it.Short(), led.Round, len(led.Tried))
	case len(led.Attempts) > 0:
		w.logf("%s: resuming conflict round %d at attempt %d of %d", it.Short(), led.RoundOr(), len(led.Attempts)+1, max)
	}
	for len(led.Attempts) < max {
		if o, done := loop.attempt(ctx, len(led.Attempts)+1); done {
			return o
		}
	}
	led.Closed = true
	loop.save()
	loop.note(mergefix.FinalUnresolved(max))
	o := outcome{failed: true, blocked: true, addLabels: []string{kanban.CheckpointLabel(kanban.CheckpointConflictsUnresolved)},
		note: fmt.Sprintf("agent %s found no resolution of %s#%d that passed its checks in %d fresh checkouts; held for a person at checkpoint %s", p.Name, card.Repo, card.Number, max, kanban.CheckpointConflictsUnresolved)}
	return o
}

// conflictRun is one claim's loop state.
type conflictRun struct {
	w         *Worker
	it        kanban.Item
	claim     *tools.WorkerClaim
	card      mergefix.Card
	refs      ConflictRefs
	max       int
	dir       string
	prefer    mergefix.Choice
	emit      func(agent.Event)
	nextRound func()
	forge     ConflictForge
	led       *mergefix.Ledger
	turns     int
}

// note appends a harness-composed note to the card.
func (r *conflictRun) note(text string) {
	if _, err := r.w.Store.Update(r.it.ID, kanban.Patch{Note: text}, r.w.Record.Session); err != nil {
		r.w.logf("%s: note: %v", r.it.Short(), err)
	}
}

func (r *conflictRun) save() {
	if err := mergefix.WriteLedger(r.dir, *r.led); err != nil {
		r.w.logf("%s: conflict ledger: %v", r.it.Short(), err)
	}
}

// record ends a failed attempt: the ledger entry, the note and the file.
func (r *conflictRun) record(a mergefix.Attempt) {
	r.led.Record(a)
	r.note(mergefix.FormatNote(a))
	r.save()
	r.w.logf("%s: %s", r.it.Short(), mergefix.FormatNote(a))
}

// discardAttempt removes an attempt's worktree and its branch: a failed
// attempt leaves nothing behind, so the next one starts from scratch.
func discardAttempt(ctx context.Context, ws *Workspace) {
	if ws == nil || !ws.Worktree {
		return
	}
	bg := context.WithoutCancel(ctx)
	_ = ws.Remove(bg)
	if strings.HasPrefix(ws.Branch, BranchPrefix) {
		_, _ = git(bg, ws.repoRun, ws.repo, "branch", "-D", ws.Branch)
	}
}

// interrupted is the outcome of a loop whose item context ended (a budget, a
// lost lease, a stop) in the middle of attempt n. The attempt is not recorded:
// the next claim makes it again.
func (r *conflictRun) interrupted(ctx context.Context, n int) outcome {
	cause := context.Cause(ctx)
	f := classifyFault(nil, cause, "")
	why := "the item's context ended"
	if cause != nil {
		why = cause.Error()
	}
	return outcome{failed: true, fault: f, note: fmt.Sprintf("agent %s did not finish conflict attempt %d of %d: %s", r.w.Profile.Name, n, r.max, why)}
}

// attempt runs checkout n. done is true when the loop ends with o; false when
// the attempt failed and was recorded.
func (r *conflictRun) attempt(ctx context.Context, n int) (outcome, bool) {
	w, it := r.w, r.it
	if ctx.Err() != nil {
		return r.interrupted(ctx, n), true
	}
	// A fresh worktree on a new branch of the item's own, at the head.
	ws, err := PrepareWorktree(ctx, w.Repo, kanban.Item{ID: it.ID, Title: it.Title, Attempts: it.Attempts + n - 1}, r.refs.Head)
	if err != nil {
		return outcome{failed: true, fault: Fault{Code: FaultWorkspace}, note: "the workspace could not be prepared: " + sanitize.Sanitize(err.Error())}, true
	}
	ws.Session = w.Record.Session
	w.Record.Branch = ws.Branch
	w.save()
	keep := false
	defer func() {
		if !keep {
			discardAttempt(ctx, ws)
		}
	}()
	if b := r.led.Baseline; b == nil || b.Head != r.refs.Head {
		r.led.Baseline = w.conflictBaseline(ctx, ws, r.refs.Head)
		r.save()
	}
	if ctx.Err() != nil {
		return r.interrupted(ctx, n), true
	}
	mergeErr := ws.mergeNoCommit(ctx, r.refs.Base)
	tree, err := captureConflicts(ctx, ws)
	if err != nil {
		return outcome{failed: true, blocked: true, note: "the merge's conflicts could not be read: " + sanitize.Line(err.Error(), 200) + "; a person must look"}, true
	}
	if len(tree.files) == 0 {
		if mergeErr != nil {
			return outcome{failed: true, blocked: true, note: "git could not merge the base into the head: " + sanitize.Line(mergeErr.Error(), 200) + "; a person must look"}, true
		}
		// The base merges cleanly now: check it and finish.
		chk := w.conflictChecks(ctx, ws, tree, nil, r.led.Baseline)
		switch {
		case ctx.Err() != nil:
			return r.interrupted(ctx, n), true
		case chk.blocked != "":
			return outcome{failed: true, blocked: true, note: chk.blocked + "; a person must look"}, true
		case chk.result.Failed():
			return outcome{failed: true, blocked: true, note: fmt.Sprintf("the base merges into the head without conflicts, but %s; a person must look", chk.result)}, true
		}
		o, kept := r.finish(ctx, ws, mergefix.Attempt{N: n, Max: r.max, Result: mergefix.Result{Outcome: mergefix.Passed}, Strategy: "no conflicts"}, chk, true)
		keep = kept
		return o, true
	}
	files := tree.parsed()
	state := snapshotMerge(ws, tree)
	tried := r.led.Fingerprints()
	strategy := mergefix.Plan(files, tried, r.led.LastImplicated(), r.prefer)
	w.logf("%s: conflict attempt %d of %d on %s: %d conflicted files, strategy %s", it.Short(), n, r.max, ws.Branch, len(files), strategy.Name)

	results, o, stop := r.turn(ctx, ws, tree, n, strategy, 0, "")
	if stop {
		return o, true
	}
	fp := mergefix.Fingerprint(results)
	if prev := r.led.TriedBy(fp); prev > 0 {
		// The same choices as an earlier attempt: say so once, in the same
		// worktree, with a strategy that excludes this fingerprint too.
		tried[fp] = true
		strategy = mergefix.Plan(files, tried, r.led.LastImplicated(), r.prefer)
		w.logf("%s: attempt %d repeats attempt %d (%s); asking for another variation", it.Short(), n, prev, fp)
		if err := state.restore(ctx, ws, r.refs.Head); err != nil {
			// The merge cannot be put back for a second turn: the repeat stands.
			w.logf("%s: restore the merge for another turn: %v", it.Short(), err)
			r.record(mergefix.Attempt{N: n, Max: r.max, Fingerprint: fp, Files: mergefix.Summarise(results), Hunks: results,
				Result: mergefix.Result{Outcome: mergefix.Repeat, RepeatOf: prev}, Strategy: strategy.Name})
			return outcome{}, false
		}
		results, o, stop = r.turn(ctx, ws, tree, n, strategy, prev, fp)
		if stop {
			return o, true
		}
		fp = mergefix.Fingerprint(results)
		if again := r.led.TriedBy(fp); again > 0 {
			r.record(mergefix.Attempt{N: n, Max: r.max, Fingerprint: fp, Files: mergefix.Summarise(results), Hunks: results,
				Result: mergefix.Result{Outcome: mergefix.Repeat, RepeatOf: again}, Strategy: strategy.Name})
			return outcome{}, false
		}
	}
	resolved := readResolved(ws, tree)
	chk := w.conflictChecks(ctx, ws, tree, resolved, r.led.Baseline)
	switch {
	case ctx.Err() != nil:
		return r.interrupted(ctx, n), true
	case chk.blocked != "":
		return outcome{failed: true, blocked: true, note: chk.blocked + "; a person must look"}, true
	}
	a := mergefix.Attempt{N: n, Max: r.max, Fingerprint: fp, Files: mergefix.Summarise(results), Hunks: results,
		Result: chk.result, Strategy: strategy.Name, Implicated: chk.implicated}
	if chk.result.Failed() {
		r.record(a)
		return outcome{}, false
	}
	o, kept := r.finish(ctx, ws, a, chk, false)
	keep = kept
	return o, true
}

// turn runs one agent turn on the attempt's worktree and classifies what it
// left. stop is true when the loop must end with o (the item's context ended,
// the provider is down, the session could not start).
func (r *conflictRun) turn(ctx context.Context, ws *Workspace, tree conflictTree, n int, s mergefix.Strategy, repeatOf int, repeatFP string) ([]mergefix.HunkResult, outcome, bool) {
	w := r.w
	if r.turns > 0 && r.nextRound != nil {
		r.nextRound()
	}
	r.turns++
	sessionID := session.MustID()
	w.Record.Session = sessionID
	w.save()
	ws.Session = sessionID
	ws.Sandbox() // prepares the ref dirs and packed-refs before the snapshot
	rootBefore := ws.RootEntries()
	w.applyControl()
	res, err := w.Runner(ctx, Turn{
		Item: r.it, Workdir: ws.Dir, Claim: r.claim, SessionID: sessionID, Workspace: ws,
		Memory: w.memory(ctx), Emit: r.emit,
		Conflicts: conflictBrief(r.card, n, r.max, tree, *r.led, s, repeatOf, repeatFP),
	})
	if removed, serr := ws.Settle(rootBefore); serr != nil || len(removed) > 0 {
		w.logf("%s: settled the git common dir: removed %v, err %v", r.it.Short(), removed, serr)
	}
	switch {
	case ctx.Err() != nil:
		return nil, r.interrupted(ctx, n), true
	case err != nil:
		f := classifyFault(err, nil, res.StopReason)
		o := outcome{failed: true, fault: f, transient: f.Outage, note: fmt.Sprintf("agent %s did not finish conflict attempt %d of %d: error: %s", w.Profile.Name, n, r.max, sanitize.Line(err.Error(), 200))}
		if o.transient {
			o.note += "; the model provider was unavailable, so this is not counted as an attempt"
		}
		return nil, o, true
	}
	// However the turn ended, what decides the attempt is what it left.
	return classifyTree(ws, tree), outcome{}, false
}

// classifyTree classifies every conflicted file as the turn left it.
func classifyTree(ws *Workspace, tree conflictTree) []mergefix.HunkResult {
	var out []mergefix.HunkResult
	for _, cf := range tree.files {
		f := cf.file
		data, kind := readTreeFile(ws.Dir, f.Path)
		if f.Whole {
			result := ""
			if kind != fileGone {
				result = blobID(data, len(f.OursBlob) == 64 || len(f.TheirsBlob) == 64)
			}
			c, d := mergefix.ClassifyWhole(f.OursBlob, f.TheirsBlob, result)
			out = append(out, mergefix.HunkResult{File: f.Path, Hunk: 0, Choice: c, Digest: d})
			continue
		}
		if kind != fileRegular {
			out = append(out, mergefix.Classify(f, nil, true)...)
			continue
		}
		out = append(out, mergefix.Classify(f, data, false)...)
	}
	return out
}

// readResolved returns each text conflict's resolved content, nil for a file
// that is gone or is not a regular file.
func readResolved(ws *Workspace, tree conflictTree) map[string][]byte {
	out := map[string][]byte{}
	for _, cf := range tree.files {
		if cf.file.Whole {
			continue
		}
		if data, kind := readTreeFile(ws.Dir, cf.file.Path); kind == fileRegular {
			out[cf.file.Path] = data
		}
	}
	return out
}

type fileKind int

const (
	fileGone fileKind = iota
	fileRegular
	fileLink
	fileOther
)

// readTreeFile reads a worktree file without following a symlink: a link's
// content is its target, as git stores it. A file over the size limit is read
// as other.
func readTreeFile(root, rel string) ([]byte, fileKind) {
	p := filepath.Join(root, filepath.FromSlash(rel))
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return nil, fileGone
	case fi.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return nil, fileOther
		}
		return []byte(target), fileLink
	case !fi.Mode().IsRegular() || fi.Size() > maxConflictBytes:
		return nil, fileOther
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fileOther
	}
	return data, fileRegular
}

// blobID is git's object id for a blob of data: sha1 in a sha1 repository,
// sha256 in a sha256 one.
func blobID(data []byte, sha256Repo bool) string {
	header := "blob " + strconv.Itoa(len(data)) + "\x00"
	if sha256Repo {
		h := sha256.New()
		h.Write([]byte(header))
		h.Write(data)
		return hex.EncodeToString(h.Sum(nil))
	}
	h := sha1.New()
	h.Write([]byte(header))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// safeRel reports whether a path from git's index is a plain relative path
// inside the worktree.
func safeRel(p string) bool {
	if p == "" || len(p) > 4096 || strings.ContainsRune(p, 0) || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "" || seg == ".git" {
			return false
		}
	}
	return true
}

// captureConflicts reads the unmerged paths from the worktree's index and
// each conflicted file as the merge left it.
func captureConflicts(ctx context.Context, ws *Workspace) (conflictTree, error) {
	if err := ws.intact(); err != nil {
		return conflictTree{}, err
	}
	out, err := ws.run(ctx, ws.Dir, "git", "ls-files", "-u", "-z")
	if err != nil {
		return conflictTree{}, err
	}
	stages := map[string][4]string{}
	var order []string
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			continue
		}
		stage, err := strconv.Atoi(f[2])
		if err != nil || stage < 1 || stage > 3 || kanban.CleanRef(f[1]) == "" {
			continue
		}
		if !safeRel(path) {
			return conflictTree{}, errors.New("a conflicted path is not a plain relative path")
		}
		s, seen := stages[path]
		if !seen {
			order = append(order, path)
		}
		s[stage] = kanban.CleanRef(f[1])
		stages[path] = s
	}
	if len(order) > maxConflictFiles {
		return conflictTree{}, fmt.Errorf("%d conflicted files, more than %d", len(order), maxConflictFiles)
	}
	sort.Strings(order)
	var tree conflictTree
	for _, path := range order {
		s := stages[path]
		if data, kind := readTreeFile(ws.Dir, path); kind == fileRegular && mergefix.MarkerCount(data) > 0 {
			if f, perr := mergefix.ParseFile(path, data); perr == nil && len(f.Hunks) > 0 {
				ours, _ := catBlob(ctx, ws, s[2])
				theirs, _ := catBlob(ctx, ws, s[3])
				tree.files = append(tree.files, conflictFile{file: f, ours: ours, theirs: theirs, merged: data})
				continue
			}
		}
		tree.files = append(tree.files, conflictFile{file: mergefix.File{Path: path, Whole: true, OursBlob: s[2], TheirsBlob: s[3]}})
	}
	return tree, nil
}

// catBlob reads one blob from the object store, up to the size limit.
func catBlob(ctx context.Context, ws *Workspace, id string) ([]byte, error) {
	if id == "" {
		return nil, nil
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out, err := ws.run(cctx, ws.Dir, "git", "cat-file", "blob", id)
	if err != nil || len(out) > maxConflictBytes {
		return nil, err
	}
	return out, nil
}

// mergeNoCommit merges base into the worktree's branch and stops before the
// commit, conflicts left in the files with diff3 markers. rerere is off, so a
// resolution recorded on this machine never answers for the model.
func (w *Workspace) mergeNoCommit(ctx context.Context, base string) error {
	if err := w.intact(); err != nil {
		return err
	}
	_, err := git(ctx, w.run, w.Dir, "-c", "merge.conflictStyle=diff3", "-c", "rerere.enabled=false",
		"merge", "--no-ff", "--no-commit", "--no-edit", "--end-of-options", base)
	return err
}

// stageTree stages the whole worktree and writes it as a tree: exactly what
// the checks then run on and what the merge commit holds. Build output the
// checks leave afterwards is not in it.
func (w *Workspace) stageTree(ctx context.Context) (string, error) {
	if err := w.intact(); err != nil {
		return "", err
	}
	if err := w.onBranch(ctx); err != nil {
		return "", err
	}
	if _, err := git(ctx, w.run, w.Dir, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	tree, err := git(ctx, w.run, w.Dir, "write-tree")
	if err != nil {
		return "", err
	}
	if kanban.CleanRef(tree) == "" {
		return "", errors.New("git wrote no tree id")
	}
	return kanban.CleanRef(tree), nil
}

// commitMerge commits tree as the merge of base into head and points the
// worktree's branch at it. The model's own commits, if it made any, are left
// out: the merge's parents are the two commits the attempt started from.
func (w *Workspace) commitMerge(ctx context.Context, tree, head, base, msg string) (string, error) {
	sha, err := git(ctx, w.run, w.Dir, "commit-tree", tree, "-p", head, "-p", base, "-m", attribution.Message(msg, w.Session))
	if err != nil {
		return "", err
	}
	if sha = kanban.CleanRef(sha); sha == "" {
		return "", errors.New("git wrote no commit id")
	}
	if _, err := git(ctx, w.run, w.Dir, "update-ref", "refs/heads/"+w.Branch, sha); err != nil {
		return "", err
	}
	_, _ = git(ctx, w.run, w.Dir, "reset", "-q")
	return sha, nil
}

// conflictCheck is what the checks found.
type conflictCheck struct {
	result     mergefix.Result
	implicated []string
	// blocked is why a check could not run at all (a deny or ask rule): that
	// says nothing about the resolution, so a person must look.
	blocked string
	tree    string   // the tree the checks ran on
	ran     []string // the checks that ran
	skipped []string // the checks whose program could not start, or that already failed on the head
}

// conflictBaseline runs the build and the linters on the head before the
// merge: a build that already fails there, and the findings a linter already
// reports there, are not the merge's doing.
func (w *Worker) conflictBaseline(ctx context.Context, ws *Workspace, head string) *mergefix.Baseline {
	b := &mergefix.Baseline{Head: head, Lint: map[string]int{}}
	builds := w.buildsAt(ctx, ws.Dir)
	if plan := buildPlan(builds); len(plan.Argvs) > 0 {
		for i, r := range w.testResultsAt(ctx, ws.Dir, plan) {
			if r.Status == testrun.Failed || r.Status == testrun.TimedOut || r.Status == testrun.Errored {
				b.BuildFailed = append(b.BuildFailed, builds[i].Name)
			}
		}
	}
	linters := w.lintersAt(ctx, ws.Dir)
	if plan := lintPlan(linters); len(plan.Argvs) > 0 {
		for i, r := range w.testResultsAt(ctx, ws.Dir, plan) {
			switch n, ok := lintFindings(linters[i], r); {
			case ok:
				b.Lint[linters[i].Name] = n
			case r.Status != testrun.Denied && r.Status != testrun.NeedsApproval:
				b.LintSkip = append(b.LintSkip, linters[i].Name)
			}
		}
	}
	return b
}

func buildPlan(builds []builddetect.Build) testrun.Plan {
	var p testrun.Plan
	for _, b := range builds {
		p.Names = append(p.Names, b.Name)
		p.Argvs = append(p.Argvs, slices.Clone(b.Command))
	}
	return p
}

// lintFindings is a linter run's finding count, read from the tool's own
// summary or location lines; ok is false when the run says nothing about
// findings (it could not start, or failed with another exit code).
func lintFindings(l lintdetect.Linter, r testrun.Result) (int, bool) {
	switch {
	case r.Status == testrun.Passed:
		return 0, true
	case r.Status == testrun.Failed && (l.FindingsExit == 0 || r.ExitCode == l.FindingsExit):
		return max(quality.CountFindings(l.Tool, r.Output), 1), true
	}
	return 0, false
}

// unrunnable reports a run that never executed because of a rule, which only
// a person can change.
func unrunnable(r testrun.Result) bool {
	return r.Status == testrun.Denied || r.Status == testrun.NeedsApproval
}

// conflictChecks runs the checks on the resolution, in order, and stops at
// the first failure: leftover markers, duplicate imports, the build, the
// linters, the tests. Only identifiers from them are kept: a check's output
// decides which conflicted files it implicates and nothing else.
func (w *Worker) conflictChecks(ctx context.Context, ws *Workspace, tree conflictTree, resolved map[string][]byte, base *mergefix.Baseline) conflictCheck {
	var c conflictCheck
	if base == nil {
		base = &mergefix.Baseline{}
	}
	for _, cf := range tree.files {
		data, ok := resolved[cf.file.Path]
		if cf.file.Whole || !ok {
			continue
		}
		if mergefix.MarkersLeft(data, cf.ours, cf.theirs) {
			c.result, c.implicated = mergefix.Result{Outcome: mergefix.MarkersFound}, []string{cf.file.Path}
			return c
		}
	}
	for _, cf := range tree.files {
		data, ok := resolved[cf.file.Path]
		if cf.file.Whole || !ok {
			continue
		}
		if dups := mergefix.NewDuplicateImports(cf.file.Path, data, cf.ours, cf.theirs); len(dups) > 0 {
			c.result, c.implicated = mergefix.Result{Outcome: mergefix.DuplicateImportsFound, File: cf.file.Path}, []string{cf.file.Path}
			return c
		}
	}
	staged, err := ws.stageTree(ctx)
	if err != nil {
		c.blocked = "the resolution could not be staged: " + sanitize.Line(err.Error(), 200)
		return c
	}
	c.tree = staged
	paths := tree.paths()

	var builds []builddetect.Build
	for _, b := range w.buildsAt(ctx, ws.Dir) {
		if slices.Contains(base.BuildFailed, b.Name) {
			c.skipped = append(c.skipped, b.Name)
			continue
		}
		builds = append(builds, b)
	}
	if plan := buildPlan(builds); len(plan.Argvs) > 0 {
		results := w.testResultsAt(ctx, ws.Dir, plan)
		for i, r := range results {
			b := builds[i]
			switch {
			case unrunnable(r):
				c.blocked = fmt.Sprintf("the build check %s could not run (%s)", b.Display(ws.Dir), r.Status)
				return c
			case r.Status == testrun.Errored:
				c.skipped = append(c.skipped, b.Name)
			case r.Status != testrun.Passed:
				c.result = mergefix.Result{Outcome: mergefix.BuildFailed, Command: b.Display(ws.Dir)}
				c.implicated = mergefix.Implicated(r.Output, paths)
				return c
			default:
				c.ran = append(c.ran, b.Name)
			}
		}
		if len(results) < len(plan.Argvs) {
			return c
		}
	}

	var linters []lintdetect.Linter
	for _, l := range w.lintersAt(ctx, ws.Dir) {
		if slices.Contains(base.LintSkip, l.Name) {
			c.skipped = append(c.skipped, l.Name)
			continue
		}
		linters = append(linters, l)
	}
	if plan := lintPlan(linters); len(plan.Argvs) > 0 {
		results := w.testResultsAt(ctx, ws.Dir, plan)
		for i, r := range results {
			l := linters[i]
			if unrunnable(r) {
				c.blocked = fmt.Sprintf("the linter %s could not run (%s)", l.Name, r.Status)
				return c
			}
			if r.Status == testrun.Errored {
				c.skipped = append(c.skipped, l.Name)
				continue
			}
			n, ok := lintFindings(l, r)
			if !ok {
				n = 1
			}
			if n > base.Lint[l.Name] {
				c.result = mergefix.Result{Outcome: mergefix.LintFailed, Linter: l.Name, Count: n}
				c.implicated = mergefix.Implicated(r.Output, paths)
				return c
			}
			c.ran = append(c.ran, l.Name)
		}
		if len(results) < len(plan.Argvs) {
			return c
		}
	}

	plan := testrun.BuildPlan(w.suitesAt(ctx, ws.Dir), w.Settings.TestsCommand(), "full", nil)
	if len(plan.Argvs) > 0 {
		for _, r := range w.testResultsAt(ctx, ws.Dir, plan) {
			switch {
			case unrunnable(r):
				c.blocked = fmt.Sprintf("the test suite %s could not run (%s)", r.Suite, r.Status)
				return c
			case r.Status == testrun.Errored:
				c.skipped = append(c.skipped, r.Suite)
			case r.Status != testrun.Passed:
				c.result = mergefix.Result{Outcome: mergefix.TestsFailed, Suite: r.Suite}
				c.implicated = mergefix.Implicated(r.Output, paths)
				return c
			default:
				c.ran = append(c.ran, r.Suite)
			}
		}
	}
	c.result = mergefix.Result{Outcome: mergefix.Passed}
	return c
}

// finish commits a passing attempt's merge, pushes it to the pull request's
// head as a fast-forward (a fork's goes to origin as the attempt's branch
// instead), comments the ledger on the pull request and composes the release.
// kept is true when the attempt's branch stays: it holds the merge.
func (r *conflictRun) finish(ctx context.Context, ws *Workspace, a mergefix.Attempt, chk conflictCheck, clean bool) (outcome, bool) {
	w, it, card := r.w, r.it, r.card
	bg := context.WithoutCancel(ctx)
	tree := chk.tree
	if tree == "" {
		var err error
		if tree, err = ws.stageTree(bg); err != nil {
			return outcome{failed: true, note: "the resolution could not be staged: " + sanitize.Line(err.Error(), 200)}, false
		}
	}
	msg := fmt.Sprintf("Merge %s into %s\n\nResolved by agent %s on conflict attempt %d of %d (strategy: %s), kanban item %s.",
		card.Base, card.Head, w.Profile.Name, a.N, a.Max, a.Strategy, it.Short())
	if clean {
		msg = fmt.Sprintf("Merge %s into %s\n\nMerged by agent %s, kanban item %s: the base merged without conflicts.", card.Base, card.Head, w.Profile.Name, it.Short())
	}
	sha, err := ws.commitMerge(bg, tree, r.refs.Head, r.refs.Base, msg)
	if err != nil {
		return outcome{failed: true, note: "the merge could not be committed: " + sanitize.Line(err.Error(), 200)}, false
	}
	files := ws.FilesChanged(bg)
	w.auditCommit(bg, it, ws, files, "merge")
	if !clean {
		a.Fingerprint = orFingerprint(a)
		r.led.Record(a)
	}
	pushed, why := false, ""
	switch {
	case !w.Settings.AgentsPublishEnabled():
		why = "agents.publish is off"
	case card.Fork:
		why = "the pull request is from a fork"
	default:
		pctx, cancel := context.WithTimeout(bg, 2*time.Minute)
		err := r.forge.Push(pctx, ws, card, sha)
		cancel()
		if err != nil {
			// The resolution passed but is not on the pull request: it is not an
			// attempt of the round, so the next claim may make it again.
			if !clean {
				r.led.Attempts = r.led.Attempts[:len(r.led.Attempts)-1]
				r.led.Tried = slices.DeleteFunc(r.led.Tried, func(t mergefix.Tried) bool { return t.Fingerprint == a.Fingerprint })
				r.save()
			}
			return outcome{failed: true, note: fmt.Sprintf("conflict attempt %d of %d passed its checks, but the merge could not be pushed to %s: %s", a.N, a.Max, card.Head, sanitize.Line(err.Error(), 200))}, false
		}
		pushed = true
		w.auditItem(audit.RepoPublish, it, audit.Fact{SessionID: w.Record.Session, Branch: card.Head, Commit: sha, BaseCommit: r.refs.Head, Outcome: "fast-forward",
			Data: map[string]string{"pr": httpsURL(card.PR)}})
	}
	if !clean {
		r.note(mergefix.FormatNote(a))
	}
	published := ""
	if !pushed && card.Fork && w.Settings.AgentsPublishEnabled() {
		pctx, cancel := context.WithTimeout(bg, 2*time.Minute)
		if err := r.forge.Publish(pctx, ws); err == nil {
			published = ws.Branch
		} else {
			w.logf("%s: publish the fork's resolution: %v", it.Short(), err)
		}
		cancel()
	}
	if pushed {
		r.note(mergefix.FinalResolved(a.N, sha))
	} else {
		r.note(mergefix.FinalNotPushed(a.N, why))
	}
	r.save()
	if w.Settings.AgentsPublishEnabled() {
		body := attribution.Markdown(conflictComment(card, *r.led, a, chk, sha, pushed, why, published, clean), ws.Session)
		cctx, cancel := context.WithTimeout(bg, time.Minute)
		if err := r.forge.Comment(cctx, ws, card, body); err != nil {
			w.logf("%s: comment on %s#%d: %v", it.Short(), card.Repo, card.Number, err)
			r.note("the attempt ledger was not posted on the pull request: " + sanitize.Line(err.Error(), 160))
		}
		cancel()
	}
	note := fmt.Sprintf("agent %s resolved the merge conflicts of %s#%d on attempt %d of %d", w.Profile.Name, card.Repo, card.Number, a.N, a.Max)
	switch {
	case clean:
		note = fmt.Sprintf("agent %s merged the base of %s#%d without conflicts", w.Profile.Name, card.Repo, card.Number)
	}
	if pushed {
		note += fmt.Sprintf("; pushed %s to %s as a fast-forward", short12(sha), card.Head)
	} else {
		note += "; not pushed: " + why
		if published != "" {
			note += "; the resolution is on " + published + " in " + card.Repo + ", named in a comment on the pull request"
		}
	}
	_ = ws.Remove(bg)
	// The card points at what the next stage should check out, on any host: the
	// pull request's head once the merge is on it, else the attempt's branch
	// (on origin for a fork whose resolution was published).
	branch := ws.Branch
	if pushed {
		branch = card.Head
	}
	return outcome{note: note, branch: branch, files: files}, true
}

// orFingerprint keeps an attempt's fingerprint, or makes one for an attempt
// that classified nothing.
func orFingerprint(a mergefix.Attempt) string {
	if a.Fingerprint != "" {
		return a.Fingerprint
	}
	return mergefix.Fingerprint(a.Hunks)
}

// conflictComment is the pull request comment: the attempt ledger as a table,
// and what was pushed. Harness facts only.
func conflictComment(c mergefix.Card, led mergefix.Ledger, final mergefix.Attempt, chk conflictCheck, sha string, pushed bool, why, published string, clean bool) string {
	var b strings.Builder
	if clean {
		fmt.Fprintf(&b, "The base `%s` now merges into `%s` without conflicts.\n\n", c.Base, c.Head)
	} else {
		fmt.Fprintf(&b, "Merge conflicts with `%s` resolved on attempt %d of %d.\n\n", c.Base, final.N, final.Max)
		b.WriteString("| Attempt | Fingerprint | Files | Result |\n| --- | --- | --- | --- |\n")
		for _, a := range led.Attempts {
			parts := make([]string, 0, len(a.Files))
			for i, f := range a.Files {
				if i == mergefix.MaxNoteFiles {
					parts = append(parts, fmt.Sprintf("+%d more", len(a.Files)-i))
					break
				}
				parts = append(parts, "`"+mergefix.NoteFile(f.File)+"`="+f.Choice)
			}
			fmt.Fprintf(&b, "| %d/%d | `%s` | %s | %s |\n", a.N, a.Max, a.Fingerprint, strings.Join(parts, ", "), a.Result)
		}
		b.WriteString("\n")
	}
	switch {
	case pushed:
		fmt.Fprintf(&b, "Pushed `%s` to `%s` as a fast-forward: a merge commit of `%s` into the head.\n", short12(sha), c.Head, c.Base)
	case published != "":
		fmt.Fprintf(&b, "Not pushed to `%s` (%s). The merge commit `%s` is on the branch `%s` of this repository.\n", c.Head, why, short12(sha), published)
	default:
		fmt.Fprintf(&b, "Not pushed to `%s` (%s).\n", c.Head, why)
	}
	if len(chk.ran) > 0 {
		fmt.Fprintf(&b, "\nChecks passed: %s.", strings.Join(chk.ran, ", "))
	}
	if len(chk.skipped) > 0 {
		fmt.Fprintf(&b, "\nNot run (failing on the head already, or not installed on this host): %s.", strings.Join(chk.skipped, ", "))
	}
	return strings.TrimSpace(b.String())
}

// conflictBrief is the attachment of a conflict turn: the attempt, what
// current and incoming mean, the conflicted files and their hunks, the
// ledger, and the strategy to try. Every value in it is a harness fact: the
// card's validated refs, cleaned paths from git's index, line numbers, ledger
// notes and choice names.
func conflictBrief(c mergefix.Card, n, max int, tree conflictTree, led mergefix.Ledger, s mergefix.Strategy, repeatOf int, repeatFP string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Merge conflict attempt %d of %d for %s#%d: the base %s merged into the head %s, in a fresh checkout.\n", n, max, c.Repo, c.Number, c.Base, c.Head)
	if r := led.RoundOr(); r > 1 {
		fmt.Fprintf(&b, "This is round %d: a person sent the card back after every checkout of the earlier rounds failed.\n", r)
	}
	b.WriteString("current is the head's side of a hunk (between <<<<<<< and |||||||), incoming is the base's side (between ======= and >>>>>>>), and the ||||||| section is their common ancestor. both keeps the two sides with current first; both-incoming-first keeps them with incoming first.\n")
	if repeatOf > 0 {
		fmt.Fprintf(&b, "\nYour resolution makes the same choices as attempt %d (fingerprint %s), which did not pass its checks. Make a different choice in at least one hunk. This repeat does not use up the attempt; a second one does.\n", repeatOf, repeatFP)
	}
	fmt.Fprintf(&b, "\nConflicted files (%d):\n", len(tree.files))
	for i, cf := range tree.files {
		if i == 100 {
			fmt.Fprintf(&b, "- and %d more (git status lists them)\n", len(tree.files)-i)
			break
		}
		f := cf.file
		if f.Whole {
			fmt.Fprintf(&b, "- %s: the whole file (binary, or changed on one side and deleted on the other): keep one side\n", mergefix.NoteFile(f.Path))
			continue
		}
		lines := make([]string, 0, len(f.Hunks))
		for _, h := range f.Hunks {
			lines = append(lines, strconv.Itoa(h.Line))
		}
		fmt.Fprintf(&b, "- %s: %d hunks, at lines %s\n", mergefix.NoteFile(f.Path), len(f.Hunks), strings.Join(lines, ", "))
	}
	if len(led.Attempts) > 0 {
		b.WriteString("\nEarlier attempts in this round, each from its own fresh checkout (none may be repeated):\n")
		for _, a := range led.Attempts {
			b.WriteString(mergefix.FormatNote(a) + "\n")
		}
	}
	if len(led.Tried) > len(led.Attempts) {
		b.WriteString("\nEarlier rounds on these commits also tried the fingerprints (none may be repeated):")
		for _, t := range led.Tried {
			b.WriteString(" " + t.Fingerprint)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\nStrategy for this attempt: %s.\n", s.Name)
	if s.Free {
		b.WriteString("Every variation the plan lists has been tried. Build a resolution of your own that differs from each earlier attempt in at least one hunk, keeping both sides' intent.\n")
	} else {
		for i, h := range s.Choices {
			if i == 200 {
				fmt.Fprintf(&b, "- and %d more hunks: %s\n", len(s.Choices)-i, h.Choice)
				break
			}
			fmt.Fprintf(&b, "- %s hunk %d: %s\n", mergefix.NoteFile(h.File), h.Hunk+1, h.Choice)
		}
	}
	b.WriteString("\nFollow the strategy unless the code shows it cannot work. Whatever you choose, your resolution is classified hunk by hunk when your turn ends, and one that repeats an earlier attempt is refused.\n")
	return b.String()
}

// conflictDirective replaces the workspace directive on a conflict turn.
func conflictDirective(ws *Workspace) string {
	if ws == nil || !ws.Worktree {
		return ""
	}
	head := ws.Base
	if len(head) > 12 {
		head = head[:12]
	}
	return fmt.Sprintf("Workspace: your working directory is a fresh git worktree of the pull request's head (branch %s, at commit %s) with its base merged in and the merge left uncommitted: the conflicted files hold conflict markers, and the attachment lists them with the ledger of earlier attempts and the strategy to try on this one. "+
		"Resolve every conflict in place and remove every marker; git status, git diff, git log and git checkout --ours or --theirs on a path work here. "+
		"Do not commit, merge, rebase, reset, stash or switch branches, and do not publish: when your turn ends the harness classifies your resolution hunk by hunk, refuses one that repeats an earlier attempt, checks for leftover markers and duplicate imports, runs the build, the linters and the tests, and commits and pushes the merge only if they pass. "+
		"Commit trailers are added for you.", ws.Branch, head)
}

// originForge is ConflictForge on the repository's origin.
type originForge struct{}

// pullRef is where the forge keeps a pull request's head for a fork.
func pullRef(rem forge.Remote, number int) string {
	if rem.Kind == forge.KindGitLab {
		return fmt.Sprintf("refs/merge-requests/%d/head", number)
	}
	return fmt.Sprintf("refs/pull/%d/head", number)
}

func (originForge) Fetch(ctx context.Context, repo string, c mergefix.Card) (ConflictRefs, error) {
	repoRun := hardenedGit("", "")
	top, err := git(ctx, repoRun, repo, "rev-parse", "--show-toplevel")
	if err != nil {
		return ConflictRefs{}, err
	}
	ident := identityEnv(ctx, repoRun, top)
	origin, _ := git(ctx, repoRun, top, "remote", "get-url", "origin")
	rem, ok := forge.ParseRemote(origin)
	if !ok {
		return ConflictRefs{}, errors.New("the repository has no GitHub or GitLab origin remote")
	}
	runner, credArgs := hardenedGit("", "", ident...), []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		runner = hardenedGit("", "", append(slices.Clone(ident), env...)...)
		credArgs = helper
	}
	head := "refs/heads/" + c.Head
	if c.Fork {
		head = pullRef(rem, c.Number)
	}
	headLocal := fmt.Sprintf("refs/belai/pull/%d/head", c.Number)
	baseLocal := fmt.Sprintf("refs/belai/pull/%d/base", c.Number)
	fctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := git(fctx, runner, top, append(credArgs, "fetch", "--no-tags", "origin", "+"+head+":"+headLocal, "+refs/heads/"+c.Base+":"+baseLocal)...); err != nil {
		return ConflictRefs{}, err
	}
	h, err := git(ctx, repoRun, top, "rev-parse", "--verify", "--end-of-options", headLocal+"^{commit}")
	if err != nil {
		return ConflictRefs{}, err
	}
	b, err := git(ctx, repoRun, top, "rev-parse", "--verify", "--end-of-options", baseLocal+"^{commit}")
	if err != nil {
		return ConflictRefs{}, err
	}
	return ConflictRefs{Head: h, Base: b}, nil
}

func (originForge) Push(ctx context.Context, ws *Workspace, c mergefix.Card, sha string) error {
	if err := ws.intact(); err != nil {
		return err
	}
	rem, err := ws.originRemote(ctx)
	if err != nil {
		return err
	}
	if err := ws.absorbObjects(); err != nil {
		return err
	}
	runner, credArgs := ws.run, []string(nil)
	if env, helper := publishAuth(rem, time.Now()); len(env) > 0 {
		runner = hardenedGit(ws.gitDir, ws.Dir, append(slices.Clone(ws.ident), env...)...)
		credArgs = helper
	}
	// No "+" and no --force: the forge refuses anything but a fast-forward.
	_, err = git(ctx, runner, ws.Dir, append(slices.Clone(credArgs), "push", "origin", sha+":refs/heads/"+c.Head)...)
	return err
}

func (originForge) Publish(ctx context.Context, ws *Workspace) error { return ws.PushBranch(ctx) }

func (originForge) Comment(ctx context.Context, ws *Workspace, c mergefix.Card, body string) error {
	p, _, _, err := ws.forgeAuthed(ctx)
	if err != nil {
		return err
	}
	return p.CommentPR(ctx, ws.Dir, c.Number, body)
}

// conflictAttachment is the label the brief rides under.
const conflictAttachment = "merge conflicts, the attempt ledger and the strategy for this attempt"

// conflictInput fills a conflict turn's attachment and directive.
func conflictInput(in *agent.TurnInput, t Turn) {
	in.Directive = conflictDirective(t.Workspace)
	in.Attachments = append(in.Attachments, run.Attachment{Kind: "conflicts", Label: conflictAttachment, Body: t.Conflicts})
}
