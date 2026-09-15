package explore

import (
	"testing"

	"github.com/vulnetix/signet/internal/modes"
	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestPlanExploreModes(t *testing.T) {
	cases := []struct {
		name     string
		prompt   string
		decision rolemanager.ModeDecision
		want     int
	}{
		{"agent no explore", "fix the bug", rolemanager.ModeDecision{Mode: modes.ModeAgent}, 0},
		{"plan explores", "review @a.txt @b.txt", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 2},
		{"goal with refs explores", "ship @x", rolemanager.ModeDecision{Mode: modes.ModeGoal, Explore: true}, 1},
		{"goal no refs no explore", "ship", rolemanager.ModeDecision{Mode: modes.ModeGoal}, 0},
		{"no refs single task", "investigate", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 1},
		{"agent directive excluded", "review @agent:security-expert @f.txt", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Plan(tc.prompt, tc.decision)
			if len(got) != tc.want {
				t.Fatalf("Plan(%q) = %d tasks, want %d", tc.prompt, len(got), tc.want)
			}
		})
	}
}

func TestPlanCapsFanOut(t *testing.T) {
	prompt := "review @a @b @c @d @e @f @g"
	tasks := Plan(prompt, rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	if len(tasks) != MaxTasks {
		t.Fatalf("Plan fan-out = %d, want %d", len(tasks), MaxTasks)
	}
}

func TestPlanDeterministicOrder(t *testing.T) {
	prompt := "review @zebra @alpha @mike"
	a := Plan(prompt, rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	b := Plan(prompt, rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	for i := range a {
		if a[i].Reference != b[i].Reference {
			t.Fatalf("Plan order drifted: %v vs %v", a, b)
		}
	}
}
