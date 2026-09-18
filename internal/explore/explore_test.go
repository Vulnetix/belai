package explore

import (
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/clarify"
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
		// Plan mode with no references surveys the repository (PlanSurvey),
		// whatever the prompt's length; the single-task fallback below is what
		// a non-plan explore with no references gets.
		{"plan no refs surveys, short prompt", "investigate", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 3},
		{"goal no refs single task", "investigate", rolemanager.ModeDecision{Mode: modes.ModeGoal, Explore: true}, 1},
		{"agent directive excluded", "review @agent:security-expert @f.txt", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 1},
		{"plan no refs surveys", "investigate something", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true}, 3},
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

func TestPlanSurveyIsNotRawPrompt(t *testing.T) {
	const p = "plan something"
	tasks := PlanSurvey(p)
	if len(tasks) == 0 {
		t.Fatal("plan survey produced no tasks")
	}
	for _, task := range tasks {
		if task.Prompt == p {
			t.Fatalf("plan survey repeats prompt: %q", task.Prompt)
		}
		if !strings.Contains(task.Prompt, p) {
			t.Fatalf("plan survey lost prompt: %q", task.Prompt)
		}
	}
}

func TestPlanUsesPerKindPrompts(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"file.txt", "Read the file"},
		{"docs/arch", "Explore the directory"},
		{"Vulnetix/vdb-site", "Investigate the repository"},
		{"Vulnetix", "Investigate repositories under"},
		{"https://example.com", "Fetch and summarize"},
		{"concept", "Investigate"},
	}
	for _, tc := range cases {
		tasks := Plan("review @"+tc.ref, rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
		if len(tasks) != 1 {
			t.Fatalf("expected one task for %q, got %d", tc.ref, len(tasks))
		}
		if !strings.HasPrefix(tasks[0].Prompt, tc.want) {
			t.Fatalf("prompt for %q = %q, want prefix %q", tc.ref, tasks[0].Prompt, tc.want)
		}
	}
}

func TestPlanRepoPromptIncludesLocalFirstRule(t *testing.T) {
	tasks := Plan("reuse @Vulnetix/vdb-site", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "local index first") {
		t.Fatalf("repo prompt missing local-first rule: %q", tasks[0].Prompt)
	}
}

func TestPlanOrgPromptIncludesLocalFirstRule(t *testing.T) {
	tasks := Plan("reuse @Vulnetix", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "local index first") {
		t.Fatalf("org prompt missing local-first rule: %q", tasks[0].Prompt)
	}
}

func TestClassifyRef(t *testing.T) {
	cases := []struct {
		ref  string
		want RefKind
	}{
		{"file.txt", RefFile},
		{"docs/arch.md", RefFile},
		{"docs/arch", RefDir},
		{"./internal", RefDir},
		{"Vulnetix/vdb-site", RefRepo},
		{"org/repo-name", RefRepo},
		{"Vulnetix", RefOrg},
		{"GitHub", RefOrg},
		{"https://example.com", RefURL},
		{"http://x.y", RefURL},
		{"concept", RefText},
		{"lowercase/slash", RefDir},
	}
	for _, tc := range cases {
		if got := ClassifyRef(tc.ref); got != tc.want {
			t.Errorf("ClassifyRef(%q) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}

func TestPlanClarifiedSkipsEmptyAndSkipped(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		{Context: "Which pool?", Options: []clarify.Option{{Label: "Parent"}, {Label: "Child"}}},
		{Context: "Which format?", Options: []clarify.Option{{Label: "JSON"}, {Label: "YAML"}}},
	}}
	a := clarify.Answers{Items: []clarify.Answer{
		{GroupIndex: 0, Chosen: []int{0}},
		{GroupIndex: 1, Skipped: true},
	}}
	tasks := PlanClarified("do the thing", q, a)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "Parent") {
		t.Fatalf("missing chosen label: %q", tasks[0].Prompt)
	}
	if strings.Contains(tasks[0].Prompt, "format") {
		t.Fatalf("skipped group leaked into tasks")
	}
}

func TestPlanClarifiedIncludesNote(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		{Context: "Which pool?", Options: []clarify.Option{{Label: "Parent"}, {Label: "Child"}}},
	}}
	a := clarify.Answers{Items: []clarify.Answer{
		{GroupIndex: 0, Chosen: []int{1}, Note: "use seed 42"},
	}}
	tasks := PlanClarified("do the thing", q, a)
	if len(tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(tasks))
	}
	if !strings.Contains(tasks[0].Prompt, "use seed 42") {
		t.Fatalf("missing note: %q", tasks[0].Prompt)
	}
}

func TestPlanClarifiedCapsAtMaxTasks(t *testing.T) {
	var groups []clarify.Group
	var items []clarify.Answer
	for i := 0; i < MaxTasks+3; i++ {
		groups = append(groups, clarify.Group{
			Context: "Question?",
			Options: []clarify.Option{{Label: "A"}, {Label: "B"}},
		})
		items = append(items, clarify.Answer{GroupIndex: i, Chosen: []int{0}})
	}
	tasks := PlanClarified("prompt", clarify.Questionnaire{Groups: groups}, clarify.Answers{Items: items})
	if len(tasks) != MaxTasks {
		t.Fatalf("got %d tasks, want %d", len(tasks), MaxTasks)
	}
}

func TestPlanClarifiedDeterministic(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		{Context: "Which?", Options: []clarify.Option{{Label: "A"}, {Label: "B"}}},
	}}
	a := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}
	if got, want := PlanClarified("p", q, a), PlanClarified("p", q, a); !slicesEqual(got, want) {
		t.Fatalf("PlanClarified not deterministic")
	}
}

func slicesEqual(a, b []Task) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
