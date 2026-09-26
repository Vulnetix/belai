package tools

import (
	"context"
	"strings"
	"testing"
)

func TestQuestionnaireFromArgs(t *testing.T) {
	q, err := QuestionnaireFromArgs(map[string]any{"questions": []any{
		map[string]any{"question": "Which one?", "header": "DB", "multiSelect": true, "options": []any{
			map[string]any{"label": "A", "description": "first"},
			map[string]any{"label": "B"},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	g := q.Groups[0]
	if g.Context != "DB: Which one?" || !g.Multi || len(g.Options) != 2 || g.Options[0].Description != "first" {
		t.Fatalf("group = %+v", g)
	}
	for name, args := range map[string]map[string]any{
		"no questions": {},
		"one option":   {"questions": []any{map[string]any{"question": "Which?", "options": []any{map[string]any{"label": "A"}}}}},
		"no mark":      {"questions": []any{map[string]any{"question": "Which", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}},
	} {
		if _, err := QuestionnaireFromArgs(args); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := (AskUserQuestion{}).Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("Execute accepted no questions")
	}
}

func TestAskUserDescriptionCautions(t *testing.T) {
	d := AskUserQuestion{}.Definition().Description
	for _, want := range []string{"already asked", "already gives the best answer", "ExitPlanMode"} {
		if !strings.Contains(d, want) {
			t.Errorf("description lacks %q", want)
		}
	}
}
