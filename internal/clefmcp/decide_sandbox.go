//go:build belai_sandbox

package clefmcp

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/sanitize"
)

// Input limits. The model only ever sees sanitized text, capped here.
const (
	maxQuestionRunes = 2000
	maxContextRunes  = 8000
	maxOptionRunes   = 200
	maxIDRunes       = 64
	// MaxPairwiseItems is the most items rank_pairwise orders: n(n-1)/2 pairs
	// must fit one request of decisions.ClefMaxQuestions questions (11 items
	// make 55 pairs, 12 would make 66).
	MaxPairwiseItems = 11
	// MaxBatch is the most questions decide_batch asks in one call.
	MaxBatch = decisions.ClefMaxQuestions
)

// toolError is a failure the tool reports to the caller as its result.
type toolError struct {
	Code string
	Msg  string
}

func (e *toolError) Error() string { return e.Code + ": " + e.Msg }

func invalid(format string, args ...any) error {
	return &toolError{Code: "invalid", Msg: fmt.Sprintf(format, args...)}
}

// backendError maps a decision failure to a tool error. Only the class is
// reported: backend text never reaches the model.
func backendError(err error) error {
	switch {
	case decisions.IsUnavailable(err):
		return &toolError{Code: "unavailable", Msg: "the decision model did not answer; retry later or decide another way"}
	case decisions.ClassOf(err) == decisions.ClassAuth:
		return &toolError{Code: "auth", Msg: "the decision model refused the sandbox credential"}
	case decisions.ClassOf(err) == decisions.ClassSchema:
		return &toolError{Code: "schema", Msg: "the decision model could not take this request"}
	}
	return &toolError{Code: "error", Msg: "the decision call failed"}
}

// round4 is the one rounding every number in a result goes through.
func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

// text cleans a caller string for a decision request.
func text(s string, max int) string {
	return strings.TrimSpace(sanitize.ForDecision(s, max).String())
}

// stateFor is the request state: the caller's context, cleaned. Clef needs a
// non-empty state, so an absent context is a fixed neutral line.
func stateFor(context string) map[string]any {
	c := text(context, maxContextRunes)
	if c == "" {
		c = "No further context."
	}
	return map[string]any{"context": c}
}

// options is a cleaned option list: what the model sees, and the caller's own
// strings for the result.
type options struct {
	shown []string
	orig  []string
	desc  map[string]string
}

// newOptions checks and cleans opts (and their optional descriptions, keyed by
// the caller's option). It needs at least two options, at most
// decisions.ClefMaxOptions, none empty and none the same once cleaned.
func newOptions(opts []string, descriptions map[string]string) (options, error) {
	if len(opts) < 2 {
		return options{}, invalid("options needs at least 2 entries")
	}
	if len(opts) > decisions.ClefMaxOptions {
		return options{}, invalid("options has %d entries, the most is %d", len(opts), decisions.ClefMaxOptions)
	}
	o := options{orig: opts, shown: make([]string, len(opts))}
	seen := map[string]bool{}
	for i, s := range opts {
		c := text(s, maxOptionRunes)
		if c == "" {
			return options{}, invalid("option %d is empty", i)
		}
		if seen[c] {
			return options{}, invalid("option %d repeats an earlier option", i)
		}
		seen[c] = true
		o.shown[i] = c
	}
	for k, v := range descriptions {
		for i, s := range opts {
			if s == k {
				if o.desc == nil {
					o.desc = map[string]string{}
				}
				o.desc[o.shown[i]] = text(v, maxQuestionRunes)
			}
		}
	}
	return o, nil
}

func (o options) question(q string) decisions.Question {
	return decisions.Question{Type: decisions.TypeChoice, Instructions: text(q, maxQuestionRunes), Options: o.shown, Descriptions: o.desc}
}

// weights returns one weight per option, in input order, normalised to sum to
// 1 and rounded. A choice answer that holds no probability is an error.
func (o options) weights(a decisions.Answer) ([]float64, error) {
	w := make([]float64, len(o.shown))
	sum := 0.0
	for i, s := range o.shown {
		w[i] = a.Probabilities[s]
		sum += w[i]
	}
	if sum <= 0 {
		return nil, &toolError{Code: "error", Msg: "the decision model gave no weight to any option"}
	}
	for i := range w {
		w[i] = round4(w[i] / sum)
	}
	return w, nil
}

// ask sends one request and returns its answers.
func (s *Server) ask(ctx context.Context, state map[string]any, qs map[string]decisions.Question) (map[string]decisions.Answer, error) {
	if len(qs) == 0 || len(qs) > decisions.ClefMaxQuestions {
		return nil, invalid("a request takes 1 to %d questions", decisions.ClefMaxQuestions)
	}
	d, err := s.decider()
	if err != nil {
		return nil, &toolError{Code: "unavailable", Msg: err.Error()}
	}
	res, err := d.Decide(ctx, decisions.Request{State: state, Questions: qs})
	if err != nil {
		return nil, backendError(err)
	}
	return res.Answers, nil
}

// pBool is P(true) of one noul answer, rounded.
func pBool(a decisions.Answer) float64 { return round4(a.Noul) }

// Weighted is one option with its weight.
type Weighted struct {
	Option string  `json:"option"`
	Weight float64 `json:"weight"`
}

// Ranked is one option in an ordering.
type Ranked struct {
	Rank   int     `json:"rank"`
	Option string  `json:"option"`
	Weight float64 `json:"weight"`
}

// inOrder pairs each option with its weight, in input order.
func (o options) inOrder(w []float64) []Weighted {
	out := make([]Weighted, len(w))
	for i := range w {
		out[i] = Weighted{Option: o.orig[i], Weight: w[i]}
	}
	return out
}

// order returns the option indexes by weight, heaviest first. Equal weights
// keep their input order, so the same answers always give the same ordering.
func order(w []float64) []int {
	idx := make([]int, len(w))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return w[idx[a]] > w[idx[b]] })
	return idx
}

// ranking turns weights into a ranked list, best first, truncated to k (0 for
// all).
func (o options) ranking(w []float64, k int) []Ranked {
	idx := order(w)
	if k > 0 && k < len(idx) {
		idx = idx[:k]
	}
	out := make([]Ranked, len(idx))
	for r, i := range idx {
		out[r] = Ranked{Rank: r + 1, Option: o.orig[i], Weight: w[i]}
	}
	return out
}

// confidence and margin describe the heaviest option: its weight, and its lead
// over the runner-up.
func confidence(w []float64) (conf, margin float64) {
	idx := order(w)
	conf = w[idx[0]]
	margin = round4(w[idx[0]] - w[idx[1]])
	return conf, margin
}
