package rolemanager

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestNoteServedModelIsANoOpOnAnUntrackedContext(t *testing.T) {
	NoteServedModel(context.Background(), "p/m") // must not panic
}

func TestTrackServedModelKeepsTheLastNonEmptyNote(t *testing.T) {
	ctx, served := TrackServedModel(context.Background())
	if got := served(); got != "" {
		t.Fatalf("fresh slot = %q, want empty", got)
	}
	NoteServedModel(ctx, "fast/m")
	NoteServedModel(ctx, "")
	if got := served(); got != "fast/m" {
		t.Fatalf("served = %q, want fast/m (an empty note is ignored)", got)
	}
}

// servingClassifier notes model as the leaf it is, before it answers (as
// run.classifierFromConfig does), then replies with raw or fails with err.
func servingClassifier(model, raw string, err error) Classifier {
	return ClassifierFunc(func(ctx context.Context, _ ClassifierPayload) (string, error) {
		NoteServedModel(ctx, model)
		if err != nil {
			return "", err
		}
		return raw, nil
	})
}

// captureModels collects the Model of every activity for one event.
func captureModels(t *testing.T, e Event) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	cancel := SetObserver(func(a Activity) {
		if a.Event == e {
			mu.Lock()
			got = append(got, a.Model)
			mu.Unlock()
		}
	})
	t.Cleanup(cancel)
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// Each role-manager activity names the model that answered it, so the feed
// shows the fast tier or a routed winner rather than the agent model.
func TestRoleActivitiesCarryTheServedModel(t *testing.T) {
	const m = "cloudflare-ai-gateway/@cf/deepseek-ai/deepseek-v4-flash-0731"
	ctx := context.Background()
	cases := []struct {
		event Event
		run   func()
	}{
		{EventModeClassify, func() { _, _ = ClassifyMode(ctx, servingClassifier(m, "AGENT", nil), "fix it") }},
		{EventGoalEval, func() { _, _ = EvaluateGoal(ctx, servingClassifier(m, string(GoalComplete), nil), GoalEvalInput{}) }},
		{EventPlanEval, func() {
			_, _ = EvaluatePlanVerdict(ctx, servingClassifier(m, string(PlanComplete), nil), PlanEvalInput{})
		}},
		{EventAgentEval, func() { _, _ = EvaluateAgent(ctx, servingClassifier(m, string(AgentContinue), nil), "g", "o") }},
		{EventGoalDraft, func() {
			_, _ = DraftGoalContract(ctx, servingClassifier(m, "## Verification surface\n- run tests", nil), GoalDraftInput{Prompt: "ship it"})
		}},
		{EventSessionName, func() { _, _ = ParseServedSessionName("fix the parser", m) }},
		{EventCompactionSummary, func() { _, _ = ValidateServedSummary("", m) }},
	}
	for _, c := range cases {
		models := captureModels(t, c.event)
		c.run()
		got := models()
		if len(got) == 0 {
			t.Fatalf("%s: no activity recorded", c.event)
		}
		for _, g := range got {
			if g != m {
				t.Fatalf("%s: activity model = %q, want %q", c.event, g, m)
			}
		}
	}
}

// A call that times out or fails still names the model that was asked: that
// is the model whose speed or availability the line is about. A classifier
// that never reports itself leaves the model empty, and the TUI falls back to
// the agent model rather than inventing one.
func TestFailedCallNamesTheModelThatWasAsked(t *testing.T) {
	models := captureModels(t, EventGoalDraft)
	_, _ = DraftGoalContract(context.Background(), servingClassifier("fast/m", "", context.DeadlineExceeded), GoalDraftInput{Prompt: "ship it"})
	silent := ClassifierFunc(func(context.Context, ClassifierPayload) (string, error) { return "", errors.New("refused") })
	_, _ = DraftGoalContract(context.Background(), silent, GoalDraftInput{Prompt: "ship it"})
	if got := models(); len(got) != 2 || got[0] != "fast/m" || got[1] != "" {
		t.Fatalf("models = %q, want [fast/m, \"\"]", got)
	}
}
