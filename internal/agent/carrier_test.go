package agent

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/profiles"
	"github.com/vulnetix/signet/internal/prompt"
	"github.com/vulnetix/signet/internal/rolemanager"
)

// An engaged agent profile becomes the system prompt's carrier block: its text
// replaces the default agent framing, while the harness parts that are not the
// agent's business — the identity block naming Signet, the provider and the
// model — stay exactly where they were.
func TestEngagedProfileCarriesTheSystemPrompt(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	const body = "You are a merciless reviewer of Go diffs."
	if _, err := profiles.Save(profiles.Profile{Name: "reviewer", Content: body}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d := rolemanager.ModeDecision{Mode: modes.ModeAgent, AgentName: "reviewer", AppendCarrier: true}
	opts, err := CarrierOptions(t.TempDir(), d, config.State{}, config.Settings{})
	if err != nil {
		t.Fatalf("CarrierOptions: %v", err)
	}
	if opts.Carrier != prompt.CarrierProfile || opts.ProfileText != body {
		t.Fatalf("carrier = %q text = %q, want the profile", opts.Carrier, opts.ProfileText)
	}

	opts.Provider = "anthropic"
	opts.Model = "claude-sonnet-5"
	sys, err := prompt.System(opts)
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	for _, want := range []string{
		"running inside Signet",    // identity: the harness names itself
		"Provider: anthropic",      // identity: the API serving the session
		"Model: claude-sonnet-5",   // identity: which one the model is
		"Active profile:\n" + body, // the engaged agent, in place of the default
	} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "debug assistant") {
		t.Fatalf("the debug profile leaked into an unrelated agent turn:\n%s", sys)
	}
}

// With no agent engaged the prompt keeps its default shape: identity, no
// carrier block.
func TestNoEngagedProfileLeavesTheDefaultPrompt(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())

	d := rolemanager.ModeDecision{Mode: modes.ModeAgent}
	opts, err := CarrierOptions(t.TempDir(), d, config.State{}, config.Settings{})
	if err != nil {
		t.Fatalf("CarrierOptions: %v", err)
	}
	sys, err := prompt.System(opts)
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if strings.Contains(sys, "Active profile:") {
		t.Fatalf("unexpected carrier block:\n%s", sys)
	}
	if !strings.Contains(sys, "running inside Signet") {
		t.Fatalf("identity block missing:\n%s", sys)
	}
}

// A background-agent definition can carry a foreground turn too: its
// system_prompt is the same kind of text, kept in the other tree.
func TestBackgroundDefinitionCarriesTheSystemPrompt(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	const body = "You are the nightly audit."
	p := agentprofile.AgentProfile{
		Name:         "nightly-audit",
		Description:  "audits the repo",
		SystemPrompt: body,
		Mode:         agentprofile.ModeSingle,
	}
	if _, err := agentprofile.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d := rolemanager.ModeDecision{Mode: modes.ModeAgent, AgentName: "nightly-audit", AppendCarrier: true}
	opts, err := CarrierOptions(t.TempDir(), d, config.State{}, config.Settings{})
	if err != nil {
		t.Fatalf("CarrierOptions: %v", err)
	}
	if opts.ProfileText != body {
		t.Fatalf("ProfileText = %q, want the definition's system_prompt", opts.ProfileText)
	}
}

// A flat profile owns a shared name: it is resolved first, so a background
// definition cannot shadow it.
func TestFlatProfileWinsOverBackgroundDefinition(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	if _, err := profiles.Save(profiles.Profile{Name: "reviewer", Content: "flat"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bg := agentprofile.AgentProfile{
		Name: "reviewer", Description: "d", SystemPrompt: "background", Mode: agentprofile.ModeSingle,
	}
	if _, err := agentprofile.Save(bg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	d := rolemanager.ModeDecision{Mode: modes.ModeAgent, AgentName: "reviewer", AppendCarrier: true}
	opts, err := CarrierOptions(t.TempDir(), d, config.State{}, config.Settings{})
	if err != nil {
		t.Fatalf("CarrierOptions: %v", err)
	}
	if opts.ProfileText != "flat" {
		t.Fatalf("ProfileText = %q, want the flat profile", opts.ProfileText)
	}
}
