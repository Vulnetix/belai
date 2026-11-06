package agent

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/finishreview"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testdetect"
	"github.com/vulnetix/belai/internal/workdiff"
)

// The finish review gate (docs/finish-review.md): after the verification gate
// has let a GOAL_COMPLETE through, the harness reads the working tree itself and
// may send the completion back once or twice with what it found. No model is
// asked whether a file is scratch or a test was weakened; the findings are
// closed kinds, plain-alphabet paths and counts, composed by finishreview.

// finishReviewDirective frames the findings the model is asked to resolve
// before the goal is accepted. The lines are harness text (finishreview.Finding.Line):
// a path appears only if it is made of plain characters.
const finishReviewDirective = "Finish review: before this goal is accepted as complete, the harness read the working tree itself and found the points below. This is a check of the files, not an opinion. Resolve each with your ordinary tools: remove scratch files you created, restore a test you weakened or say why the change is intended, and run the project's checks after your last change. A point you leave must be named in your reply with the reason. Then finish."

// finishReviewOpenDirective is the closing note when findings stay open: the
// rounds are spent, or the tree could not be read.
const finishReviewOpenDirective = "Finish review: the harness found the points below and the goal is being accepted anyway. Name each one plainly in your report, and say whether it is intended."

// reviewLines renders a report as the bullet list a directive carries.
func reviewLines(r finishreview.Report) string {
	var b strings.Builder
	for _, f := range r.Findings {
		b.WriteString("\n- " + f.Line())
	}
	if r.More > 0 {
		b.WriteString("\n- and more findings not listed")
	}
	return b.String()
}

// actionable reports whether the report holds a finding the model can fix:
// anything but "part of the tree was not reviewed".
func actionable(r finishreview.Report) bool {
	if r.More > 0 {
		return true
	}
	for _, f := range r.Findings {
		if f.Kind != finishreview.KindUnreviewed {
			return true
		}
	}
	return false
}

// finishReviewActive reports whether the goal about to run is reviewed at its
// end: the gate is on, the goal is the main loop's own (not a decision turn or
// a request rated simple) and there is a working directory.
func (s *Session) finishReviewActive(l *passLedger) bool {
	return s.settings.FinishReviewEnabled() && !l.simple && !l.decision && s.workdir != ""
}

func (s *Session) finishReviewHide() func(string) bool {
	perms, workdir := s.perms, s.workdir
	return func(rel string) bool { return workdiff.ReadDenied(perms, workdir, rel) }
}

// finishReviewStart records which files were already changed when the goal
// began, so the user's own untracked files are never called scratch. A goal
// that resumes reads the baseline its first run saved; a baseline that cannot
// be read leaves every untracked file counting as new, the stricter reading.
// goalID names the baseline file and is a harness-made identifier.
func (s *Session) finishReviewStart(ctx context.Context, l *passLedger, goalID string, resumed bool) {
	if !s.finishReviewActive(l) {
		return
	}
	l.reviewing = true
	file, err := config.FinishReviewFile(goalID)
	if resumed {
		if err == nil {
			l.baseline = finishreview.LoadBaseline(file)
		}
		return
	}
	l.baseline = finishreview.Capture(ctx, s.workdir, s.finishReviewHide())
	if err == nil && l.baseline.Complete {
		// Best effort: a baseline that is not saved only costs a resumed goal
		// the stricter reading.
		_ = l.baseline.Save(file)
	}
}

// finishReviewGate runs the review at a point where the goal would otherwise be
// accepted. It returns a directive when the completion should go back to the
// model (a round is spent), and otherwise the findings that stay open, to be put
// in the final report. Both are empty when the review is off, clean or skipped.
func (s *Session) finishReviewGate(ctx context.Context, l *passLedger) (directive string, open finishreview.Report) {
	if !l.reviewing {
		return "", finishreview.Report{}
	}
	var suites []string
	for _, su := range testdetect.Detect(s.workdir, s.allTestCommands(), l.touched) {
		suites = append(suites, su.Name)
	}
	rep := finishreview.Review(ctx, finishreview.Options{
		Workdir:         s.workdir,
		Hide:            s.finishReviewHide(),
		Baseline:        l.baseline,
		Verified:        l.checked || l.writes == 0,
		Suites:          suites,
		ScratchPatterns: s.settings.FinishReviewScratchPatterns(),
	})
	if rep.Empty() {
		return "", rep
	}
	// A tree that could not be read is for the report and the user: there is
	// nothing for the model to fix, so it never costs a round.
	if actionable(rep) && l.reviewRounds < s.settings.FinishReviewMaxRounds() {
		l.reviewRounds++
		return finishReviewDirective + reviewLines(rep), finishreview.Report{}
	}
	return "", rep
}

// finishReviewOpenTurns is the directive that carries still-open findings into
// the final report turn.
func finishReviewOpenTurns(open finishreview.Report) string {
	return finishReviewOpenDirective + reviewLines(open)
}

// finishReviewWarning is the one line shown to the user when findings stay open.
func finishReviewWarning(open finishreview.Report) string {
	n := len(open.Findings) + open.More
	if n == 1 {
		return "finish review: 1 point left open; " + open.Findings[0].Line()
	}
	return "finish review: " + strconv.Itoa(n) + " points left open (see the report)"
}

// withReviewOpen appends the still-open findings, as a sealed harness
// directive, to the turns the final report is written from. With none it
// returns turns unchanged.
func (s *Session) withReviewOpen(turns []run.Turn, open finishreview.Report) []run.Turn {
	if open.Empty() {
		return turns
	}
	return append(slices.Clone(turns), directiveTurns(finishReviewOpenTurns(open))...)
}
