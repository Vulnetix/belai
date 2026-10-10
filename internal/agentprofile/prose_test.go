package agentprofile

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/tools"
)

// A built-in profile's prose is persona and judgement. The mechanics (which
// board tools exist, where files are, how the workspace and publishing work,
// what the harness does afterwards) are written by the harness from the claim,
// the workspace and the profile's declared blocks (agent.workerDirective,
// fleet.directive), so every profile gets them and none has to repeat them. A
// prompt that names them couples the profile to internals another profile's
// prose could lean on too, and silently loses them if the name changes.
func TestBuiltinPromptsCarryNoMechanics(t *testing.T) {
	banned := []string{
		tools.KanbanSearchName, tools.KanbanUpdateName, tools.KanbanMoveName, tools.KanbanAddName,
		tools.KanbanHandoffName, tools.KanbanVerdictName, tools.KanbanGateName, tools.KanbanContractName,
		tools.PublishBranchName, tools.CloseDuplicatePRName,
		".vulnetix/", "the harness", "kb+", "workspace note", "described below",
	}
	for _, name := range workerProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := p.Identity + "\n" + p.SystemPrompt
		for _, b := range banned {
			if strings.Contains(text, b) {
				t.Errorf("%s: the prose names %q, which the harness tells every worker itself", name, b)
			}
		}
	}
}
