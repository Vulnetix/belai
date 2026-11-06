package docsuite

import (
	"fmt"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/locate"
	"github.com/vulnetix/belai/internal/symindex"
)

// TestSymbolMapPageStatesTheDefaultsAndBounds pins the settings table, the
// bounds table and the closed kind set in docs/symbol-map.md to the code.
func TestSymbolMapPageStatesTheDefaultsAndBounds(t *testing.T) {
	phrases := []string{
		"| `symbol_index.enabled` | Build the index and offer `Outline` and `Symbol`. | `true` |",
		fmt.Sprintf("100 to 10000. | `%d` |", config.DefaultSymbolIndexMaxFiles),
		fmt.Sprintf("1 to 200. | `%d` |", config.DefaultSymbolIndexMaxResults),
		fmt.Sprintf("5 to 3600. | `%d` |", config.DefaultSymbolIndexRefreshSecs),
		fmt.Sprintf("| Time for one build or refresh | %d seconds |", int(symindex.BuildTimeout.Seconds())),
		fmt.Sprintf("| Declarations kept for one file | %d |", symindex.MaxPerFile),
		fmt.Sprintf("| Declarations in one index | %d |", symindex.MaxTotal),
		fmt.Sprintf("| Length of a name | %d bytes |", symindex.MaxNameLen),
		"`TestRepoMapBlockNeverCarriesADeclarationName`",
	}
	for _, k := range locate.Kinds {
		phrases = append(phrases, "`"+k+"`")
	}
	requirePhrases(t, "docs/symbol-map.md", phrases...)
	requirePhrases(t, "AGENTS.md", "The symbol index serves the repository's names only through classified tools")
}
