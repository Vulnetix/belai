package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBashEcho(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("expected hello in %q", res.Content)
	}
}

func TestBashRejectsShellMetacharacters(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	for _, meta := range ";&|$`<>()" {
		_, err := b.Execute(context.Background(), map[string]any{"command": "echo " + string(meta)})
		if err == nil {
			t.Fatalf("expected rejection for metacharacter %q", meta)
		}
	}
}

func TestBashNonZeroExitReturnsOutputAndNoError(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "false"})
	if err != nil {
		t.Fatalf("Execute should not error on non-zero exit: %v", err)
	}
	if !strings.Contains(res.Content, "exit status 1") {
		t.Fatalf("expected exit status in %q", res.Content)
	}
}

func TestBashTimeout(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 100 * time.Millisecond}
	_, err := b.Execute(context.Background(), map[string]any{"command": "sleep 5"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestBashTruncation(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second, MaxBytes: 5}
	res, err := b.Execute(context.Background(), map[string]any{"command": "printf '123456789'"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "truncated at 5 bytes") {
		t.Fatalf("expected truncation marker in %q", res.Content)
	}
}

func TestBashConfinesToRoot(t *testing.T) {
	root := t.TempDir()
	b := &Bash{Root: root, ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Content) != root {
		t.Fatalf("expected dir %q, got %q", root, strings.TrimSpace(res.Content))
	}
}

func TestBashScrubsCredentialEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "super-secret")
	b := &Bash{Root: t.TempDir(), ReadOnly: true, Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(res.Content, "super-secret") {
		t.Fatalf("env output leaked OPENAI_API_KEY")
	}
}

func TestBashResult(t *testing.T) {
	res := BashResult("x")
	if res.Kind != KindBash || res.Content != "x" {
		t.Fatalf("BashResult = %+v", res)
	}
}

func TestShellMetacharactersMatchesPlanmode(t *testing.T) {
	if ShellMetacharacters != ";&|$`<>\n()" {
		t.Fatalf("ShellMetacharacters = %q", ShellMetacharacters)
	}
	_ = os.Environ()
}

func TestBashRejectsNonAllowlisted(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true}
	for _, cmd := range []string{"rm -rf /", "awk '{print > \"x\"}'", "sed -i x", "xargs rm", "git add ."} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err == nil {
			t.Fatalf("Bash(%q) should be rejected", cmd)
		}
	}
}

func TestBashAllowsDataWork(t *testing.T) {
	b := &Bash{Root: t.TempDir(), ReadOnly: true}
	for _, cmd := range []string{"echo hi", "cat x", "git status", "find . -name x", "jq . x"} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err != nil && strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("Bash(%q) should be allowlisted, got %v", cmd, err)
		}
	}
}

func TestBashFullModeRunsShellSyntax(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo a && echo b"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.TrimSpace(res.Content) != "a\nb" {
		t.Fatalf("chained commands = %q, want a\\nb", res.Content)
	}
	res, err = b.Execute(context.Background(), map[string]any{"command": "echo hi | tr a-z A-Z"})
	if err != nil {
		t.Fatalf("Execute pipe: %v", err)
	}
	if strings.TrimSpace(res.Content) != "HI" {
		t.Fatalf("pipe = %q, want HI", res.Content)
	}
}

func TestBashFullModeRunsNonAllowlisted(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	// touch is not in the read-only allowlist; full mode runs it fine.
	res, err := b.Execute(context.Background(), map[string]any{"command": "touch made.txt && ls made.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "made.txt") {
		t.Fatalf("expected made.txt in %q", res.Content)
	}
}

func TestBashDefinitionBranchesOnMode(t *testing.T) {
	full := (&Bash{Root: t.TempDir()}).Definition()
	if !strings.Contains(full.Description, "full shell") {
		t.Fatalf("full-mode description = %q", full.Description)
	}
	ro := (&Bash{Root: t.TempDir(), ReadOnly: true}).Definition()
	if !strings.Contains(ro.Description, "read-only") {
		t.Fatalf("read-only description = %q", ro.Description)
	}
}

func TestDefaultWiresBashReadOnly(t *testing.T) {
	dir := t.TempDir()
	full, ok := Default(dir, false).Find("Bash")
	if !ok {
		t.Fatalf("Bash not registered")
	}
	if b, ok := full.(*Bash); !ok || b.ReadOnly {
		t.Fatalf("Default(dir, false) should give full-mode Bash, got %v", full)
	}
	ro, ok := Default(dir, true).Find("Bash")
	if !ok {
		t.Fatalf("Bash not registered")
	}
	if b, ok := ro.(*Bash); !ok || !b.ReadOnly {
		t.Fatalf("Default(dir, true) should give read-only Bash, got %v", ro)
	}
}
