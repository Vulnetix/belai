package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/scanartifacts"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// Feedback rounds (kanban.security.rounds). A patcher's turn ends when the
// model says it is done, and a model's word that a finding is gone is not
// evidence. After each turn the harness scans the worktree itself and, while
// the scanner still reports the card's finding, runs another turn with the
// result attached, up to rounds turns. The verifier still checks the branch
// independently; this loop just stops a fix that did not work from reaching it.
//
// The feedback text is composed from parsed identifiers, versions and counts:
// no scanner text reaches the model, so it is a harness fact and needs no
// classifier.

// scanFeedback is the result of scanning a worktree for one finding.
type scanFeedback struct {
	present bool   // the scanner still reports the finding
	text    string // harness-composed feedback for the next round
}

// roundsResult is what running an item's turns produced.
type roundsResult struct {
	res    run.Result
	err    error
	rounds int // turns run
	// scanned is true when a feedback scan answered; stillPresent is true when
	// the last such scan still reported the finding.
	scanned, stillPresent bool
}

// maxRounds is how many turns this item may take: one unless the profile asks
// for feedback rounds and the item is an SCA finding worked in a worktree.
func (w *Worker) maxRounds(it kanban.Item, ws *Workspace) int {
	k := w.Profile.Kanban
	if k == nil || k.Security == nil || k.Security.Rounds <= 1 || ws == nil || !ws.Worktree || w.Profile.ReadOnlyWorkspace() {
		return 1
	}
	if it.Finding == "" || scanartifacts.KindOfID(it.Finding) != scanartifacts.KindSCA || strings.HasPrefix(it.Finding, SweepFindingPrefix) {
		return 1
	}
	return k.Security.Rounds
}

// runRounds runs the item's first turn and then, while the scanner still
// reports the finding and rounds remain, a feedback turn. nextRound is called
// between turns so the token budget counts them together.
func (w *Worker) runRounds(ctx context.Context, t Turn, nextRound func()) roundsResult {
	res, err := w.Runner(ctx, t)
	rr := roundsResult{res: res, err: err, rounds: 1}
	max := w.maxRounds(t.Item, t.Workspace)
	if max <= 1 {
		return rr
	}
	for {
		if rr.err != nil || ctx.Err() != nil {
			return rr
		}
		// A worker that recorded a verdict other than fixed is not claiming a
		// fix, so there is nothing to scan for.
		if rec, ok := t.Claim.Recorded(); ok && rec.Verdict != kanban.VerdictFixed {
			return rr
		}
		fb, ok := w.scanForFinding(ctx, t.Workspace, t.Item, rr.rounds, max)
		if !ok {
			return rr
		}
		rr.scanned, rr.stillPresent = true, fb.present
		if !fb.present || rr.rounds >= max {
			return rr
		}
		nextRound()
		t.Feedback, t.Round, t.SessionID = fb.text, rr.rounds+1, session.MustID()
		w.Record.Session = t.SessionID
		w.save()
		w.logf("%s: the scanner still reports %s; round %d of %d", t.Item.Short(), t.Item.Finding, t.Round, max)
		res, err := w.Runner(ctx, t)
		res.Passes += rr.res.Passes
		rr.res, rr.err, rr.rounds = res, err, rr.rounds+1
	}
}

// applyRounds adjusts the outcome for what the feedback scans showed: a finding
// the scanner still reports after the last round is a failed attempt, however
// sure the model was, and a fix the scanner confirmed says so.
func (w *Worker) applyRounds(o outcome, rr roundsResult, it kanban.Item) outcome {
	switch {
	case rr.stillPresent && !o.failed && o.to == "":
		o.failed, o.verdict = true, ""
		o.note = fmt.Sprintf("agent %s worked it for %d rounds and the scanner still reports %s", w.Profile.Name, rr.rounds, sanitize.Ident(it.Finding, 64))
	case rr.scanned && !rr.stillPresent && !o.failed && rr.rounds > 1:
		o.note += fmt.Sprintf("; the scanner confirmed the fix after %d rounds", rr.rounds)
	}
	return o
}

// scanForFinding scans the worktree and reports whether the card's finding is
// still there. ok is false when no answer could be had (a hook says so, no
// Vulnetix CLI, the scan failed, or the artefacts did not record the scan), and
// the caller then goes on without feedback.
func (w *Worker) scanForFinding(ctx context.Context, ws *Workspace, it kanban.Item, round, max int) (scanFeedback, bool) {
	if w.Scan != nil {
		return w.Scan(ctx, ws.Dir, it.Finding, round, max)
	}
	art := filepath.Join(ws.Dir, ".vulnetix")
	// A worktree that already has the directory (a tracked one) is left alone.
	if _, err := os.Lstat(art); err == nil {
		return scanFeedback{}, false
	}
	cli, err := vulnetixcli.Detect()
	if err != nil {
		return scanFeedback{}, false
	}
	defer removeScanArtefacts(art)
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err := cli.ExecIn(sctx, ws.Dir, "sca"); err != nil {
		w.logf("%s: feedback scan: %v", it.Short(), err)
		return scanFeedback{}, false
	}
	head, err := git(ctx, ws.run, ws.Dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return scanFeedback{}, false
	}
	res := scanartifacts.ReadReviewFindings(art, head)
	if !res.Covered[scanartifacts.KindSCA] {
		return scanFeedback{}, false
	}
	return composeFeedback(res, it.Finding, round, max), true
}

// removeScanArtefacts deletes the directory the feedback scan created, so the
// harness's commit of the worktree never carries scan output. A symlink is
// removed as a link, never followed.
func removeScanArtefacts(art string) {
	if fi, err := os.Lstat(art); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(art)
			return
		}
		_ = os.RemoveAll(art)
	}
}

// composeFeedback builds the harness's account of a scan for one finding.
func composeFeedback(res scanartifacts.ReviewResult, finding string, round, max int) scanFeedback {
	var hit *scanartifacts.ReviewFinding
	open := 0
	for i, f := range res.Findings {
		if f.Kind == scanartifacts.KindSCA {
			open++
		}
		if f.ID == finding {
			hit = &res.Findings[i]
		}
	}
	if hit == nil {
		return scanFeedback{}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Round %d of %d. The harness scanned your worktree after your changes and the scanner still reports %s.\n", round, max, hit.ID)
	for _, kv := range [][2]string{{"package", hit.Package}, {"ecosystem", hit.Ecosystem}, {"version", hit.Version}, {"file", hit.File}, {"severity", hit.Severity}} {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	fmt.Fprintf(&b, "findings still open in this worktree: %d\n", open)
	b.WriteString("Change your approach: work out why this version is still resolved (a lockfile, a second manifest, a transitive pin) and do not repeat an attempt that did not remove it.")
	return scanFeedback{present: true, text: b.String()}
}
