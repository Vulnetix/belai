package kbgate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

type fakeRater struct {
	items, bytes int
	local        bool
	mu           sync.Mutex
	calls        int
	lastChunks   []string
	lastTopics   []jev.TopicItem
	answer       func([]jev.TopicItem) (jev.ScoreResult, error)
}

func (f *fakeRater) Limits() (int, int) { return f.items, f.bytes }
func (f *fakeRater) Local() bool        { return f.local }
func (f *fakeRater) Rate(_ context.Context, chunks []string, topics []jev.TopicItem) (jev.ScoreResult, error) {
	f.mu.Lock()
	f.calls++
	f.lastChunks, f.lastTopics = chunks, topics
	f.mu.Unlock()
	return f.answer(topics)
}

func scoring(v map[string]float64) func([]jev.TopicItem) (jev.ScoreResult, error) {
	return func(items []jev.TopicItem) (jev.ScoreResult, error) {
		res := jev.ScoreResult{Scores: map[string]float64{}, Identity: "fake/model"}
		for _, it := range items {
			if s, ok := v[it.ID]; ok {
				res.Scores[it.ID] = s
			} else {
				res.Scores[it.ID] = 0.01
			}
		}
		return res, nil
	}
}

func authDoc() tags.Input {
	return tags.Input{Rel: "docs/auth.md", Size: 900, Chunks: []string{
		"# Authentication\n\nUsers log in with a password and a totp code. Sessions are cookies with samesite set.\n",
	}}
}

func TestTaggerAsksOneCallAndTheBackendDecides(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: scoring(map[string]float64{"oauth": 0.95, "authn": 0.2})}
	tg := newTopicTagger(f, 12, 40, 0.7)
	r := tg.Tag(context.Background(), authDoc())
	if f.calls != 1 || len(f.lastTopics) != 128 {
		t.Fatalf("calls %d, topics asked %d", f.calls, len(f.lastTopics))
	}
	if r.Src != tags.SrcJev || !r.Has("topic:oauth") {
		t.Fatalf("result %+v", r)
	}
	if r.Has("topic:authn") {
		t.Fatalf("authn was answered below the cut and must be dropped: %v", r.Topics)
	}
	if !strings.Contains(strings.Join(f.lastChunks, ""), "Authentication") {
		t.Fatalf("the document text was not sent: %q", f.lastChunks)
	}
}

func TestTaggerPutsDetectorFindsFirstInTheCall(t *testing.T) {
	f := &fakeRater{items: 24, bytes: 12_000, local: true, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 40, 0.7)
	tg.Tag(context.Background(), authDoc())
	if len(f.lastTopics) != 24 {
		t.Fatalf("a local backend takes 24 topics, asked %d", len(f.lastTopics))
	}
	first := map[string]bool{}
	for _, it := range f.lastTopics[:5] {
		first[it.ID] = true
	}
	if !first["authn"] {
		t.Fatalf("the topic the detector found must be asked first: %v", f.lastTopics[:5])
	}
}

func TestTaggerFitsTheRequestAndSamplesLongDocuments(t *testing.T) {
	var chunks []string
	for i := 0; i < 100; i++ {
		chunks = append(chunks, fmt.Sprintf("chunk %03d ", i)+strings.Repeat("kubernetes helm deployment ", 30))
	}
	f := &fakeRater{items: 128, bytes: 38_000, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 40, 0.7)
	tg.Tag(context.Background(), tags.Input{Rel: "k.md", Chunks: chunks})
	if len(f.lastChunks) == 0 || len(f.lastChunks) > 12 {
		t.Fatalf("sampled %d chunks, want at most topic_chunks (12)", len(f.lastChunks))
	}
	if !strings.HasPrefix(f.lastChunks[0], "chunk 000") || !strings.HasPrefix(f.lastChunks[len(f.lastChunks)-1], "chunk 099") {
		t.Fatalf("the sample keeps the first and last chunk")
	}
	bytes := jev.TopicFixedBytes()
	for _, it := range f.lastTopics {
		bytes += jev.TopicItemBytes(it)
	}
	for _, c := range f.lastChunks {
		bytes += len(c) + topicChunkJoin
	}
	if bytes >= 38_000 {
		t.Fatalf("the question is %d bytes, over the 38000 byte request", bytes)
	}
}

func TestTaggerDropsTopicsBeforeStarvingTheDocument(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 12_000, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 40, 0.7)
	tg.Tag(context.Background(), authDoc())
	if len(f.lastTopics) >= 128 || len(f.lastTopics) == 0 {
		t.Fatalf("a 12000 byte request cannot hold 128 topics and a document: asked %d", len(f.lastTopics))
	}
	total := 0
	for _, c := range f.lastChunks {
		total += len(c)
	}
	if total == 0 {
		t.Fatal("no document text was sent")
	}
}

func TestTaggerFallsBackToPatternsWhenTheBackendFails(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: func([]jev.TopicItem) (jev.ScoreResult, error) {
		return jev.ScoreResult{}, errors.New("timeout")
	}}
	tg := newTopicTagger(f, 12, 40, 0.7)
	r := tg.Tag(context.Background(), authDoc())
	if r.Src != tags.SrcRegex || !r.Has("topic:authn") {
		t.Fatalf("the detector's result must stand: %+v", r)
	}
	if !tg.Refresh(r) {
		t.Fatal("a pattern-only document is retried while there is budget")
	}
}

func TestTaggerStopsAfterRepeatedFailuresUntilTheNextRefresh(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: func([]jev.TopicItem) (jev.ScoreResult, error) {
		return jev.ScoreResult{}, errors.New("down")
	}}
	tg := newTopicTagger(f, 12, 40, 0.7)
	for i := 0; i < 10; i++ {
		tg.Tag(context.Background(), authDoc())
	}
	if f.calls != topicMaxFailures {
		t.Fatalf("%d calls to a backend that is down, want %d", f.calls, topicMaxFailures)
	}
	tg.(interface{ BeginRefresh() }).BeginRefresh()
	tg.Tag(context.Background(), authDoc())
	if f.calls != topicMaxFailures+1 {
		t.Fatalf("a new refresh tries again: %d calls", f.calls)
	}
}

func TestTaggerBudgetIsPerRefresh(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 2, 0.7)
	for i := 0; i < 5; i++ {
		tg.Tag(context.Background(), authDoc())
	}
	if f.calls != 2 {
		t.Fatalf("%d calls with a budget of 2", f.calls)
	}
	tg.(interface{ BeginRefresh() }).BeginRefresh()
	tg.Tag(context.Background(), authDoc())
	if f.calls != 3 {
		t.Fatalf("budget did not reset: %d", f.calls)
	}
}

func TestTaggerSendsNothingForScannerRecordsOrEmptyDocuments(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 40, 0.7)
	tg.Tag(context.Background(), tags.Input{Rel: ".vulnetix/a.sarif", Artifact: "sarif", Records: true, Chunks: []string{"VNX-GO-SQLI"}})
	tg.Tag(context.Background(), tags.Input{Rel: ".vulnetix/memory.yaml", Artifact: "memory", Chunks: []string{"scan memory"}})
	tg.Tag(context.Background(), tags.Input{Rel: "empty.md"})
	if f.calls != 0 {
		t.Fatalf("%d backend calls for documents that are not prose", f.calls)
	}
}

func TestTaggerRefreshWantsAJevPassOnlyWhileItCanGiveOne(t *testing.T) {
	f := &fakeRater{items: 128, bytes: 38_000, answer: scoring(nil)}
	tg := newTopicTagger(f, 12, 1, 0.7)
	regexOnly := tags.Detect(authDoc())
	if !tg.Refresh(regexOnly) {
		t.Fatal("a pattern-only document wants a pass while budget remains")
	}
	jevDone := regexOnly
	jevDone.Src = tags.SrcJev
	if tg.Refresh(jevDone) {
		t.Fatal("a scored document is current")
	}
	stale := jevDone
	stale.Ver = tags.Version - 1
	if !tg.Refresh(stale) {
		t.Fatal("an older format is always redone")
	}
	tg.Tag(context.Background(), authDoc())
	if tg.Refresh(regexOnly) {
		t.Fatal("the budget is spent")
	}
}

func TestNewTaggerIsDeterministicWithoutABackendOrBudget(t *testing.T) {
	if _, ok := NewTagger(run.Config{}, config.Settings{}).(tags.Fast); !ok {
		t.Fatal("no decision backend must give the deterministic tagger")
	}
	zero := 0
	s := config.Settings{Knowledge: &config.KnowledgeSettings{TopicBudgetDocs: &zero}}
	if _, ok := newTopicTagger(&fakeRater{}, 12, *s.Knowledge.TopicBudgetDocs, 0.7).(tags.Fast); !ok {
		t.Fatal("a budget of 0 must give the deterministic tagger")
	}
	if c, d := (config.Settings{}).KnowledgeTopics(); c != config.DefaultKnowledgeTopicChunks || d != config.DefaultKnowledgeTopicDocs {
		t.Fatalf("defaults %d %d", c, d)
	}
}
