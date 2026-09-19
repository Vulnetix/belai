package agentprofile

import (
	"encoding/json"
	"strings"
	"testing"
)

func ptr(b bool) *bool { return &b }

func TestProfileRoundTripsNewFields(t *testing.T) {
	p := AgentProfile{
		Name:          "test",
		Description:   "d",
		SystemPrompt:  "sp",
		Mode:          ModeSingle,
		Provider:      "llama-server",
		Model:         "default",
		Effort:        "low",
		Guardrails:    ptr(true),
		AskPermission: ptr(false),
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got AgentProfile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Provider != "llama-server" || got.Model != "default" || got.Effort != "low" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.Guardrails == nil || !*got.Guardrails {
		t.Fatal("guardrails lost")
	}
	if got.AskPermission == nil || *got.AskPermission {
		t.Fatal("ask_permission lost")
	}
}

func TestValidateRejectsBadEffort(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		Effort:       "extreme",
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("expected effort error, got %v", err)
	}
}

func TestValidateRejectsInvalidProvider(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
		Provider:     "not-a-provider!",
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestValidateRejectsUnattendedUnguardedUnbounded(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeLoop,
		Autonomy:     AutonomyAutonomous,
		Guardrails:   ptr(false),
	}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "max_iterations") {
		t.Fatalf("expected max_iterations error, got %v", err)
	}
}

func TestValidateAllowsUnguardedWithMaxIterations(t *testing.T) {
	p := AgentProfile{
		Name:          "test",
		Description:   "d",
		SystemPrompt:  "sp",
		Mode:          ModeLoop,
		Autonomy:      AutonomyAutonomous,
		Guardrails:    ptr(false),
		MaxIterations: 10,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAllowsLoopWithoutGuardrailsDrop(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeLoop,
		Autonomy:     AutonomyAutonomous,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAcceptsEmptyProviderAndModel(t *testing.T) {
	p := AgentProfile{
		Name:         "test",
		Description:  "d",
		SystemPrompt: "sp",
		Mode:         ModeSingle,
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
