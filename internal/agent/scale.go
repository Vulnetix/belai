package agent

import (
	"context"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// Request scale. A goal turn carries ceremony that pays for itself on a staged
// request and only delays a simple one: a contract draft by the fast model, a
// prefetch of every changed file, a planning list, a verification pass before
// the goal may end, and a test-suite verification surface. A decision backend
// rates the request once per turn; a simple verdict drops that ceremony and
// the goal loop still runs, so the evaluator still ends the goal.
//
// The job only removes ceremony. It never approves a call, never changes the
// mode the user chose, and never sees more than the sanitized prompt. Anything
// but a clear simple verdict (job off, no backend, a timeout, an unanswered or
// inconclusive score) leaves the ordinary behaviour untouched.

// scaleTimeout bounds the rating so a slow backend never delays the turn: the
// verdict is worth having only if it arrives before the work would start.
const scaleTimeout = 8 * time.Second

// rateScale sizes the turn's request and reports whether it is simple.
func (s *Session) rateScale(ctx context.Context, request string) bool {
	if s.jev == nil || !s.jev.Enabled(config.JevRequestScale) || request == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, scaleTimeout)
	defer cancel()
	start := time.Now()
	scores, res, err := s.jev.RateScale(ctx, request)
	if err != nil || len(scores) == 0 {
		rolemanager.RecordRequestScale("unknown", -1, res.Identity, time.Since(start))
		return false
	}
	verdict, score := jev.PickScale(scores)
	name := string(verdict)
	if verdict == jev.ScaleUnknown {
		name = "unknown"
	}
	rolemanager.RecordRequestScale(name, int(score*100), res.Identity, time.Since(start))
	return verdict == jev.ScaleSimple
}
