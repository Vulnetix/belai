package permissions

import "testing"

func TestAllowRule(t *testing.T) {
	s := Settings{Allow: []string{"Bash(git diff:*)"}}
	if got := s.Evaluate("Bash", "git diff:HEAD"); got != DecisionAllow {
		t.Fatalf("Evaluate = %q, want allow", got)
	}
	if got := s.Evaluate("Bash", "git push:origin"); got != DecisionBlock {
		t.Fatalf("non-matching subject should block, got %q", got)
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
	if got := s.Evaluate("Read", "main.go"); got != DecisionBlock {
		t.Fatalf("non-matching subject should block, got %q", got)
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

func TestUnknownToolBlocked(t *testing.T) {
	s := Settings{Allow: []string{"Read"}}
	if got := s.Evaluate("Write", "file"); got != DecisionBlock {
		t.Fatalf("unknown tool should block, got %q", got)
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
	if got := s.Evaluate("unknown", "x"); got != DecisionBlock {
		t.Fatalf("unknown = %q, want block", got)
	}
}
