package jev

import (
	"context"
	"slices"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// The delivery crew's relevance jobs. Each one only narrows: it can send a
// handoff to review, flag a gate or file a gap card, and it can never move a
// card out of review, mark a gate met or a clause covered, or skip a check the
// harness runs. A score it does not get is unknown, never a low score, and the
// deterministic behaviour it sits on runs unchanged. What a backend sees is
// DecisionText built from the task or gate's own title and identifiers, never
// file contents, test output or attachments.

// Clarity is the verdict of RateClarity.
type Clarity string

// The clarity verdicts.
const (
	ClarityUnknown Clarity = "unknown"
	ClarityClear   Clarity = "clear"
	ClarityUnclear Clarity = "unclear"
)

const clarityCriterion = "The task in state.context is specific enough that an agent can start it without asking a question: it says what to change, where, and how to tell it is done."

const clarityItem = "clear"

// RateClarity rates one handoff's title and body as clear enough to start.
func (j *Jobs) RateClarity(ctx context.Context, title, body string) (map[string]float64, ScoreResult, error) {
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevHandoffClarity),
		Criterion:   sanitize.ForDecision(clarityCriterion, 0),
		Context:     sanitize.ForDecision(title+"\n\n"+body, 1500),
		Items:       []ScoreItem{{ID: clarityItem, Label: sanitize.ForDecision("A clear task an agent can start at once.", 120)}},
		MaxRequests: 2,
	})
	return res.Scores, res, err
}

// PickClarity turns the score into a verdict. A task is unclear only when the
// backend answered and rated it below clear_at; an unanswered item is unknown.
// Clear here means only "no objection": it never moves a task out of review.
func PickClarity(scores map[string]float64) (Clarity, float64) {
	v, ok := scores[clarityItem]
	switch {
	case !ok:
		return ClarityUnknown, 0
	case v < config.ActiveJevThresholds().ClearAt:
		return ClarityUnclear, v
	}
	return ClarityClear, v
}

// GateRef is a runnable gate as a backend is told about it: its own title and
// the identifiers of the test that would decide it.
type GateRef struct {
	ID, Title, Suite, Dir, Test string
}

const alignCriterion = "The outcome named in each item is something the test named in the same item would show to be true or false."

// RateAlignment rates whether each gate's test would show its stated outcome.
func (j *Jobs) RateAlignment(ctx context.Context, gates []GateRef) (map[string]float64, ScoreResult, error) {
	items := make([]ScoreItem, 0, len(gates))
	for _, g := range gates {
		test := strings.TrimSpace(g.Suite + " " + g.Dir + " " + g.Test)
		items = append(items, ScoreItem{ID: g.ID, Label: sanitize.ForDecision("outcome: "+g.Title+" | test: "+test, 300)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevGateAlignment),
		Criterion:   sanitize.ForDecision(alignCriterion, 0),
		Items:       items,
		MaxRequests: 4,
	})
	return res.Scores, res, err
}

// PickAlignment returns the ids of the gates the backend rated below
// align_at, in ledger order. A gate it did not answer is not returned: unknown
// is never a low score.
func PickAlignment(scores map[string]float64, gates []GateRef) []string {
	at := config.ActiveJevThresholds().AlignAt
	var out []string
	for _, g := range gates {
		if v, ok := scores[g.ID]; ok && v < at {
			out = append(out, g.ID)
		}
	}
	return out
}

// CoverPair is one request clause and one task that says it covers it.
type CoverPair struct {
	Clause string // clause id, C1
	Task   string // an id for the covering task, unique within the request
	Text   string // the clause text
	Title  string // the task's title
}

func (p CoverPair) key() string { return p.Clause + "/" + p.Task }

const coverCriterion = "The task named in each item would accomplish the request part named in the same item."

// RateCoverage rates whether each covering task does what its clause says.
func (j *Jobs) RateCoverage(ctx context.Context, pairs []CoverPair) (map[string]float64, ScoreResult, error) {
	items := make([]ScoreItem, 0, len(pairs))
	for _, p := range pairs {
		items = append(items, ScoreItem{ID: p.key(), Label: sanitize.ForDecision("request part: "+p.Text+" | task: "+p.Title, 400)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevRequestCoverage),
		Criterion:   sanitize.ForDecision(coverCriterion, 0),
		Items:       items,
		MaxRequests: 8,
	})
	return res.Scores, res, err
}

// PickCoverage returns the clause ids whose covering tasks the backend rated,
// every one of them, below cover_at. A clause with any unanswered pair is not
// returned: the harness's own rule (a covering handoff exists) stands.
func PickCoverage(scores map[string]float64, pairs []CoverPair) []string {
	at := config.ActiveJevThresholds().CoverAt
	type tally struct{ n, low, unknown int }
	by := map[string]*tally{}
	var order []string
	for _, p := range pairs {
		t := by[p.Clause]
		if t == nil {
			t = &tally{}
			by[p.Clause] = t
			order = append(order, p.Clause)
		}
		t.n++
		if v, ok := scores[p.key()]; !ok {
			t.unknown++
		} else if v < at {
			t.low++
		}
	}
	var out []string
	for _, c := range order {
		if t := by[c]; t.n > 0 && t.unknown == 0 && t.low == t.n && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}
