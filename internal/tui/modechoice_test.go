package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/rolemanager"
)

func TestModeChoicePanelStartsOnRecommended(t *testing.T) {
	q := clarify.Questionnaire{Groups: []clarify.Group{{
		Context: "Which mode should handle this prompt?",
		Options: []clarify.Option{
			{Label: "Plan handoff · 86% (Recommended)"},
			{Label: "Agent · 14%"},
		},
	}}}
	state := newClarifyState(q, make(chan clarify.Answers), true)
	if state.modeChoice != true {
		t.Fatal("expected modeChoice true")
	}
	row := state.currentRow()
	if row == nil || row.optionIdx != 0 {
		t.Fatalf("expected cursor on recommended option 0, got %+v", row)
	}
}

func TestModeChoiceHeaderRendersMode(t *testing.T) {
	a := New(Options{})
	a.width = 120
	a.height = 40
	reply := make(chan clarify.Answers)
	q := clarify.Questionnaire{Groups: []clarify.Group{{
		Context: "Which mode should handle this prompt?",
		Options: []clarify.Option{
			{Label: "Plan handoff · 86% (Recommended)"},
			{Label: "Agent · 14%"},
		},
	}}}
	a.clarifyState = newClarifyState(q, reply, true)
	a.view = viewClarify

	panel := a.clarifyPanel()
	if !strings.Contains(panel, "Mode") {
		t.Fatalf("panel header should read 'Mode', got %q", panel)
	}
}

func TestApplyLiveModeDecisionUpdatesStickyModeWhenUserChosen(t *testing.T) {
	a := New(Options{})
	a.mode = "plan"
	a.modeSticky = true

	d := rolemanager.ModeDecision{
		Mode:       modes.ModeAgent,
		Intent:     rolemanager.IntentDebug,
		AgentName:  "belai:debug",
		UserChosen: true,
	}
	a.applyLiveModeDecision(d)
	if a.mode != "agent" {
		t.Fatalf("expected sticky mode updated to agent, got %q", a.mode)
	}
	if a.footerAgentOverride != "debug" {
		t.Fatalf("expected footer override 'debug', got %q", a.footerAgentOverride)
	}
}

func TestApplyLiveModeDecisionRespectsStickyWithoutUserChosen(t *testing.T) {
	a := New(Options{})
	a.mode = "plan"
	a.modeSticky = true

	d := rolemanager.ModeDecision{
		Mode:   modes.ModeAgent,
		Intent: rolemanager.IntentDebug,
	}
	a.applyLiveModeDecision(d)
	if a.mode != "plan" {
		t.Fatalf("expected sticky mode preserved, got %q", a.mode)
	}
}
