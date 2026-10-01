package permissions

import "testing"

func TestHarnessDenyBlocksUnlessPermitted(t *testing.T) {
	s := From(nil, nil, nil).WithHarness(
		[]string{"Write(*.vulnetix/*)", "Edit(*.vulnetix/*)", "Bash(*git push*)"},
		[]string{"Write(.vulnetix/crews/delivery.md)", "Edit(.vulnetix/crews/delivery.md)"},
	)
	cases := []struct {
		tool, subject string
		want          Decision
	}{
		{"Write", ".vulnetix/settings.json", DecisionBlock},
		{"Edit", ".vulnetix/memory.yaml", DecisionBlock},
		{"Write", ".vulnetix/crews/other.md", DecisionBlock},
		{"Write", ".vulnetix/crews/delivery.md", DecisionAllow},
		{"Edit", ".vulnetix/crews/delivery.md", DecisionAllow},
		{"Write", "src/main.go", DecisionAllow},
		{"Read", ".vulnetix/crews/delivery.md", DecisionAllow},
	}
	for _, c := range cases {
		if got := s.Evaluate(c.tool, c.subject); got != c.want {
			t.Errorf("%s(%s) = %s, want %s", c.tool, c.subject, got, c.want)
		}
	}
}

// A permit is the harness's exemption from its own rules. A rule the user
// wrote, in Deny or Block, still wins.
func TestPermitNeverOverridesAUsersDeny(t *testing.T) {
	s := From(nil, nil, []string{"Write(.vulnetix/crews/*)"}).WithHarness(
		[]string{"Write(*.vulnetix/*)"},
		[]string{"Write(.vulnetix/crews/delivery.md)"},
	)
	if got := s.Evaluate("Write", ".vulnetix/crews/delivery.md"); got != DecisionBlock {
		t.Fatalf("the user's deny must win, got %s", got)
	}
	b := Settings{Block: []string{"Write(.vulnetix/crews/*)"}}.WithHarness(nil, []string{"Write(.vulnetix/crews/delivery.md)"})
	if got := b.Evaluate("Write", ".vulnetix/crews/delivery.md"); got != DecisionBlock {
		t.Fatalf("the user's block must win, got %s", got)
	}
}

func TestPermitDoesNotExemptAShellLine(t *testing.T) {
	s := From(nil, nil, nil).WithHarness([]string{"Bash(*git push*)"}, []string{"Bash(git push origin x)"})
	if got := s.Evaluate("Bash", "git push origin x"); got != DecisionBlock {
		t.Fatalf("a permit exempts file paths, never a command: %s", got)
	}
}

func TestHarnessAndPermitAreNotReadFromSettingsFiles(t *testing.T) {
	var s Settings
	if err := jsonUnmarshal([]byte(`{"harness":["Write(*)"],"permit":["Write(*)"],"deny":["Read(x)"]}`), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Harness) != 0 || len(s.Permit) != 0 || len(s.Deny) != 1 {
		t.Fatalf("settings = %+v", s)
	}
}
