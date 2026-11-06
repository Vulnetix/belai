package main

import (
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

func TestApplyLintLoopFlag(t *testing.T) {
	var s config.Settings
	applyLintLoopFlag(&s, false)
	if s.LintLoopEnabled() || s.LintLoop != nil {
		t.Fatal("no flag must change nothing")
	}
	applyLintLoopFlag(&s, true)
	if !s.LintLoopEnabled() {
		t.Fatal("-lint-loop did not enable the loop")
	}

	// It keeps the user's numbers and never switches the loop off.
	rows := 4
	s = config.Settings{LintLoop: &config.LintLoopSettings{MaxRows: &rows}}
	applyLintLoopFlag(&s, true)
	if !s.LintLoopEnabled() || s.LintLoopMaxRows() != 4 {
		t.Fatalf("settings = %+v", s.LintLoop)
	}
	applyLintLoopFlag(&s, false)
	if !s.LintLoopEnabled() {
		t.Fatal("an absent flag switched the loop off")
	}
}
