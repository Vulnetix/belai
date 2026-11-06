package agentprofile

import (
	"os"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kanban"
)

// The Software Factory's fixer workers each claim one label, work on a
// worktree branch and hand the result to review. The labels are the ones the
// factory applies to a card, so a rename here breaks the handoff.
var factoryFixers = map[string]string{
	"belai:pqc-fixer":       "sf:pqc",
	"belai:iac-fixer":       "sf:fix-iac",
	"belai:container-fixer": "sf:fix-container",
	"belai:license-fixer":   "license",
	"belai:defend-writer":   "sf:no-fix",
}

func TestFactoryFixerProfiles(t *testing.T) {
	resetDir(t)
	doc, err := os.ReadFile("../../docs/cli-jobs.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, label := range factoryFixers {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		k := p.Kanban
		switch {
		case !p.Builtin, p.Mode != ModeWorker:
			t.Errorf("%s: not a built-in worker", name)
		case k == nil || len(k.Labels) != 1 || k.Labels[0] != label:
			t.Errorf("%s: claims %v, want %q", name, k, label)
		case kanban.NormLabels([]string{label})[0] != label:
			t.Errorf("%s: label %q is changed by normalisation", name, label)
		case k.OnSuccess.List != "review" || len(k.OnSuccess.Labels) != 1 || k.OnSuccess.Labels[0] != "needs-review":
			t.Errorf("%s: on_success is %+v, want review with needs-review", name, k.OnSuccess)
		case len(k.OnSuccess.DropLabels) != 1 || k.OnSuccess.DropLabels[0] != label:
			t.Errorf("%s: must drop its own label on success", name)
		case p.IsolationMode() != IsolationWorktree || p.PublishMode() != PublishAgent:
			t.Errorf("%s: needs a worktree and agent publishing", name)
		case k.CLI != nil:
			t.Errorf("%s: a fixer is a model worker, not a CLI runner", name)
		}
		for _, tool := range p.Tools {
			if tool == "GH" || tool == "WebFetch" || tool == "WebSearch" {
				t.Errorf("%s: %s reaches outside the repository", name, tool)
			}
		}
		for _, want := range []string{"untrusted data", "Never weaken", "Never merge"} {
			if !strings.Contains(p.SystemPrompt, want) {
				t.Errorf("%s: prompt lacks %q", name, want)
			}
		}
		if !strings.Contains(string(doc), "`"+name+"`") {
			t.Errorf("docs/cli-jobs.md does not list %s", name)
		}
	}
}

// The fixers that change code are held to the card's gates by the harness.
func TestFactoryFixersVerifyTheirGates(t *testing.T) {
	resetDir(t)
	for _, name := range []string{"belai:pqc-fixer", "belai:iac-fixer", "belai:container-fixer", "belai:license-fixer"} {
		p, err := Load(name)
		if err != nil {
			t.Fatal(err)
		}
		if p.Kanban.Gates.VerifyMode() != VerifyEnforce {
			t.Errorf("%s: gates.verify is %q, want enforce", name, p.Kanban.Gates.VerifyMode())
		}
	}
}
