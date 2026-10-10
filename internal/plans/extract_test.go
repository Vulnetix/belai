package plans

import (
	"reflect"
	"strings"
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

func TestExtractDocDropsNarrationAndNeedsASummaryAndSteps(t *testing.T) {
	plan := "# Add PATCH\n\n## Summary\n\nDo it.\n\n## Key Changes\n\n1. Change a\n   - Files: a.go\n   - Verify: go test ./...\n\n## Assumptions\n\n- x\n"
	doc, ok := ExtractDoc("I have all the information needed. Grounding complete.\n\n" + plan)
	if !ok || strings.Contains(doc, "Grounding") || !strings.HasPrefix(doc, "# Add PATCH") {
		t.Fatalf("ExtractDoc = %q, %v", doc, ok)
	}
	// Without a title the document starts at its first section.
	doc, ok = ExtractDoc("Sure.\n## Summary\n\nx\n\n## Steps\n\n1. one\n")
	if !ok || !strings.HasPrefix(doc, "## Summary") {
		t.Fatalf("untitled ExtractDoc = %q, %v", doc, ok)
	}
	for name, reply := range map[string]string{
		"prose":           "I read the files and will plan next.",
		"checklist only":  "Plan:\n1. read\n2. write\n",
		"no summary":      "## Key Changes\n\n1. one\n",
		"no steps":        "# T\n\n## Summary\n\nx\n",
		"heading in code": "```\n# T\n```\nnothing else",
	} {
		if doc, ok := ExtractDoc(reply); ok {
			t.Errorf("%s: ExtractDoc accepted %q", name, doc)
		}
	}
}
