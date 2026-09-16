package explore

import (
	"strings"
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

func TestPlanGoalSurveyIsDeterministicAndBounded(t *testing.T) {
	a := PlanGoalSurvey("add rate limiting")
	b := PlanGoalSurvey("add rate limiting")

	if len(a) == 0 {
		t.Fatal("goal survey produced no tasks")
	}
	if len(a) > MaxTasks {
		t.Fatalf("goal survey fan-out = %d, want <= %d", len(a), MaxTasks)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("goal survey drifted at %d: %+v vs %+v", i, a[i], b[i])
		}
		if a[i].Index != i {
			t.Fatalf("task %d has Index %d", i, a[i].Index)
		}
	}
}

func TestPlanGoalSurveySurveysRatherThanRepeatingThePrompt(t *testing.T) {
	const goal = "add rate limiting"
	tasks := PlanGoalSurvey(goal)

	for _, task := range tasks {
		if task.Prompt == goal {
			t.Fatalf("task %q just re-asks the goal", task.Reference)
		}
		if !strings.Contains(task.Prompt, goal) {
			t.Fatalf("task %q lost the goal text: %q", task.Reference, task.Prompt)
		}
		if task.Reference == "" {
			t.Fatal("survey task has no reference label")
		}
	}
}

func TestPlanGoalSurveyReferencesAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, task := range PlanGoalSurvey("goal") {
		if seen[task.Reference] {
			t.Fatalf("duplicate survey reference %q", task.Reference)
		}
		seen[task.Reference] = true
	}
}
