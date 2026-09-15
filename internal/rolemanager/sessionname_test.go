package rolemanager

import (
	"strings"
	"testing"
)

func TestBuildSessionNamePayload(t *testing.T) {
	p := BuildSessionNamePayload("refactor the parser")
	if p.System == "" {
		t.Fatal("expected non-empty system prompt")
	}
	if p.User != "refactor the parser" {
		t.Fatalf("expected user content preserved, got %q", p.User)
	}
	if len(p.Tools) != 0 || len(p.Skills) != 0 || p.Agent != "" {
		t.Fatal("session name payload should have no tools/skills/agent")
	}
}

func TestMaxSessionNameRunes(t *testing.T) {
	const max = MaxSessionNameRunes
	if max <= 0 {
		t.Fatal("MaxSessionNameRunes should be positive")
	}
}

func TestBuildSessionNamePayloadEmpty(t *testing.T) {
	p := BuildSessionNamePayload("")
	if p.User != "" {
		t.Fatalf("expected empty user, got %q", p.User)
	}
}

func TestBuildSessionNamePayloadLong(t *testing.T) {
	msg := strings.Repeat("x", 1000)
	p := BuildSessionNamePayload(msg)
	if p.User != msg {
		t.Fatal("long message should be preserved exactly")
	}
}
