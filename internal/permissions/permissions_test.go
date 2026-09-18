package permissions

import "testing"

func TestAllowRule(t *testing.T) {
	s := Settings{Allow: []string{"Bash(git diff:*)"}}
	if got := s.Evaluate("Bash", "git diff:HEAD"); got != DecisionAllow {
		t.Fatalf("Evaluate = %q, want allow", got)
	}
	if got := s.Evaluate("Bash", "git push:origin"); got != DecisionAllow {
		t.Fatalf("non-matching subject should fall through to default allow, got %q", got)
	}
}

func TestBareToolNameRule(t *testing.T) {
	s := Settings{Allow: []string{"Read"}}
	if got := s.Evaluate("Read", "anything/at/all"); got != DecisionAllow {
		t.Fatalf("Evaluate = %q, want allow", got)
	}
}

func TestAskRule(t *testing.T) {
	s := Settings{Ask: []string{"Read(*.md)"}}
	if got := s.Evaluate("Read", "README.md"); got != DecisionAsk {
		t.Fatalf("Evaluate = %q, want ask", got)
	}
	if got := s.Evaluate("Read", "main.go"); got != DecisionAllow {
		t.Fatalf("non-matching subject should fall through to default allow, got %q", got)
	}
}

func TestDenyWinsOverAllow(t *testing.T) {
	s := Settings{
		Allow: []string{"Bash(git *)"},
		Deny:  []string{"Bash(git push*)"},
	}
	if got := s.Evaluate("Bash", "git push origin"); got != DecisionBlock {
		t.Fatalf("deny should win, got %q", got)
	}
	if got := s.Evaluate("Bash", "git diff"); got != DecisionAllow {
		t.Fatalf("non-denied allow should still allow, got %q", got)
	}
}

func TestBlockAlias(t *testing.T) {
	s := Settings{Block: []string{"Bash(rm *)"}}
	if got := s.Evaluate("Bash", "rm -rf /"); got != DecisionBlock {
		t.Fatalf("block alias should deny, got %q", got)
	}
}

func TestUnmatchedToolAllowed(t *testing.T) {
	// The permissions layer allows any unmatched tool call; unregistered tools
	// are rejected earlier by the agent's registry check, not here.
	s := Settings{Allow: []string{"Read"}}
	if got := s.Evaluate("Write", "file"); got != DecisionAllow {
		t.Fatalf("unmatched tool should be allowed by default, got %q", got)
	}
	empty := Settings{}
	if got := empty.Evaluate("AnyTool", "x"); got != DecisionAllow {
		t.Fatalf("no rules should allow by default, got %q", got)
	}
}

func TestFromSimple(t *testing.T) {
	s := FromSimple(map[string]string{
		"bash": "ask",
		"read": "allow",
		"rm":   "block",
	})
	if got := s.Evaluate("bash", "ls"); got != DecisionAsk {
		t.Fatalf("bash = %q, want ask", got)
	}
	if got := s.Evaluate("read", "x"); got != DecisionAllow {
		t.Fatalf("read = %q, want allow", got)
	}
	if got := s.Evaluate("rm", "x"); got != DecisionBlock {
		t.Fatalf("rm = %q, want block", got)
	}
	if got := s.Evaluate("unknown", "x"); got != DecisionAllow {
		t.Fatalf("unknown = %q, want allow (default)", got)
	}
}

func TestFrom(t *testing.T) {
	s := From([]string{"Read"}, []string{"Bash(ls)"}, []string{"Bash(rm *)"})
	if got := s.Evaluate("Read", "x"); got != DecisionAllow {
		t.Fatalf("Read = %q", got)
	}
	if got := s.Evaluate("Bash", "ls"); got != DecisionAsk {
		t.Fatalf("ls = %q", got)
	}
	if got := s.Evaluate("Bash", "rm -rf /"); got != DecisionBlock {
		t.Fatalf("rm = %q", got)
	}
}

func TestExplainReturnsDecidingRule(t *testing.T) {
	s := Settings{Allow: []string{"Read(./src/**)"}}
	dec, rule := s.Explain("Read", "./src/main.go")
	if dec != DecisionAllow || rule != "Read(./src/**)" {
		t.Fatalf("Explain = %q/%q", dec, rule)
	}
	dec, rule = s.Explain("Read", "./other.go")
	if dec != DecisionAllow || rule != "" {
		t.Fatalf("default allow = %q/%q", dec, rule)
	}
}

func TestValidateRule(t *testing.T) {
	valid := []string{"Read", "Read(./src/**)", "Bash(git diff:*)", "Read(*.md)"}
	for _, r := range valid {
		if err := ValidateRule(r); err != nil {
			t.Fatalf("ValidateRule(%q) = %v", r, err)
		}
	}
	invalid := []string{"", "Read(", "Read()", "Bash(git [", "Tool(x)(y"}
	for _, r := range invalid {
		if err := ValidateRule(r); err == nil {
			t.Fatalf("ValidateRule(%q) should fail", r)
		}
	}
}

func TestCaseInsensitiveToolNames(t *testing.T) {
	s := Settings{Allow: []string{"read"}}
	if got := s.Evaluate("Read", "file.go"); got != DecisionAllow {
		t.Fatalf("lowercase rule should match canonical tool, got %q", got)
	}
	s2 := Settings{Deny: []string{"BASH(rm *)"}}
	if got := s2.Evaluate("Bash", "rm -rf /"); got != DecisionBlock {
		t.Fatalf("mixed-case rule should deny, got %q", got)
	}
}

func TestDoubleStarGlob(t *testing.T) {
	s := Settings{Allow: []string{"Read(./src/**)"}}
	if got := s.Evaluate("Read", "./src/a/b/c.go"); got != DecisionAllow {
		t.Fatalf("** should cross separators, got %q", got)
	}
}

func TestQuestionMarkMatchesOneRune(t *testing.T) {
	s := Settings{Allow: []string{"Read(a?c)"}}
	if got := s.Evaluate("Read", "abc"); got != DecisionAllow {
		t.Fatalf("? should match one char, got %q", got)
	}
	if got := s.Evaluate("Read", "abbc"); got != DecisionAllow {
		t.Fatalf("? should not match two chars, want default allow, got %q", got)
	}
}

func TestExplicitlyAllowsDefaultsToFalse(t *testing.T) {
	s := Settings{}
	if s.ExplicitlyAllows("Bash", "git status") {
		t.Fatal("empty settings should not explicitly allow")
	}
	s = Settings{Allow: []string{"Read"}}
	if s.ExplicitlyAllows("Bash", "anything") {
		t.Fatal("Read rule should not allow Bash")
	}
}

func TestExplicitlyAllowsMatchesSubject(t *testing.T) {
	s := Settings{Allow: []string{"Bash(git status)", "Read"}}
	if !s.ExplicitlyAllows("Bash", "git status") {
		t.Fatal("exact Bash rule should explicitly allow")
	}
	if s.ExplicitlyAllows("Bash", "rm -rf /") {
		t.Fatal("non-matching Bash command should not be explicitly allowed")
	}
}

func TestHasAllowRuleReportsAnyAllowForTool(t *testing.T) {
	s := Settings{Allow: []string{"Bash(git status)", "Read"}}
	if !s.HasAllowRule("Bash") {
		t.Fatal("expected HasAllowRule(Bash) = true")
	}
	if !s.HasAllowRule("Read") {
		t.Fatal("expected HasAllowRule(Read) = true")
	}
	if s.HasAllowRule("Write") {
		t.Fatal("expected HasAllowRule(Write) = false")
	}
}
