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
	} {
		if c.v != nil && (*c.v < c.min || *c.v > c.max) {
			return fmt.Errorf("%s %d must be between %d and %d", c.key, *c.v, c.min, c.max)
		}
	}
	return nil
}
