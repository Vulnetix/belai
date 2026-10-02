package agentprofile

import (
	"strings"
	"testing"
)

func TestBuiltinTriageLoadable(t *testing.T) {
	resetDir(t)
	p, err := Load("belai:triage-vulns")
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
		Name:         "belai:custom",
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
	// "belai:triage-vulns" sanitises to "belai_triage-vulns"; a user profile
	// named "belai_triage-vulns" must be rejected to prevent shadowing.
	p := AgentProfile{
		Name:         "belai_triage-vulns",
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
		if p.Name == "belai:triage-vulns" && !p.Builtin {
			t.Fatal("builtin must be flagged")
		}
	}
}

func TestBuiltinIntentProfilesLoadable(t *testing.T) {
	resetDir(t)
	for _, name := range []string{"belai:plan-handoff", "belai:debug", "belai:fanout"} {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		if !p.Builtin {
			t.Errorf("%s: expected built-in flag", name)
		}
		if p.Mode != ModeSingle || p.Autonomy != AutonomySupervised {
			t.Errorf("%s: unexpected fields mode=%q autonomy=%q", name, p.Mode, p.Autonomy)
		}
		if strings.TrimSpace(p.SystemPrompt) == "" {
			t.Errorf("%s: system prompt must be non-empty", name)
		}
	}
}

// belai:triage is the agent the vulnerability row starts on one identifier. It
// is read-only by construction: no Edit, Write, Bash or Git on its surface.
func TestBuiltinTriageByIdentifierIsReadOnly(t *testing.T) {
	resetDir(t)
	p, err := Load("belai:triage")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !p.Builtin || p.Mode != ModeSingle || p.Autonomy != AutonomySupervised {
		t.Fatalf("unexpected fields: builtin=%v mode=%q autonomy=%q", p.Builtin, p.Mode, p.Autonomy)
	}
	want := map[string]bool{"Vulnetix": true, "Read": true, "Grep": true, "Glob": true}
	for _, tool := range p.Tools {
		if !want[tool] {
			t.Errorf("belai:triage lists %q: it must stay read-only", tool)
		}
		delete(want, tool)
	}
	if len(want) != 0 {
		t.Errorf("belai:triage lacks %v", want)
	}
	if !strings.Contains(p.SystemPrompt, "vulnerability_id") || !strings.Contains(p.SystemPrompt, "data") {
		t.Error("the prompt must name the vulnerability_id field and call it data")
	}
}
