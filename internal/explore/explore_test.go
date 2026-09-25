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
	var refs []string
	for i := 0; i < MaxTasks+5; i++ {
		refs = append(refs, "@"+string(rune('a'+i)))
	}
	prompt := "review " + strings.Join(refs, " ")
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
		ref    string
		want   string
		budget int
	}{
		{"file.txt", "Read the file", budgetFile},
		{"docs/arch", "In the directory", budgetFocus},
		{"Vulnetix/vdb-site", "In the repository", budgetRepo},
		{"Vulnetix", "Find which repositories under", budgetRepo},
		{"https://example.com", "Fetch", budgetURL},
		{"concept", "Find where", budgetLocate},
	}
	for _, tc := range cases {
		tasks := Plan("review @"+tc.ref, rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
		if len(tasks) != 1 {
			t.Fatalf("expected one task for %q, got %d", tc.ref, len(tasks))
		}
		if !strings.HasPrefix(tasks[0].Prompt, tc.want) {
			t.Fatalf("prompt for %q = %q, want prefix %q", tc.ref, tasks[0].Prompt, tc.want)
		}
		if tasks[0].Budget != tc.budget {
			t.Errorf("budget for %q = %d, want %d", tc.ref, tasks[0].Budget, tc.budget)
		}
		if !strings.HasSuffix(tasks[0].Prompt, reportContract) {
			t.Errorf("prompt for %q lacks the report contract", tc.ref)
		}
	}
}

// Every generated task is narrow: a small budget and the terse report
// contract. The plan survey no longer re-surveys the repository structure
// the map already carries.
func TestTasksAreNarrowAndBudgeted(t *testing.T) {
	var all []Task
	all = append(all, PlanSurvey("add caching")...)
	all = append(all, PlanSurveyWithEntrypoints("add caching", []string{"cmd/x/main.go"})...)
	all = append(all, PlanGoalSurvey("add caching")...)
	all = append(all, Plan("fix it", rolemanager.ModeDecision{Mode: modes.ModeGoal, Explore: true})...)
	q := clarify.Questionnaire{Groups: []clarify.Group{{Context: "Which?", Options: []clarify.Option{{Label: "a"}}}}}
	all = append(all, PlanClarified("p", q, clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}})...)
	for _, task := range all {
		if task.Budget <= 0 || task.Budget > budgetRepo {
			t.Errorf("task %q budget %d", task.Reference, task.Budget)
		}
		if !strings.HasSuffix(task.Prompt, reportContract) {
			t.Errorf("task %q lacks the report contract", task.Reference)
		}
		if task.Reference == "repository structure" {
			t.Error("the survey still re-surveys the repository structure")
		}
	}
	if got := PlanSurvey("x"); got[0].Reference != SurveyReference {
		t.Errorf("first survey task = %q, want %q", got[0].Reference, SurveyReference)
	}
	if n := len(PlanSurveyWithEntrypoints("x", []string{"main.go"})); n != 4 {
		t.Errorf("survey with entrypoints = %d tasks, want 4 (locate, call path, tests, docs)", n)
	}
}

func TestDropAttachedSkipsAttachedFiles(t *testing.T) {
	tasks := Plan("review @a.go @docs @b.go", rolemanager.ModeDecision{Mode: modes.ModePlan, Explore: true})
	got := DropAttached(tasks, map[string]bool{"a.go": true, "docs": true})
	var refs []string
	for i, task := range got {
		refs = append(refs, task.Reference)
		if task.Index != i {
			t.Errorf("task %q index %d, want %d", task.Reference, task.Index, i)
		}
	}
	// docs is a directory reference: an attached listing is not the files,
	// so its task stays.
	if strings.Join(refs, ",") != "b.go,docs" {
		t.Errorf("remaining = %v", refs)
	}
	if len(DropAttached(tasks, nil)) != len(tasks) {
		t.Error("nil attachments dropped tasks")
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

// A goal-mode survey exists to point at edits. It must name concrete targets
// rather than produce the structural tour plan mode wants, and it must stay
// small: every extra task is another read-only round trip before any work.
func TestPlanGoalSurveyTargetsEdits(t *testing.T) {
	tasks := PlanGoalSurvey("add rate limiting")
	if len(tasks) != 2 {
		t.Fatalf("goal survey tasks = %d, want 2", len(tasks))
	}
	if tasks[0].Reference != "edit targets" || tasks[1].Reference != "verification" {
		t.Fatalf("goal survey references = %q, %q", tasks[0].Reference, tasks[1].Reference)
	}
	if !strings.Contains(tasks[0].Prompt, "path:line") {
		t.Fatalf("the edit-target task should ask for path:line targets: %q", tasks[0].Prompt)
	}
	for _, task := range tasks {
		if strings.Contains(task.Prompt, "top-level directories") {
			t.Fatalf("goal survey %q still asks for a structural tour: %q", task.Reference, task.Prompt)
		}
	}
}

// PlanReview gives every non-empty scanner report its own subagent task that
// carries the report as evidence (never in the prompt text), asks for the
// remediation paths, and gets the larger review report bound.
func TestPlanReview(t *testing.T) {
	reports := []ReviewReport{
		{Scanner: "sast", Label: "sast report", Body: "S1 high a.go:1"},
		{Scanner: "iac", Label: "iac report", Body: "   "},
		{Label: "sbom report", Body: "CVE-2026-0001 critical"},
	}
	tasks := PlanReview("remediate the review", reports)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2 (the empty report is skipped)", len(tasks))
	}
	for i, tk := range tasks {
		if tk.Index != i {
			t.Errorf("task %d Index = %d", i, tk.Index)
		}
		if tk.Budget != budgetReview || tk.ReportBytes != ReviewReportBytes {
			t.Errorf("task %d budget/report = %d/%d", i, tk.Budget, tk.ReportBytes)
		}
		if strings.Contains(tk.Prompt, tk.Evidence) {
			t.Errorf("task %d splices its evidence into the prompt", i)
		}
		for _, want := range []string{"remediate the review", "options:", "none:", "read-only"} {
			if !strings.Contains(tk.Prompt, want) {
				t.Errorf("task %d prompt lacks %q", i, want)
			}
		}
	}
	if tasks[0].Reference != "vulnetix sast" || tasks[0].Evidence != "S1 high a.go:1" || tasks[0].EvidenceLabel != "sast report" {
		t.Errorf("sast task = %+v", tasks[0])
	}
	if tasks[1].Reference != "vulnetix sbom report" {
		t.Errorf("a report with no scanner name falls back to its label, got %q", tasks[1].Reference)
	}

	many := make([]ReviewReport, MaxTasks+3)
	for i := range many {
		many[i] = ReviewReport{Scanner: "s", Body: "x"}
	}
	if got := len(PlanReview("p", many)); got != MaxTasks {
		t.Errorf("fan-out = %d, want the MaxTasks cap %d", got, MaxTasks)
	}
}
