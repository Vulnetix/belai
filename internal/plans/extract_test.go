package plans

import (
	"reflect"
	"testing"
)

func TestExtractSteps(t *testing.T) {
	text := `Here is my plan.

Plan:
1. read the code
2. fix the bug
3) ship it

That should do it.`
	want := []string{"read the code", "fix the bug", "ship it"}
	got, err := ExtractSteps(text)
	if err != nil {
		t.Fatalf("ExtractSteps: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("steps = %v, want %v", got, want)
	}
}

func TestExtractStepsStopsAtProse(t *testing.T) {
	text := "Plan:\n1. do a\n2. do b\n\nSome prose here\n3. should not be included"
	got, err := ExtractSteps(text)
	if err != nil {
		t.Fatalf("ExtractSteps: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"do a", "do b"}) {
		t.Fatalf("steps = %v", got)
	}
}

func TestExtractStepsNoPlanHeader(t *testing.T) {
	if _, err := ExtractSteps("just text\n1. not a plan"); err == nil {
		t.Fatalf("expected error without Plan: header")
	}
}

func TestExtractStepsNoSteps(t *testing.T) {
	if _, err := ExtractSteps("Plan:\nno numbered steps here"); err == nil {
		t.Fatalf("expected error without numbered steps")
	}
}
