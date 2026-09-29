package jev

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/sanitize"
)

func dt(s string) sanitize.DecisionText { return sanitize.ForDecision(s, 0) }

func items(n int) []ScoreItem {
	out := make([]ScoreItem, n)
	for i := range out {
		out[i] = ScoreItem{ID: fmt.Sprintf("i%d", i), Label: dt(fmt.Sprintf("item number %d", i))}
	}
	return out
}

// scoreByID answers each question with score(id).
func scoreByID(score func(id string) float64) func(decisions.Request) (map[string]decisions.Answer, error) {
	return func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for q := range r.Questions {
			out[q] = decisions.Answer{Type: decisions.TypeNoul, Noul: score(strings.TrimPrefix(q, "s:"))}
		}
		return out, nil
	}
}

func req(n int) ScoreRequest {
	return ScoreRequest{Job: "test", Criterion: dt("the item is relevant"), Context: dt("fix the login bug"), Items: items(n)}
}

func TestScoreReturnsOneProbabilityPerItem(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: scoreByID(func(id string) float64 {
		if id == "i1" {
			return 0.9
		}
		return 0.05
	})}
	res, err := NewWith(d).Score(context.Background(), req(3))
	if err != nil {
		t.Fatal(err)
	}
	if res.Scores["i1"] != 0.9 || res.Scores["i0"] != 0.05 || len(res.Unknown) != 0 || res.Requests != 1 {
		t.Fatalf("result = %+v", res)
	}
	// The long criterion is in the state once; the questions only point at it.
	r := d.reqs[0]
	state := r.State.(map[string]any)
	if state["criterion"] != "the item is relevant" || state["context"] != "fix the login bug" {
		t.Fatalf("state = %v", state)
	}
	for id, q := range r.Questions {
		if !strings.Contains(q.Instructions, "state.criterion") || strings.Contains(q.Instructions, "relevant") {
			t.Fatalf("question %s repeats the criterion: %q", id, q.Instructions)
		}
	}
}

func TestScoreInvalidAnswersAreUnknownNotZero(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		return map[string]decisions.Answer{
			"s:i0": {Type: decisions.TypeNoul, Noul: 0.4},
			"s:i1": {Type: decisions.TypeNoul, Noul: math.NaN()},
			"s:i2": {Type: decisions.TypeNoul, Noul: 1.5},
			"s:i3": {Type: decisions.TypeNoul, Noul: -0.1},
			"s:i4": {Type: decisions.TypeChoice, Choice: "x"},
			// s:i5 is missing altogether.
		}, nil
	}}
	res, err := NewWith(d).Score(context.Background(), req(6))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scores) != 1 || res.Scores["i0"] != 0.4 {
		t.Fatalf("scores = %v", res.Scores)
	}
	if strings.Join(res.Unknown, ",") != "i1,i2,i3,i4,i5" {
		t.Fatalf("unknown = %v", res.Unknown)
	}
}

func TestScoreBatchesBySize(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: scoreByID(func(string) float64 { return 0.5 })}
	res, err := NewWith(d).Score(context.Background(), req(300))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scores) != 300 || res.Requests != 3 {
		t.Fatalf("scores %d requests %d, want 300 scores in 3 requests of at most %d", len(res.Scores), res.Requests, maxItemsRemote)
	}
	for _, r := range d.reqs {
		if len(r.Questions) > maxItemsRemote {
			t.Fatalf("batch of %d exceeds the cap", len(r.Questions))
		}
	}
}

func TestScoreLocalUsesSmallBatches(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendLocal, answer: scoreByID(func(string) float64 { return 0.5 })}
	res, err := NewWith(d).Score(context.Background(), req(60))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scores) != 60 {
		t.Fatalf("scores = %d", len(res.Scores))
	}
	for _, r := range d.reqs {
		if len(r.Questions) > maxItemsLocal {
			t.Fatalf("local batch of %d exceeds %d", len(r.Questions), maxItemsLocal)
		}
	}
}

func TestScoreSplitsAFailingBatchInHalf(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if len(r.Questions) > 4 {
			return nil, &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}
		}
		return scoreByID(func(string) float64 { return 0.7 })(r)
	}}
	res, err := NewWith(d).Score(context.Background(), req(16))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Scores) != 16 || len(res.Unknown) != 0 || res.Splits == 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.FailClass != decisions.ClassUnavailable {
		t.Fatalf("fail class = %q", res.FailClass)
	}
}

func TestScoreDoesNotSplitOnRateLimit(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return nil, &decisions.Error{Class: decisions.ClassUnavailable, Status: 429}
	}}
	res, err := NewWith(d).Score(context.Background(), req(8))
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests != 1 || res.Splits != 0 || len(res.Unknown) != 8 {
		t.Fatalf("result = %+v", res)
	}
}

func TestScoreAuthFailureStopsEverything(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return nil, &decisions.Error{Class: decisions.ClassAuth, Status: 401}
	}}
	res, err := NewWith(d).Score(context.Background(), req(300))
	if !errors.Is(err, ErrScoreAuth) {
		t.Fatalf("err = %v, want ErrScoreAuth", err)
	}
	if len(res.Scores) != 0 {
		t.Fatalf("scores = %v", res.Scores)
	}
}

func TestScoreRequestCap(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return nil, &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}
	}}
	r := req(64)
	r.MaxRequests = 5
	res, _ := NewWith(d).Score(context.Background(), r)
	if res.Requests > 5 {
		t.Fatalf("requests = %d, want at most 5", res.Requests)
	}
}

func TestScoreCacheHitsSkipTheBackend(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: scoreByID(func(string) float64 { return 0.6 })}
	c := NewWith(d)
	c.SetScoreCache(NewScoreCache(0))
	if _, err := c.Score(context.Background(), req(5)); err != nil {
		t.Fatal(err)
	}
	res, err := c.Score(context.Background(), req(5))
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHits != 5 || res.Requests != 0 || len(d.reqs) != 1 {
		t.Fatalf("result = %+v, backend requests %d", res, len(d.reqs))
	}
	// A different context is a miss.
	other := req(5)
	other.Context = dt("something else")
	res, _ = c.Score(context.Background(), other)
	if res.CacheHits != 0 || res.Requests != 1 {
		t.Fatalf("changed context should miss: %+v", res)
	}
}

func TestScoreDuplicateAndEmptyIDsIgnored(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: scoreByID(func(string) float64 { return 0.5 })}
	r := req(2)
	r.Items = append(r.Items, ScoreItem{ID: "i0", Label: dt("dup")}, ScoreItem{ID: "", Label: dt("empty")})
	res, err := NewWith(d).Score(context.Background(), r)
	if err != nil || len(res.Scores) != 2 || len(res.Unknown) != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestScoreContextCancelled(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: scoreByID(func(string) float64 { return 0.5 })}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := NewWith(d).Score(ctx, req(3))
	if !errors.Is(err, context.Canceled) || len(res.Scores) != 0 || len(d.reqs) != 0 {
		t.Fatalf("res %+v err %v reqs %d", res, err, len(d.reqs))
	}
}

func TestPickSelfHostedAsksOneChoiceQuestion(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		return map[string]decisions.Answer{"pick": {Type: decisions.TypeChoice, Choice: "o2", Probabilities: map[string]float64{"o1": 0.2, "o2": 0.6, "o3": 0.2}}}, nil
	}}
	res, err := NewWith(d).Pick(context.Background(), PickRequest{
		Job: "options", Question: dt("Which would the user pick?"), Context: dt("deploy now?"),
		Options: []ScoreItem{{ID: "o1", Label: dt("yes")}, {ID: "o2", Label: dt("no")}, {ID: "o3", Label: dt("later")}},
	})
	if err != nil || res.Best != "o2" || len(d.reqs) != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	q := d.reqs[0].Questions["pick"]
	if q.Type != decisions.TypeChoice || len(q.Options) != 3 {
		t.Fatalf("question = %+v", q)
	}
	total := 0.0
	for _, p := range res.Probs {
		total += p
	}
	if math.Abs(total-1) > 1e-9 {
		t.Fatalf("probabilities sum to %v", total)
	}
}

func TestPickTieKeepsCallerOrder(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendLocal, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return map[string]decisions.Answer{"pick": {Type: decisions.TypeChoice, Probabilities: map[string]float64{"a": 0.5, "b": 0.5}}}, nil
	}}
	res, err := NewWith(d).Pick(context.Background(), PickRequest{
		Question: dt("q"), Context: dt("c"), Options: []ScoreItem{{ID: "a", Label: dt("A")}, {ID: "b", Label: dt("B")}},
	})
	if err != nil || res.Best != "a" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestPickRefusesBadOptions(t *testing.T) {
	c := NewWith(&fakeDecider{backend: decisions.BackendLocal})
	bad := [][]ScoreItem{
		nil,
		{{ID: "a"}},
		{{ID: "a"}, {ID: "a"}},
		{{ID: "a"}, {ID: "b c"}},
		{{ID: "a"}, {ID: ""}},
	}
	for _, opts := range bad {
		if _, err := c.Pick(context.Background(), PickRequest{Options: opts}); !errors.Is(err, ErrPickOptions) {
			t.Errorf("options %v: err = %v", opts, err)
		}
	}
	many := make([]ScoreItem, MaxPickOptions+1)
	for i := range many {
		many[i] = ScoreItem{ID: fmt.Sprintf("o%d", i)}
	}
	if _, err := c.Pick(context.Background(), PickRequest{Options: many}); !errors.Is(err, ErrPickOptions) {
		t.Errorf("17 options: err = %v", err)
	}
}

func TestPickRejectsOutOfRangeProbabilities(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return map[string]decisions.Answer{"pick": {Type: decisions.TypeChoice, Probabilities: map[string]float64{"a": 1.4, "b": 0.1}}}, nil
	}}
	_, err := NewWith(d).Pick(context.Background(), PickRequest{Question: dt("q"), Context: dt("c"),
		Options: []ScoreItem{{ID: "a", Label: dt("A")}, {ID: "b", Label: dt("B")}}})
	if err == nil {
		t.Fatal("out-of-range probability accepted")
	}
}

func TestThresholdsAreOrdered(t *testing.T) {
	if !(DropAt < KeepAt && KeepAt < StrongAt && StrongAt < SwapAt && SwapAt <= 1) {
		t.Fatal("thresholds out of order")
	}
}

func TestScoreCacheEvictsAtCapacity(t *testing.T) {
	c := NewScoreCache(8)
	for i := 0; i < 50; i++ {
		c.put(fmt.Sprintf("k%d", i), 0.5)
	}
	if c.Len() > 8 {
		t.Fatalf("cache holds %d entries, cap 8", c.Len())
	}
	var nilCache *ScoreCache
	nilCache.put("x", 1)
	if _, ok := nilCache.get("x"); ok || nilCache.Len() != 0 {
		t.Fatal("nil cache must be inert")
	}
}
