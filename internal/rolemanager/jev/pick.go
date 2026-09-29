package jev

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/sanitize"
)

// MaxPickOptions is the most options one Pick may rank: the local model reads
// options as the letters A to P.
const MaxPickOptions = 16

// PickRequest asks which of a few options fits best.
type PickRequest struct {
	// Job names the caller for records.
	Job string
	// Question is the shared question, for example which option the user is
	// most likely to choose. It is harness text passed as DecisionText.
	Question sanitize.DecisionText
	// Context is what the options are judged against.
	Context sanitize.DecisionText
	// Options are the candidates; each ID must be unique and identifier-safe.
	Options []ScoreItem
}

// PickResult holds one probability per option, summing to 1.
type PickResult struct {
	Probs    map[string]float64
	Best     string
	Identity string
	Backend  decisions.Backend
	Requests int
	Latency  time.Duration
}

// ErrPickOptions is returned for fewer than two or more than MaxPickOptions
// options, or duplicate ids.
var ErrPickOptions = errors.New("pick needs between 2 and 16 distinct options")

// Pick ranks a small set of options. The local model and a self-hosted server
// answer one choice question; the hosted OpenRouter API answers noul
// questions only, so each option is scored on its own and the scores are
// normalised. Either way the caller sees a probability per option.
func (c *Client) Pick(ctx context.Context, req PickRequest) (PickResult, error) {
	start := time.Now()
	out := PickResult{Identity: c.Identity(), Backend: c.backendKind(), Probs: map[string]float64{}}
	if n := len(req.Options); n < 2 || n > MaxPickOptions {
		return out, ErrPickOptions
	}
	seen := map[string]bool{}
	for _, o := range req.Options {
		if o.ID == "" || seen[o.ID] || sanitize.Ident(o.ID, 0) != o.ID {
			return out, ErrPickOptions
		}
		seen[o.ID] = true
	}

	if out.Backend == decisions.BackendOpenRouter {
		sr, err := c.Score(ctx, ScoreRequest{
			Job:       req.Job,
			Criterion: req.Question,
			Context:   req.Context,
			Items:     req.Options,
		})
		out.Requests = sr.Requests
		if err != nil {
			return out, err
		}
		if len(sr.Unknown) > 0 {
			return out, fmt.Errorf("jev: %d of %d options unanswered", len(sr.Unknown), len(req.Options))
		}
		total := 0.0
		for _, s := range sr.Scores {
			total += s
		}
		for id, s := range sr.Scores {
			if total > 0 {
				out.Probs[id] = s / total
			} else {
				out.Probs[id] = 1 / float64(len(req.Options))
			}
		}
	} else {
		q := decisions.Question{
			Type:         decisions.TypeChoice,
			Instructions: req.Question.String(),
			Descriptions: map[string]string{},
		}
		for _, o := range req.Options {
			q.Options = append(q.Options, o.ID)
			q.Descriptions[o.ID] = o.Label.String()
		}
		answers, err := c.decide(ctx, decisions.Request{
			State:     map[string]any{"context": req.Context.String()},
			Questions: map[string]decisions.Question{"pick": q},
		})
		out.Requests = 1
		if err != nil {
			return out, err
		}
		a, ok := answers["pick"]
		if !ok || a.Type != decisions.TypeChoice {
			return out, errors.New("jev: no choice answer")
		}
		total := 0.0
		for _, o := range req.Options {
			p := a.Probabilities[o.ID]
			if p < 0 || p > 1 {
				return out, errors.New("jev: option probability out of range")
			}
			out.Probs[o.ID] = p
			total += p
		}
		if total <= 0 {
			return out, errors.New("jev: no probability mass on the options")
		}
		for id := range out.Probs {
			out.Probs[id] /= total
		}
	}

	ids := make([]string, 0, len(out.Probs))
	for id := range out.Probs {
		ids = append(ids, id)
	}
	// Highest probability first; ties keep the caller's order.
	order := map[string]int{}
	for i, o := range req.Options {
		order[o.ID] = i
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if out.Probs[ids[i]] != out.Probs[ids[j]] {
			return out.Probs[ids[i]] > out.Probs[ids[j]]
		}
		return order[ids[i]] < order[ids[j]]
	})
	if len(ids) > 0 {
		out.Best = ids[0]
	}
	out.Latency = time.Since(start)
	return out, nil
}
