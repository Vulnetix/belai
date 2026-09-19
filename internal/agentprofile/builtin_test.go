package agentprofile

import (
	"strings"
	"testing"
)

func TestBuiltinTriageLoadable(t *testing.T) {
	resetDir(t)
	p, err := Load("signet:triage-vulns")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !p.Builtin {
		t.Fatal("expected built-in flag")
	}
	if p.Mode != ModeSingle || p.Autonomy != AutonomySupervised {
		t.Fatalf("unexpected builtin fields: mode=%q autonomy=%q", p.Mode, p.Autonomy)
	}
}

func TestBuiltinCannotBeSaved(t *testing.T) {
	resetDir(t)
	p := AgentProfile{
		Name:         "signet:custom",
		Description:  "d",
		SystemPrompt: "s",
		Mode:         ModeSingle,
	}
	if _, err := Save(p); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("expected reserved error, got %v", err)
	}
}

func TestUserProfileCannotCollideWithBuiltin(t *testing.T) {
	resetDir(t)
	// "signet:triage-vulns" sanitises to "signet_triage-vulns"; a user profile
	// named "signet_triage-vulns" must be rejected to prevent shadowing.
	p := AgentProfile{
		Name:         "signet_triage-vulns",
		Description:  "d",
		SystemPrompt: "s",
		Mode:         ModeSingle,
	}
	if _, err := Save(p); err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("expected collision error, got %v", err)
	}
}

func TestBuiltinNotInDiskList(t *testing.T) {
	resetDir(t)
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, p := range list {
		if p.Name == "signet:triage-vulns" && !p.Builtin {
			t.Fatal("builtin must be flagged")
		}
	}
}
