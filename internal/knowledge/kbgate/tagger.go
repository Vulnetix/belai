package kbgate

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge/tags"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
)

// topicRater is the one thing the tagger needs from the decision backend.
type topicRater interface {
	// Limits is what one request holds: topics, and bytes for the criterion,
	// the document text and the topic labels together.
	Limits() (items, bytes int)
	// Local reports the backend answers one question at a time, so it is slower.
	Local() bool
	Rate(ctx context.Context, chunks []string, topics []jev.TopicItem) (jev.ScoreResult, error)
}

type jobsRater struct{ jobs *jev.Jobs }

func (r jobsRater) Limits() (int, int) { return r.jobs.Client.TopicLimits() }
func (r jobsRater) Local() bool        { return r.jobs.Client.Local() }
func (r jobsRater) Rate(ctx context.Context, chunks []string, topics []jev.TopicItem) (jev.ScoreResult, error) {
	return r.jobs.RateTopics(ctx, chunks, topics)
}

// Tuning of the topic call. A document is asked about at most once per
// refresh, a backend that fails three times in a row is left alone until the
// next refresh, and a call that has not answered by its deadline is dropped in
// favour of the pattern detector.
const (
	// minTopicContext is the least document text worth a call: when the topics
	// leave less room than this, the lowest ranked topics are dropped until it
	// fits.
	minTopicContext   = 2000
	topicChunkJoin    = 5 // bytes the joiner between chunks adds
	remoteTopicTimout = 8 * time.Second
	localTopicTimeout = 25 * time.Second
	topicMaxFailures  = 3
)

// topicTagger tags a document with the deterministic labels and, while a
// decision backend is available and the refresh budget lasts, scores the best
// candidate topics in one call. Everything it does not ask, and every answer it
// does not get, is the pattern detector's.
type topicTagger struct {
	rater  topicRater
	chunks int // most chunks of one document sent
	budget int // documents per refresh
	cut    float64
	left   atomic.Int64
	fails  atomic.Int64
}

// NewTagger returns the tagger a session's knowledge store uses. With no
// decision backend, the job switched off in settings, or a budget of 0 it is
// the deterministic tags.Fast: nothing is sent anywhere. Otherwise it also
// scores topics with the knowledge_topics Jev job. That job sends a sample of an
// admitted document's text to the backend, which is the one exception to
// "relevance jobs see harness facts only" (docs/jev-jobs.md); the sample is
// bounded by topic_chunks and the backend's request size.
func NewTagger(cfg run.Config, s config.Settings) tags.Tagger {
	jobs := run.NewJevJobs(cfg, s.JevJobSet)
	if !jobs.Enabled(config.JevKnowledgeTopics) {
		return tags.Fast{}
	}
	chunks, docs := s.KnowledgeTopics()
	return newTopicTagger(jobsRater{jobs}, chunks, docs, s.JevThresholds().TopicAt)
}

func newTopicTagger(r topicRater, chunks, docs int, cut float64) tags.Tagger {
	if docs <= 0 || chunks <= 0 {
		return tags.Fast{}
	}
	t := &topicTagger{rater: r, chunks: chunks, budget: docs, cut: cut}
	t.left.Store(int64(docs))
	return t
}

// BeginRefresh resets the per-refresh budget and failure count.
func (t *topicTagger) BeginRefresh() {
	t.left.Store(int64(t.budget))
	t.fails.Store(0)
}

// Refresh reports whether a stored document should be tagged again: its format
// is stale, or the backend has not scored it yet and there is budget left.
func (t *topicTagger) Refresh(r tags.Result) bool {
	if r.Ver != tags.Version {
		return true
	}
	if r.Kind == "scanner" {
		return false // labelled from its kind and identifiers; never sent
	}
	return r.Src != tags.SrcJev && t.left.Load() > 0 && t.fails.Load() < topicMaxFailures
}

// Tag implements tags.Tagger.
func (t *topicTagger) Tag(ctx context.Context, in tags.Input) tags.Result {
	r := tags.Detect(in)
	// Scanner artefacts are labelled from their kind and identifier fields; their
	// records are not prose a backend could add anything to.
	if len(in.Chunks) == 0 || in.Artifact != "" || in.Records {
		return r
	}
	if t.fails.Load() >= topicMaxFailures || t.left.Add(-1) < 0 {
		return r
	}
	items, sample := t.plan(r.Topics, in.Chunks)
	if len(items) == 0 || len(sample) == 0 {
		return r
	}
	timeout := remoteTopicTimout
	if t.rater.Local() {
		timeout = localTopicTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res, err := t.rater.Rate(cctx, sample, items)
	took := time.Since(start)
	if err != nil || len(res.Scores) == 0 {
		t.fails.Add(1)
		rolemanager.RecordKnowledgeTopics("unknown", len(items), len(r.Topics), res.Identity, took)
		return r
	}
	t.fails.Store(0)
	asked := make(map[string]bool, len(items))
	for _, it := range items {
		asked[it.ID] = true
	}
	r.Topics = tags.Merge(r.Topics, asked, res.Scores, t.cut)
	r.Src = tags.SrcJev
	rolemanager.RecordKnowledgeTopics("scored", len(items), len(r.Topics), res.Identity, took)
	return r
}

// plan picks the topics to ask about and the chunks to show with them, so the
// whole question fits one request. It starts from as many topics as the backend
// takes, the detector's finds first (tags.Candidates), and drops the lowest
// ranked until the document text has room for at least minTopicContext bytes.
func (t *topicTagger) plan(found []tags.Topic, chunks []string) ([]jev.TopicItem, []string) {
	maxItems, maxBytes := t.rater.Limits()
	var items []jev.TopicItem
	for _, id := range tags.Candidates(found, maxItems) {
		if d, ok := tags.Lookup(id); ok {
			items = append(items, jev.TopicItem{ID: d.ID, Label: d.Label})
		}
	}
	room := func() int {
		used := jev.TopicFixedBytes() + jev.TopicContextMargin
		for _, it := range items {
			used += jev.TopicItemBytes(it)
		}
		return maxBytes - used
	}
	for len(items) > 0 && room() < minTopicContext {
		items = items[:len(items)-1]
	}
	if len(items) == 0 {
		return nil, nil
	}
	budget := room() - topicChunkJoin*t.chunks
	return items, tags.Sample(chunks, t.chunks, budget)
}
