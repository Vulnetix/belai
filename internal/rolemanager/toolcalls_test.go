package rolemanager

import "testing"

func TestCheckToolCallsAllKnown(t *testing.T) {
	calls := []ToolCall{
		{ID: "1", Name: "read", Args: map[string]any{"path": "a.txt"}},
		{ID: "2", Name: "bash", Args: map[string]any{"command": "ls"}},
	}
	got, err := CheckToolCalls(calls, []string{"read", "bash", "write"}, PolicyAbort)
	if err != nil {
		t.Fatalf("CheckToolCalls: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(got))
	}
}

func TestCheckToolCallsMismatchAbort(t *testing.T) {
	calls := []ToolCall{{ID: "1", Name: "unknown_tool", Args: nil}}
	if _, err := CheckToolCalls(calls, []string{"read"}, PolicyAbort); err == nil {
		t.Fatalf("expected abort error for mismatched tool call")
	}
}

func TestCheckToolCallsMismatchStrip(t *testing.T) {
	calls := []ToolCall{
		{ID: "1", Name: "read", Args: nil},
		{ID: "2", Name: "bad", Args: nil},
		{ID: "3", Name: "bash", Args: nil},
	}
	got, err := CheckToolCalls(calls, []string{"read", "bash"}, PolicyStrip)
	if err != nil {
		t.Fatalf("CheckToolCalls: %v", err)
	}
	if len(got) != 2 || got[0].Name != "read" || got[1].Name != "bash" {
		t.Fatalf("strip result wrong: %+v", got)
	}
}

func TestCheckToolCallsMismatchIgnore(t *testing.T) {
	calls := []ToolCall{
		{ID: "1", Name: "read", Args: nil},
		{ID: "2", Name: "bad", Args: nil},
	}
	got, err := CheckToolCalls(calls, []string{"read"}, PolicyIgnore)
	if err != nil {
		t.Fatalf("CheckToolCalls: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ignore should forward all calls, got %d", len(got))
	}
}

func TestCheckToolCallsEmptyPolicyDefaultsToAbort(t *testing.T) {
	calls := []ToolCall{{ID: "1", Name: "bad", Args: nil}}
	if _, err := CheckToolCalls(calls, []string{"read"}, ""); err == nil {
		t.Fatalf("empty policy should default to abort")
	}
}
