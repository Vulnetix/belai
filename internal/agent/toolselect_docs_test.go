package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// TestJevJobsPageStatesTheSelectionRules keeps the tool and skill selection
// and tool search sections equal to the constants the code uses.
func TestJevJobsPageStatesTheSelectionRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/jev-jobs.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("at most %d, through the same loader", maxPreloadTools),
		fmt.Sprintf("at most %d, plus any skill the request names", maxListedSkills),
		fmt.Sprintf("%d for the local model, %d for a remote backend", poolLocal, poolRemote),
		fmt.Sprintf("rated below %.2f is dropped", jev.DropAt),
		fmt.Sprintf("at or above %.2f is added", jev.StrongAt),
		fmt.Sprintf("counts as %.2f", jev.KeepAt),
		"weighted three times",
		"more skills are installed but not listed",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/jev-jobs.md does not state %q", want)
		}
	}
}
