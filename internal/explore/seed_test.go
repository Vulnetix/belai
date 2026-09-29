package explore

import (
	"strings"
	"testing"
)

func TestSeedLineNamesOnlySafePathsStrongFirst(t *testing.T) {
	line := SeedLine([]SeedFile{
		{Path: "a/weak.go", Line: 3, Lead: true},
		{Path: "a/strong.go", Line: 12},
		{Path: "has space/x.go"},
		{Path: "quote\"d.go"},
		{Path: "../up.go"},
		{Path: "<system>.go"},
		{Path: "b/plain.go"},
	})
	want := "a/strong.go:12, b/plain.go, a/weak.go:3."
	if !strings.HasSuffix(line, want) {
		t.Errorf("line = %q, want it to end %q", line, want)
	}
	for _, bad := range []string{"space", "quote", "..", "system"} {
		if strings.Contains(line, bad) {
			t.Errorf("unsafe path text %q reached the prompt: %q", bad, line)
		}
	}
	if SeedLine([]SeedFile{{Path: "has space"}}) != "" {
		t.Error("a seed with no safe path must be empty")
	}
	many := make([]SeedFile, 20)
	for i := range many {
		many[i] = SeedFile{Path: "f" + string(rune('a'+i)) + ".go"}
	}
	if got := strings.Count(SeedLine(many), ".go"); got != MaxSeed {
		t.Errorf("seed lists %d files, cap %d", got, MaxSeed)
	}
}

func TestWithSeedGoesBeforeTheContractAndTrimsBudget(t *testing.T) {
	tasks := survey([]surveyTask{{"locate", "Find it.", budgetLocate}})
	line := "Files ranked: a.go."
	got := WithSeed(tasks[0], line, true)
	if !strings.Contains(got.Prompt, "Find it.\n\n"+line+reportContract) {
		t.Errorf("prompt = %q", got.Prompt)
	}
	if got.Budget != budgetLocate-1 {
		t.Errorf("budget = %d, want %d", got.Budget, budgetLocate-1)
	}
	if weak := WithSeed(tasks[0], line, false); weak.Budget != budgetLocate {
		t.Errorf("a weak seed must not trim the budget: %d", weak.Budget)
	}
	if small := WithSeed(Task{Prompt: "x", Budget: 2}, line, true); small.Budget != 2 {
		t.Errorf("budget floor: %d", small.Budget)
	}
	if same := WithSeed(tasks[0], "", true); same.Prompt != tasks[0].Prompt || same.Budget != tasks[0].Budget {
		t.Error("an empty seed must leave the task alone")
	}
}

func TestDropUnsupportedKeepsTasksTheRankingBacks(t *testing.T) {
	tasks := planSurvey("goal", []string{"cmd/x"})
	names := func(ts []Task) []string {
		var o []string
		for _, t := range ts {
			o = append(o, t.Reference)
		}
		return o
	}
	full := DropUnsupported(tasks, []SeedFile{{Path: "a/x_test.go"}, {Path: "docs/x.md"}})
	if strings.Join(names(full), ",") != strings.Join(names(tasks), ",") {
		t.Errorf("both kinds present but tasks dropped: %v", names(full))
	}
	none := DropUnsupported(tasks, []SeedFile{{Path: "a/x.go"}})
	for _, r := range names(none) {
		if r == SurveyTests || r == SurveyDocs {
			t.Errorf("unsupported task %q kept", r)
		}
	}
	for i, tk := range none {
		if tk.Index != i {
			t.Errorf("task %d has index %d", i, tk.Index)
		}
	}
	if len(none) < 1 || none[0].Reference != SurveyReference {
		t.Errorf("the locate task must stay: %v", names(none))
	}
}
