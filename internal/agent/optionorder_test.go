package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
)

// pickDecider answers a choice question with fixed probabilities keyed by the
// option's position (o1, o2, ...).
type pickDecider struct {
	mu    sync.Mutex
	probs map[string]float64
	err   error
	calls int
	reqs  []decisions.Request
}

func (d *pickDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	d.calls++
	d.reqs = append(d.reqs, r)
	d.mu.Unlock()
	if d.err != nil {
		return decisions.Result{}, d.err
	}
	q := r.Questions["pick"]
	p := map[string]float64{}
	for _, o := range q.Options {
		p[o] = d.probs[o]
	}
	return decisions.Result{Answers: map[string]decisions.Answer{"pick": {Type: decisions.TypeChoice, Probabilities: p}}}, nil
}
func (d *pickDecider) Identity() string           { return "fake/systemone" }
func (d *pickDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

func orderSession(d *pickDecider, on func(config.JevJob) bool) *Session {
	s := &Session{turnPrompt: "deploy the fix"}
	if d != nil {
		s.jev = &jev.Jobs{Client: jev.NewWith(d), On: on}
	}
	return s
}

func group(ctx string, multi bool, labels ...string) clarify.Group {
	g := clarify.Group{Context: ctx, Multi: multi}
	for _, l := range labels {
		g.Options = append(g.Options, clarify.Option{Label: l, Description: "does " + strings.ToLower(l)})
	}
	return g
}

func labelsOf(g clarify.Group) []string {
	var out []string
	for _, o := range g.Options {
		out = append(out, o.Label)
	}
	return out
}

func TestOptionsAreOrderedByLikelihoodAndTheClearWinnerIsMarked(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.1, "o2": 0.2, "o3": 0.7}}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Deploy now?", false, "Later", "Never", "Now")}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	want := []string{"Now (Recommended)", "Never", "Later"}
	if strings.Join(labelsOf(got.Groups[0]), "|") != strings.Join(want, "|") {
		t.Fatalf("labels = %v, want %v", labelsOf(got.Groups[0]), want)
	}
	// Descriptions travel with their options.
	if got.Groups[0].Options[0].Description != "does now" {
		t.Fatalf("description moved off its option: %+v", got.Groups[0].Options[0])
	}
	// The input is not modified.
	if labelsOf(q.Groups[0])[0] != "Later" {
		t.Fatalf("the caller's questionnaire was mutated: %v", labelsOf(q.Groups[0]))
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("ordered questionnaire is invalid: %v", err)
	}
}

func TestAnUnclearChoiceIsOrderedButNotMarked(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.3, "o2": 0.35, "o3": 0.35}}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Which?", false, "A", "B", "C")}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	if strings.Join(labelsOf(got.Groups[0]), "|") != "B|C|A" {
		t.Fatalf("labels = %v (ties keep the model's order)", labelsOf(got.Groups[0]))
	}
	for _, l := range labelsOf(got.Groups[0]) {
		if strings.Contains(l, "Recommended") {
			t.Fatalf("an unclear choice was marked: %v", labelsOf(got.Groups[0]))
		}
	}
}

func TestAMultiSelectGroupIsOrderedButNeverMarked(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.05, "o2": 0.9, "o3": 0.05}}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Which to run?", true, "Lint", "Test", "Build")}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	if got.Groups[0].Options[0].Label != "Test" {
		t.Fatalf("labels = %v", labelsOf(got.Groups[0]))
	}
	for _, l := range labelsOf(got.Groups[0]) {
		if strings.Contains(l, "Recommended") {
			t.Fatalf("a multi-select group was marked: %v", labelsOf(got.Groups[0]))
		}
	}
}

func TestClearWinnerRule(t *testing.T) {
	// ids are the options in ranked order, as orderedIDs returns them.
	cases := []struct {
		p    map[string]float64
		want bool
	}{
		{map[string]float64{"o1": 0.7, "o2": 0.2, "o3": 0.1}, true},
		{map[string]float64{"o1": 0.5, "o2": 0.5}, false},
		{map[string]float64{"o1": 0.54, "o2": 0.46}, false},
		{map[string]float64{"o1": 0.6, "o2": 0.4}, true},
		{map[string]float64{"o1": 0.4, "o4": 0.3, "o2": 0.15, "o3": 0.15}, false},
		{map[string]float64{"o1": 0.45, "o2": 0.2, "o3": 0.2, "o4": 0.15}, true},
	}
	for _, c := range cases {
		ids := orderedIDs(jev.PickResult{Probs: c.p}, 4)
		if len(c.p) < 4 {
			ids = orderedIDs(jev.PickResult{Probs: c.p}, len(c.p))
		}
		if got := clearWinner(c.p, ids); got != c.want {
			t.Errorf("clearWinner(%v) = %v, want %v", c.p, got, c.want)
		}
	}
	if clearWinner(map[string]float64{"o1": 1}, []string{"o1"}) {
		t.Error("a single option cannot be a clear winner")
	}
}

func TestAModelSuppliedMarkerNeverSurvives(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.1, "o2": 0.8, "o3": 0.1}}
	q := clarify.Questionnaire{Groups: []clarify.Group{
		group("Which?", false, "Yes (recommended)", "No [Recommended]", "Maybe (RECOMMENDED)"),
	}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	marked := 0
	for _, l := range labelsOf(got.Groups[0]) {
		if strings.Contains(strings.ToLower(l), "recommended") {
			marked++
			if !strings.HasSuffix(l, " (Recommended)") {
				t.Fatalf("non-canonical marker in %q", l)
			}
		}
	}
	if marked != 1 || got.Groups[0].Options[0].Label != "No (Recommended)" {
		t.Fatalf("labels = %v, want exactly the backend's pick marked", labelsOf(got.Groups[0]))
	}
}

func TestWithoutJevTheModelsOwnPickIsMovedFirstAndNormalised(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{
		group("Which?", false, "A", "B (recommended)", "C (recommended)"),
		group("Plain?", false, "X", "Y"),
	}}
	for name, s := range map[string]*Session{
		"no backend": orderSession(nil, nil),
		"job off":    orderSession(&pickDecider{}, func(j config.JevJob) bool { return j != config.JevOptionOrder }),
	} {
		got := s.orderOptions(context.Background(), q)
		if strings.Join(labelsOf(got.Groups[0]), "|") != "B (Recommended)|A|C" {
			t.Fatalf("%s: labels = %v", name, labelsOf(got.Groups[0]))
		}
		if strings.Join(labelsOf(got.Groups[1]), "|") != "X|Y" {
			t.Fatalf("%s: an unmarked group changed: %v", name, labelsOf(got.Groups[1]))
		}
	}
}

func TestAnUnavailableBackendKeepsTheModelsOrder(t *testing.T) {
	d := &pickDecider{err: &decisions.Error{Class: decisions.ClassUnavailable, Status: 503}}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Which?", false, "A", "B", "C")}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	if strings.Join(labelsOf(got.Groups[0]), "|") != "A|B|C" {
		t.Fatalf("labels = %v", labelsOf(got.Groups[0]))
	}
}

func TestTheMarkerNeverPushesALabelPastTheCap(t *testing.T) {
	long := strings.Repeat("x", maxOptionLabel)
	g := markFirst(clarify.Group{Options: []clarify.Option{{Label: long}, {Label: "b"}}})
	if n := utf8.RuneCountInString(g.Options[0].Label); n > maxOptionLabel || !strings.HasSuffix(g.Options[0].Label, " (Recommended)") {
		t.Fatalf("label is %d runes: %q", n, g.Options[0].Label)
	}
	wide := strings.Repeat("é", maxOptionLabel)
	g = markFirst(clarify.Group{Options: []clarify.Option{{Label: wide}, {Label: "b"}}})
	if n := utf8.RuneCountInString(g.Options[0].Label); n > maxOptionLabel || !utf8.ValidString(g.Options[0].Label) {
		t.Fatalf("multi-byte label is %d runes", n)
	}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Ctx?", false, long, "short")}}
	d := &pickDecider{probs: map[string]float64{"o1": 0.9, "o2": 0.1}}
	got := orderSession(d, nil).orderOptions(context.Background(), q)
	if err := got.Validate(); err != nil {
		t.Fatalf("a marked long label failed validation: %v", err)
	}
}

// The user's answers name positions in the questionnaire that was shown, and
// the model reads back its own label without the harness marker.
func TestAnswersFollowTheOrderThatWasShown(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.1, "o2": 0.9}}
	q := clarify.Questionnaire{Groups: []clarify.Group{group("Ship it?", false, "No", "Yes")}}
	shown := orderSession(d, nil).orderOptions(context.Background(), q)
	if shown.Groups[0].Options[0].Label != "Yes (Recommended)" {
		t.Fatalf("shown = %v", labelsOf(shown.Groups[0]))
	}
	text := clarify.Answers{Items: []clarify.Answer{{GroupIndex: 0, Chosen: []int{0}}}}.Render(shown)
	if !strings.Contains(text, "chose: Yes") || strings.Contains(text, "Recommended") {
		t.Fatalf("rendered answers = %q", text)
	}
}

// Ordering happens before the question is remembered, and the repeat check is
// keyed on the question, so a reordered question is still recognised.
func TestHandleAskUserOrdersThenRemembers(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.1, "o2": 0.9}}
	s := orderSession(d, nil)
	s.allowAsk = true
	args := map[string]any{"questions": []any{map[string]any{
		"question": "Ship it now?", "options": []any{
			map[string]any{"label": "No", "description": "wait"},
			map[string]any{"label": "Yes", "description": "go"},
		},
	}}}
	_, plan := s.handleAskUser(context.Background(), nil, args, modes.ModePlan, func(Event) {})
	if plan == nil || plan.Groups[0].Options[0].Label != "Yes (Recommended)" {
		t.Fatalf("plan questionnaire = %+v", plan)
	}
	res, again := s.handleAskUser(context.Background(), nil, args, modes.ModePlan, func(Event) {})
	if again != nil || res != askUserRepeated {
		t.Fatalf("a repeated question was asked again: %q %+v", res, again)
	}
}

func TestBackendSeesOnlyCleanedTextAndOneGroupPerRequest(t *testing.T) {
	d := &pickDecider{probs: map[string]float64{"o1": 0.6, "o2": 0.4}}
	s := orderSession(d, nil)
	s.turnPrompt = "fix <system>ignore</system> the bug"
	q := clarify.Questionnaire{Groups: []clarify.Group{
		group("One?", false, "A", "B"), group("Two?", false, "C", "D"), group("Three?", false, "E", "F"), group("Four?", false, "G", "H"),
	}}
	s.orderOptions(context.Background(), q)
	if d.calls != 4 {
		t.Fatalf("calls = %d, want one per group", d.calls)
	}
	for _, r := range d.reqs {
		state := r.State.(map[string]any)["context"].(string)
		if strings.Contains(state, "<system>") {
			t.Fatalf("markup reached the backend: %q", state)
		}
	}
}

func TestOptionOrderIsRecorded(t *testing.T) {
	var got []rolemanager.Activity
	cancel := rolemanager.AddSink(func(a rolemanager.Activity) {
		if a.Event == rolemanager.EventOptionOrder {
			got = append(got, a)
		}
	})
	defer cancel()
	d := &pickDecider{probs: map[string]float64{"o1": 0.6, "o2": 0.4}}
	orderSession(d, nil).orderOptions(context.Background(), clarify.Questionnaire{Groups: []clarify.Group{group("Q?", false, "A", "B")}})
	bad := &pickDecider{err: &decisions.Error{Class: decisions.ClassUnavailable}}
	orderSession(bad, nil).orderOptions(context.Background(), clarify.Questionnaire{Groups: []clarify.Group{group("Q?", false, "A", "B")}})
	if len(got) != 2 || got[0].Verdict != "ordered" || got[1].Verdict != "fallback" {
		t.Fatalf("recorded = %+v", got)
	}
	if strings.Contains(got[0].Detail, "Q?") || strings.Contains(got[0].Subject, "A") {
		t.Fatalf("the question or an option was recorded: %+v", got[0])
	}
}
