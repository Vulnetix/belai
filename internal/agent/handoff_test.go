package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

func TestHandoffGateRefusesEditBeforeUpdatePlan(t *testing.T) {
	s := testSession(t)
	s.turnIntent = rolemanager.IntentHandoff
	s.handoffUpdatePlanCalled = false

	call := rolemanager.ToolCall{Name: "Edit", Args: map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if !strings.Contains(out, "update_plan") {
		t.Fatalf("expected handoff refusal, got %q", out)
	}
}

func TestHandoffGateAllowsUpdatePlanFirst(t *testing.T) {
	s := testSession(t)
	s.turnIntent = rolemanager.IntentHandoff
	s.handoffUpdatePlanCalled = false

	call := rolemanager.ToolCall{Name: "update_plan", Args: map[string]any{"todos": []any{map[string]any{"id": "1", "content": "step", "status": "in_progress"}}}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if strings.Contains(out, "withheld") {
		t.Fatalf("expected update_plan to succeed, got %q", out)
	}
	if !s.handoffUpdatePlanCalled {
		t.Fatal("handoffUpdatePlanCalled not set")
	}
}

func TestHandoffGateAllowsEditAfterUpdatePlan(t *testing.T) {
	s := testSession(t)
	s.turnIntent = rolemanager.IntentHandoff
	s.handoffUpdatePlanCalled = true

	call := rolemanager.ToolCall{Name: "Edit", Args: map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	// The call may still be refused for other reasons (permission, file not
	// existing), but not by the handoff gate.
	if strings.Contains(out, "update_plan") {
		t.Fatalf("unexpected handoff refusal after update_plan: %q", out)
	}
}

func TestHandoffGateNoEffectOutsideHandoff(t *testing.T) {
	s := testSession(t)
	s.turnIntent = rolemanager.IntentAgent
	s.handoffUpdatePlanCalled = false

	call := rolemanager.ToolCall{Name: "Edit", Args: map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if strings.Contains(out, "update_plan") {
		t.Fatalf("unexpected handoff refusal outside handoff: %q", out)
	}
}

func TestHandoffScopeRefusesOutOfScopeRead(t *testing.T) {
	s := testSession(t)
	s.scope = []string{"internal/plan.go"}

	call := rolemanager.ToolCall{Name: "Read", Args: map[string]any{"file_path": "other.go"}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if !strings.Contains(out, "outside the handoff scope") {
		t.Fatalf("expected out-of-scope refusal, got %q", out)
	}
}

func TestHandoffScopeRefusesPathlessRead(t *testing.T) {
	s := testSession(t)
	s.scope = []string{"internal/plan.go"}

	call := rolemanager.ToolCall{Name: "Read", Args: map[string]any{}}
	out := s.executeCall(context.Background(), call, func(Event) {}, nil)
	if !strings.Contains(out, "no path argument") {
		t.Fatalf("expected pathless-refusal, got %q", out)
	}
}

func testSession(t *testing.T) *Session {
	root := t.TempDir()
	s, err := NewSession(Options{
		Cfg:      run.Config{Provider: "openai", BaseURL: "http://localhost", APIKey: "test", Model: "test"},
		Registry: tools.Default(root, false),
		Posture:  posture.Defaults(),
		Workdir:  root,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
