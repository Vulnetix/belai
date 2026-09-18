package run

import (
	"regexp"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/delimiters"
	"github.com/vulnetix/signet/internal/nonce"
	"github.com/vulnetix/signet/internal/prompt"
)

func sealPool(t *testing.T) *nonce.Pool {
	t.Helper()
	pool := nonce.New()
	if err := pool.Seed(8); err != nil {
		t.Fatalf("seed nonce pool: %v", err)
	}
	return pool
}

var toolsBlockRe = regexp.MustCompile(`(?s)<tools nonce="([^"]+)" integrity="([^"]+)">(.*?)</tools>`)

// The tool briefing ships as its own sealed <tools> block, carrying a nonce
// from the live pool and an integrity hash over exactly its own content.
func TestSealSystemSealsToolsBlock(t *testing.T) {
	pool := sealPool(t)
	cfg := Config{Provider: "openai", Model: "gpt-5"}
	sealed, err := SealSystem(cfg, pool, prompt.Options{
		Tools: prompt.ToolsOptions{
			Workdir: "/repo",
			Tools:   []prompt.ToolDoc{{Name: "Read", Summary: "Read a file."}},
		},
	})
	if err != nil {
		t.Fatalf("SealSystem: %v", err)
	}

	m := toolsBlockRe.FindStringSubmatch(sealed)
	if m == nil {
		t.Fatalf("no sealed <tools> block in:\n%s", sealed)
	}
	if !pool.Valid(m[1]) {
		t.Errorf("tools block nonce %q is not from the live pool", m[1])
	}
	if want := delimiters.Integrity(m[3]); m[2] != want {
		t.Errorf("tools block integrity = %s, want %s", m[2], want)
	}
	if !strings.Contains(m[3], "- Read — Read a file.") {
		t.Errorf("tools block does not carry the tool list:\n%s", m[3])
	}

	// The system block is still there and is still its own block.
	if !strings.Contains(sealed, "<system nonce=") {
		t.Errorf("system block missing:\n%s", sealed)
	}
}

// A turn with no tools carries no tools block. The classifier turn is
// tool-less by design, and an empty briefing would imply otherwise.
func TestSealSystemOmitsToolsBlockWithoutTools(t *testing.T) {
	sealed, err := SealSystem(Config{Provider: "openai", Model: "gpt-5"}, sealPool(t), prompt.Options{})
	if err != nil {
		t.Fatalf("SealSystem: %v", err)
	}
	if strings.Contains(sealed, "<tools") {
		t.Fatalf("tool-less turn carries a tools block:\n%s", sealed)
	}
}

// Plan mode's restrictions reach the model through the sealed block, not only
// through the absence of the tools themselves.
func TestSealSystemToolsBlockCarriesPlanRestrictions(t *testing.T) {
	sealed, err := SealSystem(Config{Provider: "openai", Model: "gpt-5"}, sealPool(t), prompt.Options{
		Tools: prompt.ToolsOptions{
			PlanMode: true,
			Tools:    []prompt.ToolDoc{{Name: "Read", Summary: "Read a file."}},
		},
	})
	if err != nil {
		t.Fatalf("SealSystem: %v", err)
	}
	m := toolsBlockRe.FindStringSubmatch(sealed)
	if m == nil {
		t.Fatalf("no sealed <tools> block in:\n%s", sealed)
	}
	for _, want := range []string{"Mode: plan", "Bash is not available"} {
		if !strings.Contains(m[3], want) {
			t.Errorf("plan tools block missing %q:\n%s", want, m[3])
		}
	}
}
