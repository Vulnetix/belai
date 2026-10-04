package run

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/prompt"
)

// Only the Pix Sandbox build sets environmentFacts. In every other build it is
// nil, so SealSystem adds nothing and nothing reaches for the metadata address.
func TestEnvironmentHookIsNilOutsideTheSandboxBuild(t *testing.T) {
	if sandboxBuild {
		t.Skip("the Pix Sandbox build sets the hook")
	}
	if environmentFacts != nil {
		t.Fatal("environmentFacts is set in a build that is not the Pix Sandbox build")
	}
}

func withEnvironment(t *testing.T, text string, calls *int) {
	t.Helper()
	prev := environmentFacts
	environmentFacts = func() string { *calls++; return text }
	t.Cleanup(func() { environmentFacts = prev })
}

var agentTools = prompt.ToolsOptions{Workdir: "/repo", Tools: []prompt.ToolDoc{{Name: "Read", Summary: "Read a file."}}}

func TestSealSystemCarriesTheEnvironmentOnAnAgentTurn(t *testing.T) {
	var calls int
	withEnvironment(t, "Environment (facts):\n- Pix Sandbox: Large.\n", &calls)

	sealed, err := SealSystem(Config{Provider: "builtin", Model: "pix-smart"}, sealPool(t), prompt.Options{Tools: agentTools})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sealed, "- Pix Sandbox: Large.") || calls != 1 {
		t.Fatalf("calls=%d, sealed:\n%s", calls, sealed)
	}
	// It is in the harness's own system block, not in the tools block.
	m := toolsBlockRe.FindStringSubmatch(sealed)
	if m == nil || strings.Contains(m[3], "Pix Sandbox") {
		t.Fatalf("the environment leaked into the tools block, or there is none:\n%s", sealed)
	}
}

func TestSealSystemLeavesATooltessTurnWithoutTheEnvironment(t *testing.T) {
	var calls int
	withEnvironment(t, "Environment (facts):\n- Pix Sandbox: Large.\n", &calls)

	sealed, err := SealSystem(Config{Provider: "builtin", Model: "pix-fast"}, sealPool(t), prompt.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "Pix Sandbox") || calls != 0 {
		t.Fatalf("a tool-less turn (the classifier) carries the environment; calls=%d:\n%s", calls, sealed)
	}
}

func TestSealSystemKeepsTheCallersOwnEnvironment(t *testing.T) {
	var calls int
	withEnvironment(t, "from the hook", &calls)

	sealed, err := SealSystem(Config{Provider: "builtin", Model: "pix-smart"}, sealPool(t), prompt.Options{Tools: agentTools, Environment: "from the caller"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sealed, "from the caller") || strings.Contains(sealed, "from the hook") || calls != 0 {
		t.Fatalf("calls=%d:\n%s", calls, sealed)
	}
}

func TestSealSystemWithoutTheHookAddsNothing(t *testing.T) {
	prev := environmentFacts
	environmentFacts = nil
	t.Cleanup(func() { environmentFacts = prev })

	sealed, err := SealSystem(Config{Provider: "openai", Model: "gpt-5"}, sealPool(t), prompt.Options{Tools: agentTools})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "Environment (") {
		t.Fatalf("an environment block without the hook:\n%s", sealed)
	}
}
