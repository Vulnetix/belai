package tui

import (
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

func TestGuardrailsPrecedenceProfileBeatsSettings(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "loose", agentprofile.AgentProfile{
		Guardrails: boolPtr(false),
	})

	a := New(Options{})
	a.settings.Guardrails = boolPtr(true)
	a.namedAgent = "loose"
	a.live = nil // guardrailsEnabled must work without live

	if a.guardrailsEnabled() {
		t.Fatal("profile guardrails=false should beat settings guardrails=true")
	}
}

func TestGuardrailsPrecedenceToggleBeatsProfile(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "loose", agentprofile.AgentProfile{
		Guardrails: boolPtr(false),
	})

	a := New(Options{})
	a.settings.Guardrails = boolPtr(true)
	a.namedAgent = "loose"
	v := true
	a.guardrailsOverride = &v

	if !a.guardrailsEnabled() {
		t.Fatal("live toggle guardrails=true should beat profile guardrails=false")
	}
}

func TestAskPrecedenceProfileBeatsSettings(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "silent", agentprofile.AgentProfile{
		AskPermission: boolPtr(false),
	})

	a := New(Options{})
	a.settings.AskPermission = boolPtr(true)
	a.namedAgent = "silent"

	if a.askEnabled() {
		t.Fatal("profile ask=false should beat settings ask=true")
	}
}

func TestAskPrecedenceToggleBeatsProfile(t *testing.T) {
	agentTestHome(t)
	writeAgentProfile(t, "silent", agentprofile.AgentProfile{
		AskPermission: boolPtr(false),
	})

	a := New(Options{})
	a.settings.AskPermission = boolPtr(false)
	a.namedAgent = "silent"
	v := true
	a.askOverride = &v

	if !a.askEnabled() {
		t.Fatal("live toggle ask=true should beat profile ask=false")
	}
}

// boolPtr is declared in classifier_view_test.go in the same package.
