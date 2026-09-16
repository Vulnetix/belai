package rolemanager

import (
	"context"
	"errors"
	"testing"
)

type recordingClarifyClassifier struct {
	calls int
}

func (r *recordingClarifyClassifier) Classify(_ context.Context, _ ClassifierPayload) (string, error) {
	r.calls++
	if r.calls == 1 {
		return `{"groups":[{"context":"Missing context","options":[{"label":"only one"}]}]}`, nil
	}
	return `{"groups":[{"context":"Which path?","options":[{"label":"A"},{"label":"B"}]}]}`, nil
}

func TestAskClarifyRetriesOnValidationError(t *testing.T) {
	c := &recordingClarifyClassifier{}
	q, err := AskClarify(context.Background(), c, ClarifyInput{Prompt: "p", Findings: "f", Round: "1"}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", c.calls)
	}
	if q.Empty() {
		t.Fatalf("expected non-empty questionnaire")
	}
	if q.Groups[0].Context != "Which path?" {
		t.Fatalf("unexpected context %q", q.Groups[0].Context)
	}
}

func TestAskClarifyFailsClosedAfterMaxAttempts(t *testing.T) {
	c := &recordingClarifyClassifier{}
	_, err := AskClarify(context.Background(), c, ClarifyInput{Prompt: "p", Findings: "f", Round: "1"}, 1)
	if !errors.Is(err, ErrClarifyUnusable) {
		t.Fatalf("expected ErrClarifyUnusable, got %v", err)
	}
}

func TestAskClarifyEmptyGroupsExitsImmediately(t *testing.T) {
	c := &recordingClassifier{raw: `{"groups": []}`}
	q, err := AskClarify(context.Background(), c, ClarifyInput{Prompt: "p", Findings: "f", Round: "1"}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !q.Empty() {
		t.Fatalf("expected empty questionnaire")
	}
}

func TestAskClarifyNoClassifier(t *testing.T) {
	_, err := AskClarify(context.Background(), nil, ClarifyInput{}, 3)
	if err == nil {
		t.Fatalf("expected error for nil classifier")
	}
}

func TestAskClarifyPropagatesClassifierError(t *testing.T) {
	wantErr := errors.New("boom")
	c := &recordingClassifier{err: wantErr}
	_, err := AskClarify(context.Background(), c, ClarifyInput{Prompt: "p"}, 3)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected %v, got %v", wantErr, err)
	}
}

// recordingClassifier is a simpler fake that always returns raw/err.
type recordingClassifier struct {
	raw string
	err error
}

func (r *recordingClassifier) Classify(_ context.Context, _ ClassifierPayload) (string, error) {
	return r.raw, r.err
}

var _ Classifier = (*recordingClassifier)(nil)
var _ Classifier = (*recordingClarifyClassifier)(nil)

// Verify that AskClarify returns the parsed, validated questionnaire so callers
// never have to re-validate.
func TestAskClarifyReturnsValidatedQuestionnaire(t *testing.T) {
	c := &recordingClassifier{raw: `{"groups":[{"context":"Which?","options":[{"label":"A"},{"label":"B"}]}]}`}
	q, err := AskClarify(context.Background(), c, ClarifyInput{Prompt: "p"}, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := q.Validate(); err != nil {
		t.Fatalf("returned questionnaire was not valid: %v", err)
	}
}
