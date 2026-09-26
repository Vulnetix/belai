package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/modes"
)

type fakeIntentDetector struct {
	scores map[Intent]float64
	model  string
	err    error
}

func (f *fakeIntentDetector) DetectIntent(ctx context.Context, in DetectInput) (map[Intent]float64, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return f.scores, f.model, nil
}

func TestDetectUsesJevWhenAvailable(t *testing.T) {
	jev := &fakeIntentDetector{scores: map[Intent]float64{IntentPlan: 0.9, IntentAgent: 0.1}, model: "openrouter/typesafe/jev-1.13"}
	llm := &fakeClassifier{}
	det, err := Detect(context.Background(), jev, llm, DetectInput{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if det.Source != "jev" {
		t.Errorf("source = %q, want jev", det.Source)
	}
	if det.Top != IntentPlan {
		t.Errorf("top = %q, want plan", det.Top)
	}
	if det.TopScore != 0.9 {
		t.Errorf("topScore = %v, want 0.9", det.TopScore)
	}
}

func TestDetectFallsBackToLLMOnJevError(t *testing.T) {
	jev := &fakeIntentDetector{err: errors.New("timeout")}
	llm := &fakeClassifier{raw: "GOAL"}
	det, err := Detect(context.Background(), jev, llm, DetectInput{Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if det.Source != "llm" {
		t.Errorf("source = %q, want llm", det.Source)
	}
	if det.Top != IntentGoal {
		t.Errorf("top = %q, want goal", det.Top)
	}
	if det.TopScore != 1.0 {
		t.Errorf("topScore = %v, want 1.0", det.TopScore)
	}
}

func TestDetectFallsBackToLLMWhenJevNil(t *testing.T) {
	llm := &fakeClassifier{raw: "AGENT"}
	det, err := Detect(context.Background(), nil, llm, DetectInput{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if det.Top != IntentAgent {
		t.Errorf("top = %q, want agent", det.Top)
	}
}

func TestResolveConfidentNoSticky(t *testing.T) {
	det := Detection{Top: IntentPlan, TopScore: 0.85, RunnerUp: 0.1}
	intent, ask := Resolve(det, ModeHint{}, true)
	if intent != IntentPlan || ask {
		t.Fatalf("Resolve(plan 0.85, unsticky) = %v, ask=%v; want plan, false", intent, ask)
	}
}

func TestResolveAmbiguousInteractiveAsks(t *testing.T) {
	det := Detection{Top: IntentPlan, TopScore: 0.55, RunnerUp: 0.45}
	intent, ask := Resolve(det, ModeHint{}, true)
	if !ask || intent != IntentAgent {
		t.Fatalf("Resolve(ambiguous, unsticky, interactive) = %v, ask=%v; want agent, true", intent, ask)
	}
}

func TestResolveAmbiguousHeadlessLowScoreFallsBackToAgent(t *testing.T) {
	det := Detection{Top: IntentPlan, TopScore: 0.45, RunnerUp: 0.3}
	intent, ask := Resolve(det, ModeHint{}, false)
	if intent != IntentAgent || ask {
		t.Fatalf("Resolve(0.45 headless) = %v, ask=%v; want agent, false", intent, ask)
	}
}

func TestResolveAmbiguousHeadlessKeepsTopIfOverThreshold(t *testing.T) {
	det := Detection{Top: IntentPlan, TopScore: 0.65, RunnerUp: 0.55}
	intent, ask := Resolve(det, ModeHint{}, false)
	if intent != IntentPlan || ask {
		t.Fatalf("Resolve(0.65 headless) = %v, ask=%v; want plan, false", intent, ask)
	}
}

func TestResolveStickyAgree(t *testing.T) {
	det := Detection{Top: IntentPlan, TopScore: 0.85, RunnerUp: 0.1}
	intent, ask := Resolve(det, ModeHint{Mode: modes.ModePlan, Sticky: true}, true)
	if intent != IntentPlan || ask {
		t.Fatalf("Resolve(confident plan, sticky plan) = %v, ask=%v; want plan, false", intent, ask)
	}
}

func TestResolveStickyDisagreeConfidentAlwaysAsks(t *testing.T) {
	det := Detection{Top: IntentGoal, TopScore: 0.85, RunnerUp: 0.1}
	intent, ask := Resolve(det, ModeHint{Mode: modes.ModePlan, Sticky: true}, true)
	if !ask || intent != IntentPlan {
		t.Fatalf("Resolve(confident goal, sticky plan, interactive) = %v, ask=%v; want plan, true", intent, ask)
	}
}

func TestResolveStickyDisagreeHeadlessKeepsSticky(t *testing.T) {
	det := Detection{Top: IntentGoal, TopScore: 0.85, RunnerUp: 0.1}
	intent, ask := Resolve(det, ModeHint{Mode: modes.ModePlan, Sticky: true}, false)
	if intent != IntentPlan || ask {
		t.Fatalf("Resolve(confident goal, sticky plan, headless) = %v, ask=%v; want plan, false", intent, ask)
	}
}

func TestResolveStickyNotConfidentKeepsSticky(t *testing.T) {
	det := Detection{Top: IntentGoal, TopScore: 0.65, RunnerUp: 0.55}
	intent, ask := Resolve(det, ModeHint{Mode: modes.ModePlan, Sticky: true}, true)
	if intent != IntentPlan || ask {
		t.Fatalf("Resolve(ambiguous goal, sticky plan) = %v, ask=%v; want plan, false", intent, ask)
	}
}

func TestModeChoiceQuestionnaireValidAndCapped(t *testing.T) {
	det := Detection{
		Source: "jev",
		Scores: map[Intent]float64{
			IntentPlan: 0.6, IntentGoal: 0.3, IntentAgent: 0.1,
		},
		Top:      IntentPlan,
		TopScore: 0.6,
	}
	q, opts := ModeChoiceQuestionnaire(det, ModeHint{Mode: modes.ModeAgent})
	if err := q.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	if len(q.Groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(q.Groups))
	}
	if len(q.Groups[0].Options) > 4 {
		t.Fatalf("options = %d, want <= 4", len(q.Groups[0].Options))
	}
	if len(opts) != len(q.Groups[0].Options) {
		t.Fatalf("option/intent mapping length mismatch: %d vs %d", len(opts), len(q.Groups[0].Options))
	}
}

func TestModeChoiceQuestionnaireIncludesStickyHint(t *testing.T) {
	det := Detection{
		Source:   "jev",
		Scores:   map[Intent]float64{IntentPlan: 0.9, IntentGoal: 0.05, IntentAgent: 0.05},
		Top:      IntentPlan,
		TopScore: 0.9,
	}
	q, opts := ModeChoiceQuestionnaire(det, ModeHint{Mode: modes.ModeAgent, Sticky: true})
	found := false
	for i, o := range q.Groups[0].Options {
		if opts[i] == IntentAgent {
			found = true
			if !strings.Contains(o.Label, "currently selected") {
				t.Errorf("agent option label = %q, want 'currently selected'", o.Label)
			}
		}
	}
	if !found {
		t.Fatalf("sticky agent intent missing from options: %+v", q.Groups[0].Options)
	}
}

func TestModeChoiceQuestionnaireLLMFallbackShowsSuggested(t *testing.T) {
	det := Detection{Source: "llm", Top: IntentDebug, TopScore: 1.0}
	q, _ := ModeChoiceQuestionnaire(det, ModeHint{Mode: modes.ModeAgent})
	for _, o := range q.Groups[0].Options {
		if !strings.Contains(o.Label, "suggested") && !strings.Contains(o.Label, "Recommended") {
			t.Errorf("LLM option label = %q, want suggested or recommended", o.Label)
		}
	}
}

func TestModeChoiceAnswer(t *testing.T) {
	_, opts := ModeChoiceQuestionnaire(Detection{Source: "jev", Scores: map[Intent]float64{IntentPlan: 0.9, IntentAgent: 0.1}, Top: IntentPlan, TopScore: 0.9}, ModeHint{Mode: modes.ModeAgent})
	answers := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}
	intent, chosen := ModeChoiceAnswer(answers, opts, ModeHint{Mode: modes.ModeAgent})
	if !chosen || intent != IntentPlan {
		t.Fatalf("ModeChoiceAnswer = %v, chosen=%v; want plan, true", intent, chosen)
	}
}

func TestModeChoiceAnswerSkipKeepsHint(t *testing.T) {
	_, opts := ModeChoiceQuestionnaire(Detection{Source: "jev", Scores: map[Intent]float64{IntentPlan: 0.9}}, ModeHint{Mode: modes.ModeGoal, Sticky: true})
	answers := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Skipped: true}}}
	intent, chosen := ModeChoiceAnswer(answers, opts, ModeHint{Mode: modes.ModeGoal, Sticky: true})
	if chosen || intent != IntentGoal {
		t.Fatalf("ModeChoiceAnswer(skip) = %v, chosen=%v; want goal, false", intent, chosen)
	}
}
