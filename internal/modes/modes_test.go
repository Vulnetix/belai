package modes

import "testing"

func TestDefaultIsAgent(t *testing.T) {
	if Default() != ModeAgent {
		t.Fatalf("Default = %q, want agent", Default())
	}
}

func TestParse(t *testing.T) {
	valid := map[string]Mode{
		"agent": ModeAgent,
		"plan":  ModePlan,
		"goal":  ModeGoal,
	}
	for in, want := range valid {
		got, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := Parse("bogus"); err == nil {
		t.Fatalf("expected unknown mode to be rejected")
	}
}
