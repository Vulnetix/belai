package prompt

import (
	"strings"
	"testing"
)

func TestSystemCarriers(t *testing.T) {
	cases := []struct {
		name     string
		opts     Options
		wantTag  string
		wantText string
	}{
		{
			name:     "plan carrier",
			opts:     Options{Carrier: CarrierPlan, PlanText: "1. do the thing\n2. ship it"},
			wantTag:  "Active plan:",
			wantText: "1. do the thing",
		},
		{
			name:     "goal carrier",
			opts:     Options{Carrier: CarrierGoal, GoalText: "build signet"},
			wantTag:  "Active goal:",
			wantText: "build signet",
		},
		{
			name:     "profile carrier",
			opts:     Options{Carrier: CarrierProfile, ProfileText: "expert Go security reviewer"},
			wantTag:  "Active profile:",
			wantText: "expert Go security reviewer",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := System(tc.opts)
			if err != nil {
				t.Fatalf("System: %v", err)
			}
			if !strings.Contains(got, tc.wantTag) {
				t.Fatalf("prompt missing %q: %q", tc.wantTag, got)
			}
			if !strings.Contains(got, tc.wantText) {
				t.Fatalf("prompt missing carrier text %q: %q", tc.wantText, got)
			}
			if !strings.Contains(got, "Voice guidance: respond clearly and professionally.") {
				t.Fatalf("normal voice guidance missing: %q", got)
			}
		})
	}
}

func TestSystemCaveman(t *testing.T) {
	on, err := System(Options{Caveman: true})
	if err != nil {
		t.Fatalf("System(on): %v", err)
	}
	if !strings.Contains(on, "talk like caveman") {
		t.Fatalf("caveman voice missing: %q", on)
	}
	if strings.Contains(on, "respond clearly and professionally") {
		t.Fatalf("normal voice should not appear when caveman on: %q", on)
	}

	off, err := System(Options{Caveman: false})
	if err != nil {
		t.Fatalf("System(off): %v", err)
	}
	if !strings.Contains(off, "respond clearly and professionally") {
		t.Fatalf("normal voice missing when caveman off: %q", off)
	}
}

func TestSystemExactlyOneCarrier(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"two carriers", Options{Carrier: CarrierPlan, PlanText: "p", GoalText: "g"}},
		{"carrier set but no text", Options{Carrier: CarrierPlan}},
		{"none carrier with text", Options{PlanText: "p"}},
		{"wrong carrier text", Options{Carrier: CarrierPlan, GoalText: "g"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := System(tc.opts); err == nil {
				t.Fatalf("expected error for %+v", tc.opts)
			}
		})
	}
}

func TestSystemNone(t *testing.T) {
	got, err := System(Options{})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if strings.Contains(got, "Active ") {
		t.Fatalf("no carrier should be rendered: %q", got)
	}
}

func TestSystemNamesThreeIdentities(t *testing.T) {
	got, err := System(Options{Provider: "anthropic", Model: "claude-sonnet-4-5"})
	if err != nil {
		t.Fatalf("System: %v", err)
	}

	for _, want := range []string{
		"running inside Signet",
		"- Harness: Signet.",
		"- Provider: anthropic.",
		"- Model: claude-sonnet-4-5,",
		"Keep your own identity",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, got)
		}
	}
}

// The harness must never tell the model it *is* Signet. Doing so overrode the
// model's own trained identity, and it answered questions about itself by
// disclaiming any knowledge of which model it was.
func TestSystemDoesNotClaimModelIsHarness(t *testing.T) {
	cases := []Options{
		{},
		{Provider: "openai", Model: "gpt-5"},
		{Carrier: CarrierPlan, PlanText: "ship it", Provider: "openai", Model: "gpt-5"},
	}
	for _, opts := range cases {
		got, err := System(opts)
		if err != nil {
			t.Fatalf("System(%+v): %v", opts, err)
		}
		if strings.Contains(got, "You are Signet") {
			t.Fatalf("system prompt claims the model is the harness:\n%s", got)
		}
	}
}

// An unknown provider or model is omitted rather than guessed at.
func TestSystemOmitsUnknownProviderAndModel(t *testing.T) {
	got, err := System(Options{})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if !strings.Contains(got, "- Provider: the API serving this session.") {
		t.Fatalf("expected a provider line without a name:\n%s", got)
	}
	if !strings.Contains(got, "- Model: you.") {
		t.Fatalf("expected a model line without a name:\n%s", got)
	}
}

// TestSystemExplorePreamble pins the plan-mode explore guidance: it only
// appears when Explore is set, and never appears in a normal prompt.
func TestSystemWorkDiscipline(t *testing.T) {
	on, err := System(Options{WorkDiscipline: true})
	if err != nil {
		t.Fatalf("System(WorkDiscipline): %v", err)
	}
	if !strings.Contains(on, "Work discipline.") {
		t.Fatalf("work discipline missing:\n%s", on)
	}
	for _, want := range []string{"Batch independent calls", "Read the code you will change", "smallest change", "run the detected test"} {
		if !strings.Contains(on, want) {
			t.Fatalf("work discipline missing %q:\n%s", want, on)
		}
	}
	// The old speed-over-understanding metrics pushed edits ahead of reading.
	for _, gone := range []string{"Time to first file mutation", "wasted turn", "one short paragraph", "Work on disk beats"} {
		if strings.Contains(on, gone) {
			t.Fatalf("work discipline still carries %q:\n%s", gone, on)
		}
	}
	// It must land before the voice line so normal/caveman voice assertions
	// remain the final word.
	workIdx := strings.Index(on, "Work discipline.")
	voiceIdx := strings.Index(on, "Voice guidance:")
	if workIdx == -1 || voiceIdx == -1 || workIdx >= voiceIdx {
		t.Fatalf("work discipline must appear before voice guidance:\n%s", on)
	}

	off, err := System(Options{WorkDiscipline: false})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if strings.Contains(off, "Work discipline.") {
		t.Fatalf("work discipline must not render when disabled:\n%s", off)
	}

	// A plan-mode explore subagent already receives the explore preamble and
	// must not also be told to edit.
	explore, err := System(Options{WorkDiscipline: true, Explore: true})
	if err != nil {
		t.Fatalf("System(WorkDiscipline, Explore): %v", err)
	}
	if strings.Contains(explore, "Work discipline.") {
		t.Fatalf("work discipline must not render during explore subagent runs:\n%s", explore)
	}
}

func TestSystemExplorePreamble(t *testing.T) {
	on, err := System(Options{Explore: true})
	if err != nil {
		t.Fatalf("System(Explore): %v", err)
	}
	if !strings.Contains(on, "plan-mode exploration") {
		t.Fatalf("explore preamble missing:\n%s", on)
	}
	if !strings.Contains(on, "the read-only tools listed below") {
		t.Fatalf("empty tool list should fall back to the generic phrase:\n%s", on)
	}
	on, err = System(Options{Explore: true, ExploreTools: []string{"Grep", "Git", "JQ"}})
	if err != nil {
		t.Fatalf("System(Explore, tools): %v", err)
	}
	if !strings.Contains(on, "Grep, Git, JQ") {
		t.Fatalf("preamble should name exactly the detected tools:\n%s", on)
	}
	if strings.Contains(on, "YQ") {
		t.Fatalf("preamble must not promise a tool that is not in the list:\n%s", on)
	}

	off, err := System(Options{})
	if err != nil {
		t.Fatalf("System: %v", err)
	}
	if strings.Contains(off, "plan-mode exploration") {
		t.Fatalf("explore preamble must not render for a normal prompt:\n%s", off)
	}
}
