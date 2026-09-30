package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/kanban"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

// The delivery crew's Jev relevance jobs (internal/rolemanager/jev/delivery.go).
// They only narrow: a handoff may be sent to review, a gap card may be filed,
// and nothing is ever approved, marked met or moved toward backlog. A backend
// that is off, slow or unsure is an unknown answer, and the counted and
// deterministic rules run alone. Only titles and identifiers reach a backend.

// relevanceTimeout bounds one rating, so a slow backend never delays a turn.
const relevanceTimeout = 8 * time.Second

// jevJobs returns the decision-backend runner, built once from the worker's
// configuration; nil when no decision backend is configured.
func (w *Worker) jevJobs() *jev.Jobs {
	w.jevOnce.Do(func() {
		if w.Jev == nil {
			w.Jev = run.NewJevJobs(w.Cfg, w.Settings.JevJobSet)
		}
	})
	return w.Jev
}

// jevRelevance implements tools.HandoffRelevance over the worker's jobs.
type jevRelevance struct{ jobs *jev.Jobs }

func pct(v float64) int { return int(v*100 + 0.5) }

// Clarity rates a handoff's title and body.
func (r jevRelevance) Clarity(ctx context.Context, title, body string) (bool, bool) {
	if !r.jobs.Enabled(config.JevHandoffClarity) {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, relevanceTimeout)
	defer cancel()
	start := time.Now()
	scores, res, err := r.jobs.RateClarity(ctx, title, body)
	verdict, score := jev.PickClarity(scores)
	if err != nil || verdict == jev.ClarityUnknown {
		rolemanager.RecordHandoffClarity("unknown", -1, res.Identity, time.Since(start))
		return false, false
	}
	rolemanager.RecordHandoffClarity(string(verdict), pct(score), res.Identity, time.Since(start))
	return verdict == jev.ClarityUnclear, true
}

// Alignment rates each runnable gate of a handoff.
func (r jevRelevance) Alignment(ctx context.Context, gates []kanban.Gate) ([]string, bool) {
	if !r.jobs.Enabled(config.JevGateAlignment) {
		return nil, false
	}
	var refs []jev.GateRef
	for i, g := range gates {
		if g.Kind == kanban.GateRunnable {
			refs = append(refs, jev.GateRef{ID: fmt.Sprintf("G%d", i+1), Title: g.Title, Suite: g.Suite, Dir: g.Dir, Test: g.Test})
		}
	}
	if len(refs) == 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, relevanceTimeout)
	defer cancel()
	start := time.Now()
	scores, res, err := r.jobs.RateAlignment(ctx, refs)
	if err != nil || len(scores) == 0 {
		rolemanager.RecordGateAlignment("unknown", 0, len(refs), res.Identity, time.Since(start))
		return nil, false
	}
	flagged := jev.PickAlignment(scores, refs)
	verdict := "aligned"
	if len(flagged) > 0 {
		verdict = "flagged"
	}
	rolemanager.RecordGateAlignment(verdict, len(flagged), len(refs), res.Identity, time.Since(start))
	return flagged, true
}

// coverageSuspects returns the covered clauses whose covering tasks all rate as
// not doing what the clause says. The clause and task titles are the model's
// words and reach the backend only as cleaned DecisionText.
func (w *Worker) coverageSuspects(ctx context.Context, parent kanban.Item, children []kanban.Item) []string {
	jobs := w.jevJobs()
	if !jobs.Enabled(config.JevRequestCoverage) {
		return nil
	}
	text := map[string]string{}
	for _, c := range parent.Clauses {
		text[c.ID] = c.Text
	}
	var pairs []jev.CoverPair
	for _, ch := range children {
		if ch.Deleted {
			continue
		}
		for _, id := range ch.Covers {
			if t, ok := text[id]; ok {
				pairs = append(pairs, jev.CoverPair{Clause: id, Task: ch.Short(), Text: t, Title: ch.Title})
			}
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, relevanceTimeout)
	defer cancel()
	start := time.Now()
	scores, res, err := jobs.RateCoverage(ctx, pairs)
	if err != nil || len(scores) == 0 {
		rolemanager.RecordRequestCoverage("unknown", 0, len(parent.Clauses), res.Identity, time.Since(start))
		return nil
	}
	suspects := jev.PickCoverage(scores, pairs)
	verdict := "sound"
	if len(suspects) > 0 {
		verdict = "suspect"
	}
	rolemanager.RecordRequestCoverage(verdict, len(suspects), len(parent.Clauses), res.Identity, time.Since(start))
	return suspects
}

// joinIDs is a short list of ids for a note.
func joinIDs(ids []string) string { return strings.Join(ids, ", ") }
