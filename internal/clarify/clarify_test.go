package clarify

import (
	"strings"
	"testing"
)

func mustQuestionnaire(t *testing.T, raw string) Questionnaire {
	t.Helper()
	q, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return q
}

func TestParseEmptyGroupsIsValid(t *testing.T) {
	q := mustQuestionnaire(t, `{"groups": []}`)
	if !q.Empty() {
		t.Fatalf("expected empty questionnaire")
	}
}

func TestParseStripsFencesAndThinking(t *testing.T) {
	raw := "<thinking>\nI need to ask one question.\n</thinking>\n\n```json\n{\"groups\":[{\"context\":\"Which pool?\",\"options\":[{\"label\":\"Parent\"},{\"label\":\"Child\"}]}]}\n```"
	q := mustQuestionnaire(t, raw)
	if len(q.Groups) != 1 {
		t.Fatalf("got %d groups", len(q.Groups))
	}
}

func TestParseUnknownFieldRejected(t *testing.T) {
	_, err := Parse(`{"groups":[],"extra":1}`)
	if err == nil {
		t.Fatalf("expected unknown field rejection")
	}
}

func TestValidateTooManyGroups(t *testing.T) {
	var groups []Group
	for i := 0; i < 7; i++ {
		groups = append(groups, Group{Context: "Q?", Options: []Option{{Label: "a"}, {Label: "b"}}})
	}
	if err := (Questionnaire{Groups: groups}).Validate(); err == nil || !strings.Contains(err.Error(), "too many groups") {
		t.Fatalf("expected too-many-groups error, got %v", err)
	}
}

func TestValidateTooFewOptions(t *testing.T) {
	q := Questionnaire{Groups: []Group{{Context: "Q?", Options: []Option{{Label: "only"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "at least 2") {
		t.Fatalf("expected too-few-options error, got %v", err)
	}
}

func TestValidateTooManyOptions(t *testing.T) {
	var opts []Option
	for i := 0; i < 5; i++ {
		opts = append(opts, Option{Label: string(rune('a' + i))})
	}
	q := Questionnaire{Groups: []Group{{Context: "Q?", Options: opts}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "at most 4") {
		t.Fatalf("expected too-many-options error, got %v", err)
	}
}

func TestValidateMultilineContext(t *testing.T) {
	q := Questionnaire{Groups: []Group{{Context: "line1\nline2?", Options: []Option{{Label: "a"}, {Label: "b"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "single line") {
		t.Fatalf("expected single-line error, got %v", err)
	}
}

func TestValidateContextDoesNotEndInPunctuation(t *testing.T) {
	q := Questionnaire{Groups: []Group{{Context: "no punctuation", Options: []Option{{Label: "a"}, {Label: "b"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "end with") {
		t.Fatalf("expected punctuation error, got %v", err)
	}
}

func TestValidateDuplicateLabelsRejected(t *testing.T) {
	q := Questionnaire{Groups: []Group{{Context: "Q?", Options: []Option{{Label: "same"}, {Label: "same"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate label") {
		t.Fatalf("expected duplicate-label error, got %v", err)
	}
}

func TestValidateContextLength(t *testing.T) {
	ctx := strings.Repeat("x", 200) + "?"
	q := Questionnaire{Groups: []Group{{Context: ctx, Options: []Option{{Label: "a"}, {Label: "b"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds 200") {
		t.Fatalf("expected context-length error, got %v", err)
	}
}

func TestValidateLabelLength(t *testing.T) {
	label := strings.Repeat("x", 81)
	q := Questionnaire{Groups: []Group{{Context: "Q?", Options: []Option{{Label: label}, {Label: "b"}}}}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "label exceeds") {
		t.Fatalf("expected label-length error, got %v", err)
	}
}

func TestValidateDuplicateContext(t *testing.T) {
	q := Questionnaire{Groups: []Group{
		{Context: "Same?", Options: []Option{{Label: "a"}, {Label: "b"}}},
		{Context: "Same?", Options: []Option{{Label: "c"}, {Label: "d"}}},
	}}
	if err := q.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate context") {
		t.Fatalf("expected duplicate-context error, got %v", err)
	}
}

func TestRenderSingleChoice(t *testing.T) {
	q := Questionnaire{Groups: []Group{
		{Context: "Which pool?", Options: []Option{{Label: "Parent"}, {Label: "Child"}}},
	}}
	a := Answers{Items: []Answer{{GroupIndex: 0, Chosen: []int{1}}}}
	got := a.Render(q)
	want := "Clarifications from the user:\n1. Which pool?\n   chose: Child"
	if got != want {
		t.Fatalf("Render mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestRenderMultiChoice(t *testing.T) {
	q := Questionnaire{Groups: []Group{
		{Context: "Pick tools.", Multi: true, Options: []Option{{Label: "Read"}, {Label: "Bash"}, {Label: "Grep"}}},
	}}
	a := Answers{Items: []Answer{{GroupIndex: 0, Chosen: []int{0, 2}}}}
	got := a.Render(q)
	if !strings.Contains(got, "chose: Read, Grep") {
		t.Fatalf("Render missing multi labels: %q", got)
	}
}

func TestRenderSkipped(t *testing.T) {
	q := Questionnaire{Groups: []Group{
		{Context: "Which pool?", Options: []Option{{Label: "Parent"}, {Label: "Child"}}},
	}}
	a := Answers{Items: []Answer{{GroupIndex: 0, Skipped: true}}}
	got := a.Render(q)
	if !strings.Contains(got, "skipped") {
		t.Fatalf("Render missing skipped: %q", got)
	}
}

func TestRenderNote(t *testing.T) {
	q := Questionnaire{Groups: []Group{
		{Context: "Which pool?", Options: []Option{{Label: "Parent"}, {Label: "Child"}}},
	}}
	a := Answers{Items: []Answer{{GroupIndex: 0, Chosen: []int{0}, Note: "because tests"}}}
	got := a.Render(q)
	if !strings.Contains(got, "note: because tests") {
		t.Fatalf("Render missing note: %q", got)
	}
}
