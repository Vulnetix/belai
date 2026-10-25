package agent

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/run"
)

// CoordEvent is what the forge coordinator did with a request a fleet worker
// filed when it could not publish its branch itself.
type CoordEvent string

// Forge coordinator events (the steer dispatch's "event").
const (
	CoordTookOver CoordEvent = "took_over"
	CoordWaiting  CoordEvent = "waiting"
	CoordFailed   CoordEvent = "failed"
	// CoordFinalize is the fleet worker's own harness telling the goal loop that
	// a per-item budget is nearly spent: finalise now. Not a coordinator event.
	CoordFinalize CoordEvent = "finalize"
)

// FinalizeReasons are the budgets a finalise fact may name.
var FinalizeReasons = map[string]bool{"cost": true, "tokens": true, "passes": true, "wall": true}

// FinalizeReservePasses is how many passes a worker gets after it is told to
// finalise at a pass ceiling.
const FinalizeReservePasses = 2

// NewFinalizeFact is the fact that tells a worker to finalise because the
// budget named by reason (a FinalizeReasons word) is nearly spent.
func NewFinalizeFact(reason string) (CoordFact, error) {
	if !FinalizeReasons[reason] {
		return CoordFact{}, errors.New("not a budget")
	}
	return CoordFact{event: CoordFinalize, reason: reason}, nil
}

// finalizeText is the fixed finalise directive for a budget word.
func finalizeText(reason string) string {
	return "Budget: this item's " + reason + " budget is nearly spent, and the claim ends when it is. Finalise now, in this pass: stop investigating and do not start new work. " +
		"Commit what you have. Record on the item with KanbanUpdate what you found, what changed, what remains and the exact next step, so the next claim resumes from it; if you have tools for the item's linked issue, update it too. " +
		"If the branch has changes and you may publish, call PublishBranch (a draft pull request is fine; say in its description what is unfinished). " +
		"Then record the outcome with KanbanOutcome if you have it: success only when the work is done and verified, otherwise end the turn with the summary. Unfinished work is kept and resumed; it is not a failure."
}

// CoordReasons is the coordinator's reason vocabulary (BelaiForgeRequest.reason).
// A fact carries one of these words, never the coordinator's own text.
var CoordReasons = map[string]bool{
	"pushed": true, "pr_opened": true, "pr_found": true, "non_fast_forward": true, "branch_mismatch": true,
	"empty_bundle": true, "bundle_missing": true, "repo_not_attached": true, "app_not_installed": true,
	"token_denied": true, "expired": true, "rate_limited": true, "server": true,
}

// CoordFact is one forge coordinator outcome as the harness states it to the
// model. Its fields are unexported so a fact can only be built by NewCoordFact,
// which accepts an enum, integers and one link it checks; the only text the
// model ever sees is Text, filled from fixed templates.
type CoordFact struct {
	event  CoordEvent
	pr     int
	prURL  string
	until  time.Time
	reason string
}

// maxCoordPR bounds a pull request number.
const maxCoordPR = 1 << 30

// urlPathSafe is what a pull request link's path may hold.
var urlPathSafe = regexp.MustCompile(`^(/[A-Za-z0-9._-]{1,100}){1,8}$`)

// hostSafe is a plain DNS host name.
var hostSafe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// CleanPRURL returns u when it is a plain https link to pull request pr: no
// credentials, port, query or fragment, a host name of DNS characters, a path
// of identifier segments that ends in the number. Anything else is refused, so
// no free text can ride into a fact on the link.
func CleanPRURL(u string, pr int) (string, bool) {
	if len(u) > 300 || pr <= 0 {
		return "", false
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme != "https" || p.User != nil || p.Opaque != "" || p.RawQuery != "" || p.Fragment != "" ||
		p.Port() != "" || p.RawPath != "" || strings.HasSuffix(u, "?") || strings.HasSuffix(u, "#") {
		return "", false
	}
	if !hostSafe.MatchString(p.Host) || !urlPathSafe.MatchString(p.Path) {
		return "", false
	}
	if !strings.HasSuffix(p.Path, "/"+strconv.Itoa(pr)) {
		return "", false
	}
	return "https://" + p.Host + p.Path, true
}

// NewCoordFact checks a coordinator outcome and returns it as a fact: took_over
// needs a pull request number and its link, waiting a time (ms epoch), failed a
// reason word. A reason, when given, must be a CoordReasons word.
func NewCoordFact(event string, pr int, prURL string, until int64, reason string) (CoordFact, error) {
	if reason != "" && !CoordReasons[reason] {
		return CoordFact{}, errors.New("not a coordinator reason")
	}
	if pr < 0 || pr > maxCoordPR || until < 0 {
		return CoordFact{}, errors.New("a number is out of range")
	}
	f := CoordFact{event: CoordEvent(event), reason: reason}
	switch f.event {
	case CoordTookOver:
		clean, ok := CleanPRURL(prURL, pr)
		if !ok {
			return CoordFact{}, errors.New("took_over needs a pull request number and its https link")
		}
		f.pr, f.prURL = pr, clean
	case CoordWaiting:
		if until == 0 {
			return CoordFact{}, errors.New("waiting needs the time the rate limit lifts")
		}
		f.until = time.UnixMilli(until).UTC()
	case CoordFailed:
		if reason == "" {
			return CoordFact{}, errors.New("failed needs a reason")
		}
	default:
		return CoordFact{}, errors.New("not a coordinator event")
	}
	return f, nil
}

// Event is the fact's event; "" for the zero fact.
func (f CoordFact) Event() CoordEvent { return f.event }

// PR is the pull request number of a took_over fact.
func (f CoordFact) PR() int { return f.pr }

// PRURL is the checked link of a took_over fact.
func (f CoordFact) PRURL() string { return f.prURL }

// Until is when a waiting fact's rate limit lifts.
func (f CoordFact) Until() time.Time { return f.until }

// Reason is the fact's reason word, if any.
func (f CoordFact) Reason() string { return f.reason }

// Text is the fact as the model reads it: one fixed template per event.
func (f CoordFact) Text() string {
	switch f.event {
	case CoordTookOver:
		return fmt.Sprintf("coordinator took over publishing: draft pull request #%d is open (%s)", f.pr, f.prURL)
	case CoordWaiting:
		return fmt.Sprintf("coordinator is waiting for the GitHub rate limit until %s; stay paused, do not retry publishing", f.until.Format(time.RFC3339))
	case CoordFailed:
		return fmt.Sprintf("coordinator could not publish: %s", f.reason)
	case CoordFinalize:
		return finalizeText(f.reason)
	}
	return ""
}

// factBuffer is the coordinator fact queue's capacity.
const factBuffer = 4

// maxCoordWait bounds how long one waiting fact holds the goal loop. The
// coordinator's own retry time is a rate limit's reset, under an hour; the
// item's wall budget ends a wait sooner.
const maxCoordWait = time.Hour

// Fact queues a forge coordinator fact for the goal loop's next pass boundary.
// Facts are harness facts, not user text: they never pass Steer's admission
// and are framed as a sealed directive. It returns false for the zero fact or
// a full queue.
func (s *Session) Fact(f CoordFact) bool {
	if f.event == "" {
		return false
	}
	select {
	case s.facts <- f:
		return true
	default:
		return false
	}
}

// takeFact returns the next queued fact, without blocking.
func (s *Session) takeFact() (CoordFact, bool) {
	select {
	case f := <-s.facts:
		return f, true
	default:
		return CoordFact{}, false
	}
}

// coordBoundary applies the queued coordinator facts at a goal pass boundary.
// Each fact becomes a directive. took_over ends the loop (stop is true and the
// stop reason is run.StopCoordinated). waiting blocks here, before the pass is
// counted and before any stall counter sees it, until the rate limit lifts, the
// context ends or another fact arrives. failed is stated and the loop goes on.
// err is the context's error when it ended during a wait.
func (s *Session) coordBoundary(ctx context.Context, turns []run.Turn, emit func(Event)) (_ []run.Turn, stop bool, err error) {
	f, ok := s.takeFact()
	for ok {
		turns = append(turns, directiveTurns(f.Text())...)
		switch f.event {
		case CoordTookOver:
			emit(Event{Kind: EventWarningKind, Warning: "goal stopped: the forge coordinator took over publishing this branch"})
			return turns, true, nil
		case CoordFinalize:
			s.finalizing = true
			emit(Event{Kind: EventWarningKind, Warning: "finalising: the item's " + f.reason + " budget is nearly spent"})
		case CoordWaiting:
			emit(Event{Kind: EventWarningKind, Warning: "waiting for the forge coordinator: GitHub rate limit until " + f.until.Format(time.RFC3339)})
			next, err := s.waitCoordinator(ctx, f.until)
			if err != nil {
				return turns, false, err
			}
			if next.event != "" {
				f = next
				continue
			}
		}
		f, ok = s.takeFact()
	}
	return turns, false, nil
}

// waitCoordinator blocks until until (at most maxCoordWait), the context ends,
// or another fact arrives, which it returns.
func (s *Session) waitCoordinator(ctx context.Context, until time.Time) (CoordFact, error) {
	d := min(time.Until(until), maxCoordWait)
	if d <= 0 {
		return CoordFact{}, ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return CoordFact{}, ctx.Err()
	case <-t.C:
		return CoordFact{}, nil
	case f := <-s.facts:
		return f, nil
	}
}
