package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
)

// TestKnowledgePageStatesTheSourceIndexDefaultsAndBounds pins the source index
// settings, the ranking constants and the opt-in rule in docs/knowledge.md and
// AGENTS.md to the code.
func TestKnowledgePageStatesTheSourceIndexDefaultsAndBounds(t *testing.T) {
	requirePhrases(t, "docs/knowledge.md",
		"| `knowledge.source_index` | `false`;",
		fmt.Sprintf("| `knowledge.source_max_tokens` | `%d`;", config.DefaultKnowledgeSourceTokens),
		fmt.Sprintf("| `knowledge.source_max_file_bytes` | `%d`;", config.DefaultKnowledgeSourceFileBytes),
		fmt.Sprintf("| %d to %d |", config.MinKnowledgeSourceFileBytes, config.MaxKnowledgeSourceFileBytes),
		"`knowledge/source/<hash of the project root>/index.bin`",
		"`/knowledge reindex`",
		"reciprocal rank (constant 60)",
		fmt.Sprintf("under %d estimated tokens joins", knowledge.MinSourceChunkTokens),
		fmt.Sprintf("over %d is cut", knowledge.MaxSourceChunkTokens),
		"The `knowledge_topics` Jev job never sees source text",
	)
	requirePhrases(t, "AGENTS.md", "The source index is the user's opt-in and the same gate")
}
