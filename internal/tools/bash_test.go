package tools

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBashEcho(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "hello") {
		t.Fatalf("expected hello in %q", res.Content)
	}
}

func TestBashRejectsShellMetacharacters(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	for _, meta := range ";&|$`<>()" {
		_, err := b.Execute(context.Background(), map[string]any{"command": "echo " + string(meta)})
		if err == nil {
			t.Fatalf("expected rejection for metacharacter %q", meta)
		}
	}
}

func TestBashNonZeroExitReturnsOutputAndNoError(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
	res, err := b.Execute(context.Background(), map[string]any{"command": "false"})
	if err != nil {
		t.Fatalf("Execute should not error on non-zero exit: %v", err)
	}
	if !strings.Contains(res.Content, "exit status 1") {
		t.Fatalf("expected exit status in %q", res.Content)
	}
}

func TestBashTimeout(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 100 * time.Millisecond}
	_, err := b.Execute(context.Background(), map[string]any{"command": "sleep 5"})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestBashTruncation(t *testing.T) {
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second, MaxBytes: 5}
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
	b := &Bash{Root: root, Timeout: 5 * time.Second}
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
	b := &Bash{Root: t.TempDir(), Timeout: 5 * time.Second}
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
	b := &Bash{Root: t.TempDir()}
	for _, cmd := range []string{"rm -rf /", "awk '{print > \"x\"}'", "sed -i x", "xargs rm", "git add ."} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err == nil {
			t.Fatalf("Bash(%q) should be rejected", cmd)
		}
	}
}

func TestBashAllowsDataWork(t *testing.T) {
	b := &Bash{Root: t.TempDir()}
	for _, cmd := range []string{"echo hi", "cat x", "git status", "find . -name x", "jq . x"} {
		if _, err := b.Execute(context.Background(), map[string]any{"command": cmd}); err != nil && strings.Contains(err.Error(), "allowlist") {
			t.Fatalf("Bash(%q) should be allowlisted, got %v", cmd, err)
		}
	}
}
