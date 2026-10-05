//go:build belai_sandbox

package clefmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/vulnetix/belai/internal/decisions"
)

// Common argument fields.
type base struct {
	Question string `json:"question"`
	Context  string `json:"context"`
}

type enumArgs struct {
	base
	Options      []string          `json:"options"`
	Descriptions map[string]string `json:"descriptions"`
	K            int               `json:"k"`
}

// decode reads tool arguments strictly: an unknown field is a mistake the
// caller should hear about, not something to ignore.
func decode(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalid("arguments: %v", err)
	}
	return nil
}

func needQuestion(q string) error {
	if text(q, maxQuestionRunes) == "" {
		return invalid("question is required")
	}
	return nil
}

// --- decide_boolean ---

type boolResult struct {
	Answer     bool    `json:"answer"`
	PTrue      float64 `json:"p_true"`
	Confidence float64 `json:"confidence"`
	Margin     float64 `json:"margin"`
	Threshold  float64 `json:"threshold"`
}

func boolOf(p, threshold float64) boolResult {
	conf := p
	if 1-p > conf {
		conf = round4(1 - p)
	}
	m := round4(p - (1 - p))
	if m < 0 {
		m = -m
	}
	return boolResult{Answer: p >= threshold, PTrue: p, Confidence: conf, Margin: m, Threshold: threshold}
}

func (s *Server) decideBoolean(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		base
		Threshold *float64 `json:"threshold"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if err := needQuestion(a.Question); err != nil {
		return nil, err
	}
	th := 0.5
	if a.Threshold != nil {
		th = *a.Threshold
	}
	if th <= 0 || th >= 1 {
		return nil, invalid("threshold must be above 0 and below 1")
	}
	ans, err := s.ask(ctx, stateFor(a.Context), map[string]decisions.Question{"q0": decisions.Noul(text(a.Question, maxQuestionRunes))})
	if err != nil {
		return nil, err
	}
	return boolOf(pBool(ans["q0"]), th), nil
}

// --- gate_decision ---

func (s *Server) gateDecision(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		base
		MinConfidence *float64 `json:"min_confidence"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if err := needQuestion(a.Question); err != nil {
		return nil, err
	}
	min := 0.8
	if a.MinConfidence != nil {
		min = *a.MinConfidence
	}
	if min <= 0.5 || min > 1 {
		return nil, invalid("min_confidence must be above 0.5 and at most 1")
	}
	ans, err := s.ask(ctx, stateFor(a.Context), map[string]decisions.Question{"q0": decisions.Noul(text(a.Question, maxQuestionRunes))})
	if err != nil {
		return nil, err
	}
	p := pBool(ans["q0"])
	verdict := "uncertain"
	switch {
	case p >= min:
		verdict = "pass"
	case 1-p >= min:
		verdict = "fail"
	}
	return map[string]any{"decision": verdict, "p_true": p, "min_confidence": min}, nil
}

// --- the choice tools ---

// choice asks one choice question and returns the options and their weights.
func (s *Server) choice(ctx context.Context, a enumArgs) (options, []float64, error) {
	if err := needQuestion(a.Question); err != nil {
		return options{}, nil, err
	}
	o, err := newOptions(a.Options, a.Descriptions)
	if err != nil {
		return options{}, nil, err
	}
	ans, err := s.ask(ctx, stateFor(a.Context), map[string]decisions.Question{"q0": o.question(a.Question)})
	if err != nil {
		return options{}, nil, err
	}
	w, err := o.weights(ans["q0"])
	return o, w, err
}

func (s *Server) decideEnum(ctx context.Context, raw json.RawMessage) (any, error) {
	var a enumArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	o, w, err := s.choice(ctx, a)
	if err != nil {
		return nil, err
	}
	conf, margin := confidence(w)
	return map[string]any{"choice": o.orig[order(w)[0]], "weights": o.inOrder(w), "confidence": conf, "margin": margin}, nil
}

func (s *Server) weighOptions(ctx context.Context, raw json.RawMessage) (any, error) {
	var a enumArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	o, w, err := s.choice(ctx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"weights": o.inOrder(w)}, nil
}

func (s *Server) rankOptions(ctx context.Context, raw json.RawMessage) (any, error) {
	var a enumArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	o, w, err := s.choice(ctx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ranking": o.ranking(w, 0)}, nil
}

func (s *Server) topK(ctx context.Context, raw json.RawMessage) (any, error) {
	var a enumArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.K < 1 || a.K >= len(a.Options) {
		return nil, invalid("k must be at least 1 and less than the number of options (%d)", len(a.Options))
	}
	o, w, err := s.choice(ctx, a)
	if err != nil {
		return nil, err
	}
	return map[string]any{"top": o.ranking(w, a.K), "k": a.K, "omitted": len(a.Options) - a.K}, nil
}

func (s *Server) comparePair(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		base
		A string `json:"a"`
		B string `json:"b"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	o, w, err := s.choice(ctx, enumArgs{base: a.base, Options: []string{a.A, a.B}})
	if err != nil {
		return nil, err
	}
	winner, pick := "a", a.A
	if w[1] > w[0] {
		winner, pick = "b", a.B
	}
	_, margin := confidence(w)
	return map[string]any{
		"winner": winner, "winner_option": pick, "tie": w[0] == w[1],
		"weights": map[string]float64{"a": w[0], "b": w[1]}, "margin": margin, "options": o.orig,
	}, nil
}

// --- rank_pairwise ---

type pairwiseItem struct {
	Rank   int     `json:"rank"`
	Item   string  `json:"item"`
	Wins   int     `json:"wins"`
	Points float64 `json:"points"`
}

func (s *Server) rankPairwise(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		base
		Items []string `json:"items"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if err := needQuestion(a.Question); err != nil {
		return nil, err
	}
	n := len(a.Items)
	if n < 2 || n > MaxPairwiseItems {
		return nil, invalid("items needs 2 to %d entries, got %d", MaxPairwiseItems, n)
	}
	o, err := newOptions(a.Items, nil)
	if err != nil {
		return nil, err
	}
	qs := map[string]decisions.Question{}
	type pair struct{ i, j int }
	pairs := map[string]pair{}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			id := fmt.Sprintf("p%d_%d", i, j)
			pairs[id] = pair{i, j}
			qs[id] = decisions.Noul(fmt.Sprintf("%s Is %q the better choice than %q?", text(a.Question, maxQuestionRunes), o.shown[i], o.shown[j]))
		}
	}
	ans, err := s.ask(ctx, stateFor(a.Context), qs)
	if err != nil {
		return nil, err
	}
	wins := make([]int, n)
	points := make([]float64, n)
	for id, p := range pairs {
		pt := pBool(ans[id])
		points[p.i] += pt
		points[p.j] += 1 - pt
		if pt >= 0.5 {
			wins[p.i]++
		} else {
			wins[p.j]++
		}
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool {
		a, b := idx[x], idx[y]
		if wins[a] != wins[b] {
			return wins[a] > wins[b]
		}
		return round4(points[a]) > round4(points[b])
	})
	out := make([]pairwiseItem, n)
	for r, i := range idx {
		out[r] = pairwiseItem{Rank: r + 1, Item: o.orig[i], Wins: wins[i], Points: round4(points[i])}
	}
	return map[string]any{"ranking": out, "comparisons": len(pairs)}, nil
}

// --- decide_batch ---

type batchQuestion struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
}

func (s *Server) decideBatch(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Context   string          `json:"context"`
		Questions []batchQuestion `json:"questions"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if len(a.Questions) == 0 || len(a.Questions) > MaxBatch {
		return nil, invalid("questions needs 1 to %d entries, got %d", MaxBatch, len(a.Questions))
	}
	qs := map[string]decisions.Question{}
	opts := map[string]options{}
	ids := map[string]bool{}
	for i, q := range a.Questions {
		if q.ID == "" || len([]rune(q.ID)) > maxIDRunes || ids[q.ID] {
			return nil, invalid("question %d needs a unique id of at most %d characters", i, maxIDRunes)
		}
		ids[q.ID] = true
		if err := needQuestion(q.Question); err != nil {
			return nil, invalid("question %q: %v", q.ID, err)
		}
		key := fmt.Sprintf("q%d", i)
		switch q.Type {
		case "boolean":
			qs[key] = decisions.Noul(text(q.Question, maxQuestionRunes))
		case "enum":
			o, err := newOptions(q.Options, nil)
			if err != nil {
				return nil, invalid("question %q: %v", q.ID, err)
			}
			opts[key] = o
			qs[key] = o.question(q.Question)
		default:
			return nil, invalid("question %q: type must be boolean or enum", q.ID)
		}
	}
	ans, err := s.ask(ctx, stateFor(a.Context), qs)
	if err != nil {
		return nil, err
	}
	out := make([]any, len(a.Questions))
	for i, q := range a.Questions {
		key := fmt.Sprintf("q%d", i)
		if q.Type == "boolean" {
			out[i] = map[string]any{"id": q.ID, "type": "boolean", "result": boolOf(pBool(ans[key]), 0.5)}
			continue
		}
		o := opts[key]
		w, err := o.weights(ans[key])
		if err != nil {
			return nil, err
		}
		conf, margin := confidence(w)
		out[i] = map[string]any{"id": q.ID, "type": "enum", "result": map[string]any{
			"choice": o.orig[order(w)[0]], "weights": o.inOrder(w), "confidence": conf, "margin": margin,
		}}
	}
	return map[string]any{"answers": out}, nil
}
