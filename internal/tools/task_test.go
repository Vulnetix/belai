package tools

import (
	"context"
	"errors"
	"testing"
)

func TestTaskDefinition(t *testing.T) {
	var task Task
	def := task.Definition()
	if def.Name != "Task" {
		t.Errorf("name = %q, want Task", def.Name)
	}
	if got := task.Kind(); got != KindSubagent {
		t.Errorf("kind = %q, want %q", got, KindSubagent)
	}
}

func TestTaskFailsClosedWithoutRunner(t *testing.T) {
	task := Task{}
	_, err := task.Execute(context.Background(), map[string]any{
		"description": "x",
		"prompt":      "y",
	})
	if err == nil {
		t.Fatal("expected error when runner is nil")
	}
}

func TestTaskRunsInjectedRunner(t *testing.T) {
	called := false
	task := Task{Runner: func(ctx context.Context, d, p string) (Result, error) {
		called = true
		if d != "desc" || p != "prompt" {
			t.Errorf("runner args = %q, %q", d, p)
		}
		return Result{Kind: KindSubagent, Content: "report"}, nil
	}}
	res, err := task.Execute(context.Background(), map[string]any{
		"description": "desc",
		"prompt":      "prompt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("runner not called")
	}
	if res.Content != "report" {
		t.Errorf("content = %q, want report", res.Content)
	}
}

func TestTaskRequiresDescriptionAndPrompt(t *testing.T) {
	task := Task{Runner: func(context.Context, string, string) (Result, error) {
		return Result{}, nil
	}}
	_, err := task.Execute(context.Background(), map[string]any{"description": "only-desc"})
	if err == nil {
		t.Fatal("expected error for missing prompt")
	}
}

func TestTaskSubject(t *testing.T) {
	var task Task
	if got := task.Subject(map[string]any{"description": "scan"}); got != "scan" {
		t.Errorf("subject = %q, want scan", got)
	}
	if got := task.Subject(map[string]any{}); got != "" {
		t.Errorf("subject = %q, want empty", got)
	}
}

func TestTaskErrorPropagates(t *testing.T) {
	want := errors.New("boom")
	task := Task{Runner: func(context.Context, string, string) (Result, error) {
		return Result{}, want
	}}
	_, err := task.Execute(context.Background(), map[string]any{"description": "x", "prompt": "y"})
	if err != want {
		t.Fatalf("err = %v, want %v", err, want)
	}
}
