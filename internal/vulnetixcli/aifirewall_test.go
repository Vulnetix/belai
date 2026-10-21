package vulnetixcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI writes a script that prints out and exits with code, and records
// its arguments.
func fakeCLI(t *testing.T, out string, code int) (CLI, string) {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\ncat <<'EOF'\n" + out + "\nEOF\nexit " + string(rune('0'+code)) + "\n"
	path := filepath.Join(dir, "vulnetix")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return CLI{Path: path}, argsFile
}

func TestAiFirewallGuardrails(t *testing.T) {
	c, argsFile := fakeCLI(t, `{"guardrails":[{"uuid":"u1","name":"deny-bash","ruleType":"tool_deny","action":"strip","pattern":"Bash","enabled":true,"priority":100}]}`, 0)
	got, err := c.AiFirewallGuardrails(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].RuleType != "tool_deny" || got[0].Pattern != "Bash" || !got[0].Enabled {
		t.Fatalf("guardrails = %+v", got)
	}
	args, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(args), "--disable-memory\nai-firewall\nget\n-o\njson") {
		t.Fatalf("args = %q", args)
	}
}

func TestApplyGuardrail(t *testing.T) {
	c, _ := fakeCLI(t, `{"guardrail":{"uuid":"g-123"}}`, 0)
	uuid, err := c.ApplyGuardrail(context.Background(), []string{"ai-firewall", "policy", "guardrail", "belai-deny-bash", "--rule-type", "tool_deny"})
	if err != nil || uuid != "g-123" {
		t.Fatalf("uuid=%q err=%v", uuid, err)
	}
}

func TestApplyGuardrailRefusesOtherCommands(t *testing.T) {
	c, _ := fakeCLI(t, `{}`, 0)
	if _, err := c.ApplyGuardrail(context.Background(), []string{"auth", "logout", "x", "y"}); err == nil {
		t.Fatal("a non-guardrail command ran")
	}
}

func TestApplyGuardrailPlanLocked(t *testing.T) {
	c, _ := fakeCLI(t, `{"planLocked":true}`, 0)
	if _, err := c.ApplyGuardrail(context.Background(), []string{"ai-firewall", "policy", "guardrail", "n"}); !errors.Is(err, ErrPlanLocked) {
		t.Fatalf("err = %v", err)
	}
	c, _ = fakeCLI(t, `Error: guardrails require the Teams plan`, 1)
	if _, err := c.ApplyGuardrail(context.Background(), []string{"ai-firewall", "policy", "guardrail", "n"}); !errors.Is(err, ErrPlanLocked) {
		t.Fatalf("err = %v", err)
	}
}
