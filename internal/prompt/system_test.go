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
