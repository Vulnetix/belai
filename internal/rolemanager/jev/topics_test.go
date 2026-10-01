package jev

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
)

func topicItems(n int) []TopicItem {
	out := make([]TopicItem, n)
	for i := range out {
		out[i] = TopicItem{ID: fmt.Sprintf("topic-%03d", i), Label: fmt.Sprintf("topic number %d", i)}
	}
	return out
}

func TestTopicLimitsFollowTheBackend(t *testing.T) {
	remote := NewWith(&fakeDecider{backend: decisions.BackendSystemOne})
	if n, b := remote.TopicLimits(); n != 128 || b != 38_000 {
		t.Fatalf("remote limits %d items %d bytes", n, b)
	}
	local := NewWith(&fakeDecider{backend: decisions.BackendLocal})
	if n, b := local.TopicLimits(); n != 24 || b != 12_000 {
		t.Fatalf("local limits %d items %d bytes", n, b)
	}
}

func TestRateTopicsIsOneRequestForEveryTopic(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for id := range r.Questions {
			v := 0.05
			if id == "s:topic-007" {
				v = 0.93
			}
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: v}
		}
		return out, nil
	}}
	j := &Jobs{Client: NewWith(d)}
	if !j.Enabled(config.JevKnowledgeTopics) {
		t.Fatal("the job is not enabled with a backend")
	}
	items := topicItems(128)
	chunks := []string{"the first sampled chunk about bearer tokens", "the last sampled chunk"}
	res, err := j.RateTopics(context.Background(), chunks, items)
	if err != nil {
		t.Fatal(err)
	}
	if res.Requests != 1 || len(d.reqs) != 1 || len(d.reqs[0].Questions) != 128 {
		t.Fatalf("requests %d, questions %d", res.Requests, len(d.reqs[0].Questions))
	}
	if len(res.Scores) != 128 || res.Scores["topic-007"] != 0.93 || len(res.Unknown) != 0 {
		t.Fatalf("scores %d unknown %v", len(res.Scores), res.Unknown)
	}
	state := d.reqs[0].State.(map[string]any)
	ctxText, _ := state["context"].(string)
	if !strings.Contains(ctxText, "bearer tokens") || !strings.Contains(ctxText, "last sampled chunk") {
		t.Fatalf("the sampled chunks must reach the state: %q", ctxText)
	}
}

func TestRateTopicsMakesNoSecondRequestAfterAFailure(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(decisions.Request) (map[string]decisions.Answer, error) {
		return nil, errors.New("timeout")
	}}
	j := &Jobs{Client: NewWith(d)}
	res, err := j.RateTopics(context.Background(), []string{"text"}, topicItems(40))
	if err != nil {
		t.Fatalf("a backend failure is unknown answers, not an error: %v", err)
	}
	if len(d.reqs) != 1 || res.Requests != 1 || len(res.Scores) != 0 || len(res.Unknown) != 40 {
		t.Fatalf("requests %d/%d scores %d unknown %d", len(d.reqs), res.Requests, len(res.Scores), len(res.Unknown))
	}
}

func TestRateTopicsStaysOutOfTheScoreCache(t *testing.T) {
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		out := map[string]decisions.Answer{}
		for id := range r.Questions {
			out[id] = decisions.Answer{Type: decisions.TypeNoul, Noul: 0.5}
		}
		return out, nil
	}}
	c := NewWith(d)
	cache := NewScoreCache(0)
	c.SetScoreCache(cache)
	j := &Jobs{Client: c}
	for i := 0; i < 2; i++ {
		if _, err := j.RateTopics(context.Background(), []string{"same text"}, topicItems(5)); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.reqs) != 2 || cache.Len() != 0 {
		t.Fatalf("requests %d, cache entries %d", len(d.reqs), cache.Len())
	}
}

func TestTopicBudgetArithmeticMatchesTheBatcher(t *testing.T) {
	items := topicItems(128)
	used := TopicFixedBytes()
	for _, it := range items {
		used += TopicItemBytes(it)
	}
	if used >= 38_000 {
		t.Fatalf("128 short topics cost %d bytes, which leaves no room in a 38000 byte request", used)
	}
	room := 38_000 - used - TopicContextMargin
	if room < 8_000 {
		t.Fatalf("only %d bytes of context fit beside 128 topics", room)
	}
	// A context of exactly that size still packs into a single batch.
	ctxText := strings.Repeat("a", room)
	d := &fakeDecider{backend: decisions.BackendSystemOne, answer: func(r decisions.Request) (map[string]decisions.Answer, error) {
		return map[string]decisions.Answer{}, nil
	}}
	j := &Jobs{Client: NewWith(d)}
	if _, err := j.RateTopics(context.Background(), []string{ctxText}, items); err != nil {
		t.Fatal(err)
	}
	if len(d.reqs) != 1 {
		t.Fatalf("a context sized to the budget needed %d requests", len(d.reqs))
	}
}
