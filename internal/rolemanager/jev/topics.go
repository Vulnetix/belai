package jev

import (
	"context"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sanitize"
)

// topicCriterion is the statement every topic is judged against.
const topicCriterion = "The document text in state.context is mainly about the topic named by the item. Answer true only when the topic is a main subject of the document, not when it is only mentioned in passing."

// TopicItem is one topic to ask about: a vocabulary id and its short label.
type TopicItem struct {
	ID    string
	Label string
}

// TopicLimits is what one decision request can hold for this backend: the most
// topics, and the bytes available to the criterion, the context and the item
// labels together. A caller that wants one request picks at most items topics
// and a context that fits what is left after the labels (TopicItemBytes).
func (c *Client) TopicLimits() (items, bytes int) {
	n, b, _ := c.limits()
	return n, b
}

// TopicItemBytes is what one topic costs against the byte budget, as the
// batcher charges it.
func TopicItemBytes(t TopicItem) int { return len(t.ID) + len(t.Label) + itemOverhead }

// TopicFixedBytes is the part of the budget the criterion takes.
func TopicFixedBytes() int { return len(topicCriterion) }

// TopicContextMargin is how much of a context budget a caller should leave
// unused: sanitising can lengthen a text (it prefixes lines that imitate the
// prompt layout), and the request must stay within one batch.
const TopicContextMargin = 512

// RateTopics asks, in one decision request, how much each topic is a main
// subject of a document. chunks is the document's sampled, already admitted
// text; it is the one place a relevance job sends file text to a backend, and
// the caller bounds it to the request's byte budget (TopicLimits). The scores
// are probabilities in [0,1]; a topic the backend did not answer is absent
// from the result. The call makes at most one request and keeps out of the
// score cache, since a document is not asked about twice.
func (j *Jobs) RateTopics(ctx context.Context, chunks []string, topics []TopicItem) (ScoreResult, error) {
	items := make([]ScoreItem, 0, len(topics))
	for _, t := range topics {
		items = append(items, ScoreItem{ID: sanitize.Ident(t.ID, 64), Label: sanitize.ForDecision(t.Label, 64)})
	}
	res, err := j.Client.Score(ctx, ScoreRequest{
		Job:         string(config.JevKnowledgeTopics),
		Criterion:   sanitize.ForDecision(topicCriterion, 0),
		Context:     sanitize.ForDecision(strings.Join(chunks, "\n---\n"), 0),
		Items:       items,
		MaxRequests: 1,
		NoCache:     true,
	})
	return res, err
}
