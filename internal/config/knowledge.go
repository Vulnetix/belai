package config

import "fmt"

// Knowledge defaults and bounds (docs/knowledge.md). These are host
// performance settings, not a payment limit: they bound how much text Belai
// keeps indexed and how much one search puts in front of a model.
const (
	// DefaultKnowledgeIndexTokens is the corpus an agent profile may keep
	// indexed.
	DefaultKnowledgeIndexTokens = 200_000
	// DefaultKnowledgeProjectTokens is the corpus of a project's .vulnetix
	// output plus a session's @ files.
	DefaultKnowledgeProjectTokens = 200_000
	// DefaultKnowledgeResultTokens is what one search may return.
	DefaultKnowledgeResultTokens = 3_000

	// DefaultKnowledgeTopicChunks is how many chunks of one document the
	// knowledge_topics job may show a decision backend. A longer document is
	// sampled down to this many (and to what the backend's request holds).
	DefaultKnowledgeTopicChunks = 12
	// DefaultKnowledgeTopicDocs is how many documents one refresh may send to
	// the backend for topics. The rest are labelled by the pattern detector and
	// get their turn at a later refresh.
	DefaultKnowledgeTopicDocs = 40

	MinKnowledgeTopicChunks = 1
	MaxKnowledgeTopicChunks = 64
	MaxKnowledgeTopicDocs   = 1_000

	MinKnowledgeCorpusTokens = 1_000
	MaxKnowledgeCorpusTokens = 5_000_000
	MinKnowledgeResultTokens = 200
	MaxKnowledgeResultTokens = 50_000
)

// KnowledgeSettings sizes the retrieval store behind agent profile documents
// and project knowledge. A per-user preference: the project layer is dropped,
// so a repository cannot make a host index or return more.
type KnowledgeSettings struct {
	// MaxIndexTokens caps one agent profile's indexed corpus, in estimated
	// tokens. Default 200000.
	MaxIndexTokens *int `json:"max_index_tokens,omitempty"`
	// MaxProjectTokens caps the project corpus (.vulnetix output and the
	// session's @ files together). Default 200000.
	MaxProjectTokens *int `json:"max_project_tokens,omitempty"`
	// MaxResultTokens caps what one search returns across every index.
	// Default 3000.
	MaxResultTokens *int `json:"max_result_tokens,omitempty"`
	// TopicChunks is the most chunks of one document the knowledge_topics Jev
	// job sends to a decision backend; a longer document is sampled. Default 12.
	TopicChunks *int `json:"topic_chunks,omitempty"`
	// TopicBudgetDocs is the most documents one refresh sends to a backend for
	// topics; 0 sends none. Default 40.
	TopicBudgetDocs *int `json:"topic_budget_docs,omitempty"`
}

// KnowledgeTopics returns the effective sample size per document and the
// per-refresh document budget of the knowledge_topics job.
func (s Settings) KnowledgeTopics() (chunks, docs int) {
	chunks, docs = DefaultKnowledgeTopicChunks, DefaultKnowledgeTopicDocs
	if k := s.Knowledge; k != nil {
		if k.TopicChunks != nil {
			chunks = *k.TopicChunks
		}
		if k.TopicBudgetDocs != nil {
			docs = *k.TopicBudgetDocs
		}
	}
	return chunks, docs
}

// KnowledgeLimits returns the effective corpus cap per profile, the corpus cap
// for the project and the per-search result cap, each in estimated tokens.
func (s Settings) KnowledgeLimits() (index, project, result int) {
	index, project, result = DefaultKnowledgeIndexTokens, DefaultKnowledgeProjectTokens, DefaultKnowledgeResultTokens
	if k := s.Knowledge; k != nil {
		if k.MaxIndexTokens != nil {
			index = *k.MaxIndexTokens
		}
		if k.MaxProjectTokens != nil {
			project = *k.MaxProjectTokens
		}
		if k.MaxResultTokens != nil {
			result = *k.MaxResultTokens
		}
	}
	return index, project, result
}

func (k *KnowledgeSettings) merge(from *KnowledgeSettings) {
	if from.MaxIndexTokens != nil {
		k.MaxIndexTokens = from.MaxIndexTokens
	}
	if from.MaxProjectTokens != nil {
		k.MaxProjectTokens = from.MaxProjectTokens
	}
	if from.MaxResultTokens != nil {
		k.MaxResultTokens = from.MaxResultTokens
	}
	if from.TopicChunks != nil {
		k.TopicChunks = from.TopicChunks
	}
	if from.TopicBudgetDocs != nil {
		k.TopicBudgetDocs = from.TopicBudgetDocs
	}
}

// ValidateKnowledge rejects a knowledge block with a size outside its bounds,
// naming the key.
func ValidateKnowledge(s Settings) error {
	k := s.Knowledge
	if k == nil {
		return nil
	}
	for _, c := range []struct {
		key      string
		v        *int
		min, max int
	}{
		{"knowledge.max_index_tokens", k.MaxIndexTokens, MinKnowledgeCorpusTokens, MaxKnowledgeCorpusTokens},
		{"knowledge.max_project_tokens", k.MaxProjectTokens, MinKnowledgeCorpusTokens, MaxKnowledgeCorpusTokens},
		{"knowledge.max_result_tokens", k.MaxResultTokens, MinKnowledgeResultTokens, MaxKnowledgeResultTokens},
		{"knowledge.topic_chunks", k.TopicChunks, MinKnowledgeTopicChunks, MaxKnowledgeTopicChunks},
		{"knowledge.topic_budget_docs", k.TopicBudgetDocs, 0, MaxKnowledgeTopicDocs},
	} {
		if c.v != nil && (*c.v < c.min || *c.v > c.max) {
			return fmt.Errorf("%s %d must be between %d and %d", c.key, *c.v, c.min, c.max)
		}
	}
	return nil
}
