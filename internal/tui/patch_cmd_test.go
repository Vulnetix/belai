package tui

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/config"
)

func TestPatchToleranceCommand(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	a := New(Options{Workdir: t.TempDir()})
	last := func() string { return a.messages[len(a.messages)-1].Text() }

	a.patchToleranceCmd("")
	if !strings.Contains(last(), "patch tolerance: whitespace") {
		t.Fatalf("show = %q", last())
	}

	a.agent = &agent.Session{}
	a.patchToleranceCmd("UNICODE")
	if a.settings.PatchToleranceValue() != config.PatchUnicode || a.agent != nil {
		t.Fatalf("level %q, session kept %v", a.settings.PatchToleranceValue(), a.agent != nil)
	}
	if !strings.Contains(last(), "unicode for this session") {
		t.Fatalf("set = %q", last())
	}

	a.patchToleranceCmd("sloppy")
	if a.settings.PatchToleranceValue() != config.PatchUnicode || !strings.Contains(last(), "not a level") {
		t.Fatalf("a bad word changed the level: %q %q", a.settings.PatchToleranceValue(), last())
	}

	// The choice survives a settings reload and is not written to a file.
	if err := a.reloadSettings(); err != nil {
		t.Fatal(err)
	}
	if a.settings.PatchToleranceValue() != config.PatchUnicode {
		t.Fatal("a reload dropped the session choice")
	}
	g, err := config.LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if g.PatchTolerance != "" {
		t.Fatalf("the command wrote %q to the settings file", g.PatchTolerance)
	}
}

func TestPatchToleranceIsRegistered(t *testing.T) {
	c, ok := NewRegistry(t.TempDir()).Command("patch-tolerance")
	if !ok || c.Args == nil || len(c.Args()) != 3 {
		t.Fatalf("command = %+v ok=%v", c, ok)
	}
}
