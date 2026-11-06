package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
)

func TestFinishReviewCommand(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_FINISH_REVIEW", "")
	a := New(Options{Workdir: t.TempDir()})
	last := func() string { return a.messages[len(a.messages)-1].Text() }

	a.finishReviewCmd("")
	if !strings.Contains(last(), "finish review: on; up to 2 round(s)") {
		t.Fatalf("status = %q", last())
	}

	a.agent = &agent.Session{}
	a.finishReviewCmd("OFF")
	if a.settings.FinishReviewEnabled() || a.agent != nil || !strings.Contains(last(), "off for this session") {
		t.Fatalf("off: enabled %v, session kept %v, %q", a.settings.FinishReviewEnabled(), a.agent != nil, last())
	}
	a.finishReviewCmd("status")
	if !strings.Contains(last(), "finish review: off") || !strings.Contains(last(), "set by /finish-review for this session") {
		t.Fatalf("status after off = %q", last())
	}

	a.finishReviewCmd("maybe")
	if a.settings.FinishReviewEnabled() || !strings.Contains(last(), "use /finish-review") {
		t.Fatalf("a bad word changed the setting: %q", last())
	}

	// The choice survives a settings reload and is not written to a file.
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	if a.settings.FinishReviewEnabled() {
		t.Fatal("a reload dropped the session choice")
	}
	if g, _ := config.LoadGlobal(); g.FinishReview != nil {
		t.Fatalf("the session choice was written to the global settings: %+v", g.FinishReview)
	}

	a.finishReviewCmd("on")
	if !a.settings.FinishReviewEnabled() {
		t.Fatal("on did not switch it back")
	}
}

// A repository's settings file cannot switch the gate off.
func TestFinishReviewProjectFileCannotSwitchItOff(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_FINISH_REVIEW", "")
	workdir := t.TempDir()
	off := false
	if err := config.SaveProject(workdir, config.Settings{FinishReview: &config.FinishReviewSettings{Enabled: &off}}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Workdir: workdir})
	if !a.settings.FinishReviewEnabled() {
		t.Fatal("a project file switched the gate off")
	}
}
