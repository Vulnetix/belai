package jev

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/rolemanager"
)

// batchDecider answers every question false, except questions about content
// holding "INJECT", which it answers true. It records each request.
type batchDecider struct {
	id   string
	fail bool
	mu   sync.Mutex
	reqs []decisions.Request
}

func (b *batchDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	b.mu.Lock()
	b.reqs = append(b.reqs, r)
	b.mu.Unlock()
	st := r.State.(map[string]any)
	if b.fail {
		if _, batched := st["items"]; batched {
			return decisions.Result{}, &decisions.Error{Class: decisions.ClassUnavailable, Status: 503, Msg: "busy"}
		}
	}
	out := map[string]decisions.Answer{}
	for k := range r.Questions {
		content, _ := st["content"].(string)
		if items, ok := st["items"].(map[string]string); ok {
			content = items[strings.SplitN(k, ":", 2)[0]]
		}
		n := 0.0
		if strings.Contains(content, "INJECT") && strings.HasSuffix(k, string(rolemanager.SentinelPromptInjection)) {
			n = 0.99
		}
		out[k] = decisions.Answer{Type: decisions.TypeNoul, Noul: n}
	}
	return decisions.Result{Answers: out}, nil
}
func (b *batchDecider) Identity() string           { return b.id }
func (b *batchDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func (b *batchDecider) requests() []decisions.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]decisions.Request(nil), b.reqs...)
}

var noFallback = rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
	return "", errors.New("fallback asked")
})

func classifyAll(t *testing.T, s *Security, contents []string) []string {
	t.Helper()
	out := make([]string, len(contents))
	var wg sync.WaitGroup
	for i, c := range contents {
		wg.Add(1)
		go func(i int, c string) {
			defer wg.Done()
			v, err := s.Classify(context.Background(), rolemanager.BuildClassifierPayload(c))
			if err != nil {
				t.Errorf("item %d: %v", i, err)
			}
			out[i] = v
		}(i, c)
	}
	wg.Wait()
	return out
}

func TestSecurityChecksArrivingTogetherShareOneRequest(t *testing.T) {
	d := &batchDecider{id: t.Name()}
	s := NewSecurityWith(d, noFallback)
	got := classifyAll(t, s, []string{"alpha", "beta", "gamma", "delta"})
	for i, v := range got {
		if v != string(rolemanager.SentinelSafe) {
			t.Fatalf("item %d = %q", i, v)
		}
	}
	reqs := d.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	if n := len(reqs[0].Questions); n != 4*len(rolemanager.BuildClassifierPayload("x").Categories) {
		t.Fatalf("%d questions", n)
	}
	if _, ok := reqs[0].Questions["2:"+string(rolemanager.SentinelPromptInjection)]; !ok {
		t.Fatalf("questions are not keyed by item and category: %v", reqs[0].Questions)
	}
}

func TestAFlaggedItemSendsItsNeighboursAloneAgain(t *testing.T) {
	d := &batchDecider{id: t.Name()}
	s := NewSecurityWith(d, noFallback)
	got := classifyAll(t, s, []string{"alpha", "INJECT here", "gamma"})
	if got[1] != string(rolemanager.SentinelPromptInjection) || got[0] != string(rolemanager.SentinelSafe) || got[2] != string(rolemanager.SentinelSafe) {
		t.Fatalf("verdicts = %v", got)
	}
	// One batch, then alpha and gamma alone.
	reqs := d.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	for _, r := range reqs[1:] {
		if _, single := r.State.(map[string]any)["content"]; !single {
			t.Fatalf("a re-check was batched: %v", r.State)
		}
	}
}

func TestBatchRespectsTheBackendQuestionCap(t *testing.T) {
	d := &decisions.SystemOne{Name: "clef", Model: t.Name(), MaxQuestions: 8}
	b := batcherFor(d)
	cats := rolemanager.BuildClassifierPayload("x").Categories
	b.pending = []*batchItem{{content: "a", cats: cats}}
	if len(cats) == 4 && b.fits(&batchItem{content: "b", cats: cats}) == false {
		t.Fatal("two items of four questions do not fit eight")
	}
	b.pending = append(b.pending, &batchItem{content: "b", cats: cats})
	if b.fits(&batchItem{content: "c", cats: cats}) {
		t.Fatal("a third item went past the question cap")
	}
	b.pending = nil
}

func TestAFailedBatchIsAskedItemByItem(t *testing.T) {
	d := &batchDecider{id: t.Name(), fail: true}
	s := NewSecurityWith(d, noFallback)
	got := classifyAll(t, s, []string{"alpha", "beta"})
	if got[0] != string(rolemanager.SentinelSafe) || got[1] != string(rolemanager.SentinelSafe) {
		t.Fatalf("verdicts = %v", got)
	}
	if n := len(d.requests()); n != 3 {
		t.Fatalf("%d requests, want the batch then two singles", n)
	}
}

func TestALargeItemTravelsAlone(t *testing.T) {
	d := &batchDecider{id: t.Name()}
	s := NewSecurityWith(d, noFallback)
	classifyAll(t, s, []string{strings.Repeat("x", batchMaxBytes+1)})
	r := d.requests()
	if len(r) != 1 {
		t.Fatalf("%d requests", len(r))
	}
	if _, single := r[0].State.(map[string]any)["content"]; !single {
		t.Fatal("a large item was batched")
	}
}

// A score batch fits the backend's own caps, so it is never refused before it
// is sent and split.
func TestScoreBatchesFitTheBackendCaps(t *testing.T) {
	c := NewWith(&decisions.SystemOne{Name: "clef", Model: "clef", MaxQuestions: 64, MaxBodyBytes: 20_000})
	items, bytes, _ := c.limits()
	if items != 64 || bytes != 10_000 {
		t.Fatalf("items=%d bytes=%d", items, bytes)
	}
	if items, _, _ := NewWith(&decisions.SystemOne{Name: "ts", Model: "m"}).limits(); items != maxItemsRemote {
		t.Fatalf("an uncapped backend got %d items", items)
	}
}
