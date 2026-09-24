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

// servingClassifier replies with raw and notes model as the answering leaf.
func servingClassifier(model, raw string, err error) Classifier {
	return ClassifierFunc(func(ctx context.Context, _ ClassifierPayload) (string, error) {
		if err != nil {
			return "", err
		}
		NoteServedModel(ctx, model)
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

// A call that fails in transport has no answering leaf, so its activity
// carries no model and the TUI falls back to the agent model rather than
// inventing one.
func TestFailedCallRecordsNoServedModel(t *testing.T) {
	models := captureModels(t, EventGoalDraft)
	_, _ = DraftGoalContract(context.Background(), servingClassifier("x/y", "", errors.New("refused")), GoalDraftInput{Prompt: "ship it"})
	if got := models(); len(got) != 1 || got[0] != "" {
		t.Fatalf("models = %q, want one empty", got)
	}
}
