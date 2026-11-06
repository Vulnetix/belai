package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
)

func TestLintLoopCommand(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_LINT_LOOP", "")
	a := New(Options{Workdir: t.TempDir()})
	last := func() string { return a.messages[len(a.messages)-1].Text() }

	a.lintLoopCmd("")
	if !strings.Contains(last(), "lint loop: off; timeout 10000 ms; up to 10 row(s) per file; 3 pass(es)") {
		t.Fatalf("status = %q", last())
	}

	a.agent = &agent.Session{}
	a.lintLoopCmd("ON")
	if !a.settings.LintLoopEnabled() || a.agent != nil || !strings.Contains(last(), "on for this session") {
		t.Fatalf("on: enabled %v, session kept %v, %q", a.settings.LintLoopEnabled(), a.agent != nil, last())
	}
	a.lintLoopCmd("status")
	if !strings.Contains(last(), "lint loop: on") || !strings.Contains(last(), "set by /lint-loop for this session") {
		t.Fatalf("status after on = %q", last())
	}

	a.lintLoopCmd("maybe")
	if !a.settings.LintLoopEnabled() || !strings.Contains(last(), "use /lint-loop") {
		t.Fatalf("a bad word changed the setting: %q", last())
	}

	// The choice survives a settings reload and is never written to a file.
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	if !a.settings.LintLoopEnabled() {
		t.Fatal("a reload dropped the session choice")
	}
	if g, _ := config.LoadGlobal(); g.LintLoop != nil {
		t.Fatalf("the session choice was written to the global settings: %+v", g.LintLoop)
	}

	a.lintLoopCmd("off")
	if a.settings.LintLoopEnabled() {
		t.Fatal("off did not switch it back")
	}
}

// A repository's settings file cannot switch the loop on.
func TestLintLoopProjectFileCannotSwitchItOn(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_LINT_LOOP", "")
	workdir := t.TempDir()
	yes := true
	if err := config.SaveProject(workdir, config.Settings{LintLoop: &config.LintLoopSettings{Enabled: &yes}}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	if a.settings.LintLoopEnabled() {
		t.Fatal("a project file switched the lint loop on")
	}
}
