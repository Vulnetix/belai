package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func script(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func set(dir string, hs ...*Hook) *Set {
	for _, h := range hs {
		h.Dir = dir
	}
	return &Set{Hooks: hs, Runner: &Runner{Timeout: 5 * time.Second, MaxBytes: 64 * 1024}}
}

func TestDispatchPassesEventOnStdin(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "echo.sh", `cat > "$0.in"; echo '{}'`)
	s := set(dir, &Hook{Name: "e", Event: EventPreTool, Command: "echo.sh"})
	s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash", ToolInput: map[string]any{"command": "ls"}})
	got, err := os.ReadFile(filepath.Join(dir, "echo.sh.in"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"tool_name":"Bash"`) || !strings.Contains(string(got), `"command":"ls"`) {
		t.Fatalf("stdin = %s", got)
	}
}

func TestDispatchDenyWins(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "allow.sh", `echo '{"decision":"allow"}'`)
	script(t, dir, "deny.sh", `echo '{"decision":"deny","reason":"no rm"}'`)
	s := set(dir,
		&Hook{Name: "a", Event: EventPreTool, Command: "allow.sh"},
		&Hook{Name: "b", Event: EventPreTool, Command: "deny.sh"},
	)
	o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"})
	if o.Decision != DecisionDeny || o.DeniedBy != "b" {
		t.Fatalf("outcome = %+v", o)
	}
	if len(o.Reasons) != 1 || o.Reasons[0].Text != "no rm" {
		t.Fatalf("reasons = %+v", o.Reasons)
	}
}

func TestBlockingHookFailureDenies(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "fail.sh", `exit 3`)
	script(t, dir, "junk.sh", `echo not-json`)
	for _, cmd := range []string{"fail.sh", "junk.sh"} {
		s := set(dir, &Hook{Name: "f", Event: EventPreTool, Command: cmd})
		if o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"}); o.Decision != DecisionDeny {
			t.Errorf("%s: decision = %q, want deny", cmd, o.Decision)
		}
	}
}

func TestBlockingHookTimeoutDenies(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "slow.sh", `sleep 5`)
	s := set(dir, &Hook{Name: "s", Event: EventPreTool, Command: "slow.sh", TimeoutMS: 100})
	start := time.Now()
	o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"})
	if o.Decision != DecisionDeny {
		t.Fatalf("decision = %q, want deny", o.Decision)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not enforced: %v", time.Since(start))
	}
}

func TestNonBlockingFailureDoesNotDeny(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "fail.sh", `exit 1`)
	s := set(dir, &Hook{Name: "f", Event: EventPostTool, Command: "fail.sh"})
	o := s.Dispatch(context.Background(), Input{Event: EventPostTool, ToolName: "Bash"})
	if o.Decision != DecisionNone || len(o.Failures) != 1 {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestNonBlockingDecisionIgnoredContextKept(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "ctx.sh", `echo '{"decision":"deny","additional_context":"vet: clean"}'`)
	s := set(dir, &Hook{Name: "c", Event: EventPostEdit, Command: "ctx.sh"})
	o := s.Dispatch(context.Background(), Input{Event: EventPostEdit, ToolName: "Edit"})
	if o.Decision != DecisionNone {
		t.Fatalf("post hook decision honoured: %q", o.Decision)
	}
	if len(o.Context) != 1 || o.Context[0].Text != "vet: clean" || o.Context[0].Hook != "c" {
		t.Fatalf("context = %+v", o.Context)
	}
}

func TestMatcher(t *testing.T) {
	h := &Hook{Matcher: "Edit|Write|mcp__*"}
	for name, want := range map[string]bool{"Edit": true, "write": true, "mcp__gh__x": true, "Bash": false} {
		if got := h.Matches(name); got != want {
			t.Errorf("Matches(%q) = %v, want %v", name, got, want)
		}
	}
	dir := t.TempDir()
	script(t, dir, "deny.sh", `echo '{"decision":"deny"}'`)
	s := set(dir, &Hook{Name: "d", Event: EventPreTool, Command: "deny.sh", Matcher: "Write"})
	if o := s.Dispatch(context.Background(), Input{Event: EventPreTool, ToolName: "Bash"}); o.Ran != 0 || o.Decision != DecisionNone {
		t.Fatalf("unmatched hook ran: %+v", o)
	}
}

func TestAskAggregates(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "ask.sh", `echo '{"decision":"ask","reason":"check this"}'`)
	s := set(dir, &Hook{Name: "q", Event: EventPreTool, Command: "ask.sh"})
	if o := s.Dispatch(context.Background(), Input{Event: EventPreTool}); o.Decision != DecisionAsk {
		t.Fatalf("decision = %q, want ask", o.Decision)
	}
}

func TestContextIsClipped(t *testing.T) {
	dir := t.TempDir()
	script(t, dir, "big.sh", `printf '{"additional_context":"%s"}' "$(head -c 5000 /dev/zero | tr '\0' a)"`)
	s := set(dir, &Hook{Name: "b", Event: EventPostTool, Command: "big.sh"})
	o := s.Dispatch(context.Background(), Input{Event: EventPostTool})
	if len(o.Context) != 1 || len(o.Context[0].Text) > MaxContextBytes+len("…") {
		t.Fatalf("context not clipped: %d", len(o.Context[0].Text))
	}
}

func TestStrictSchemaRejectsUnknownKey(t *testing.T) {
	if _, err := ParseHookFile([]byte(`{"name":"x","event":"pre_tool","command":"a.sh","matchr":"Bash"}`)); err == nil {
		t.Fatal("unknown key accepted")
	}
	if _, err := ParseHookFile([]byte(`{"name":"x","event":"pre_tool","command":"a.sh","timeout_ms":999999}`)); err == nil {
		t.Fatal("oversized timeout accepted")
	}
	if _, err := ParseHookFile([]byte(`{"name":"x","event":"stop","command":"a.sh","matcher":"[bad"}`)); err == nil {
		t.Fatal("bad matcher accepted")
	}
}

func TestNewEventsValidate(t *testing.T) {
	for _, e := range []string{EventUserPromptSubmit, EventStop, EventPreCompact, EventSubagentStop, EventNotification} {
		if _, err := ValidateHook(Hook{Name: "x", Event: e, Command: "a.sh"}); err != nil {
			t.Errorf("%s: %v", e, err)
		}
	}
}

func TestLoadDirSetsDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.json"), []byte(`{"name":"a","event":"stop","command":"a.sh"}`), 0o600)
	hs, _ := LoadDir(dir, nil)
	if len(hs) != 1 || hs[0].Dir != dir {
		t.Fatalf("hooks = %+v", hs)
	}
}
