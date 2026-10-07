package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
)

// commandApp builds an App whose project has one custom command (triage) and
// whose global layer has one named like a built-in (clear) and one global (release).
func commandApp(t *testing.T) *App {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	workdir := t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + name + "\ndescription: Runs " + name + "\nargument-hint: <id>\n---\n\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(config.ProjectCommandsDir(workdir), "triage", "Triage $1 now. Notes: $ARGUMENTS")
	write(filepath.Join(home, "commands"), "release", "Cut the release.")
	write(filepath.Join(home, "commands"), "clear", "SHADOW")
	a := New(Options{Workdir: workdir})
	t.Cleanup(func() {
		if a.procManager != nil {
			a.procManager.Shutdown()
		}
	})
	return a
}

func TestSlashPopupOffersCustomCommandsAndBuiltinsWin(t *testing.T) {
	a := commandApp(t)
	typeSlash(a, "/")
	for _, want := range []string{"/triage", "/release", "/clear"} {
		if !slices.Contains(a.autocomplete, want) {
			t.Fatalf("popup for / = %v, missing %s", a.autocomplete, want)
		}
	}
	// /clear is the built-in's one entry, not offered twice.
	n := 0
	for _, l := range a.autocomplete {
		if l == "/clear" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("/clear offered %d times", n)
	}
}

func TestCustomCommandRunsAsAModelTurn(t *testing.T) {
	a := commandApp(t)
	a.mode = "agent"
	a.modeAuto = true // no agent picker in the way
	before := len(a.messages)
	a.handleCommand("/triage CVE-1 urgent")
	var system, user string
	for _, m := range a.messages[before:] {
		switch m.Role {
		case "system":
			system += m.Text() + "\n"
		case "user":
			user = m.Text()
		}
	}
	if !strings.Contains(system, "command: /triage CVE-1 urgent") {
		t.Errorf("the invoked line was not echoed: %q", system)
	}
	if user != "Triage CVE-1 now. Notes: CVE-1 urgent" {
		t.Errorf("the turn's prompt = %q", user)
	}
}

func TestCustomCommandNeverShadowsABuiltin(t *testing.T) {
	a := commandApp(t)
	a.addSystem("keep")
	a.handleCommand("/clear")
	for _, m := range a.messages {
		if strings.Contains(m.Text(), "SHADOW") {
			t.Fatal("a custom /clear replaced the built-in")
		}
	}
}

func TestCustomCommandWaitsForTheRunningTurn(t *testing.T) {
	a := commandApp(t)
	a.mode = "agent"
	a.modeAuto = true
	a.cancel = func() {}
	before := len(a.messages)
	if cmd := a.handleCommand("/triage x"); cmd != nil {
		t.Error("a command started while a turn runs")
	}
	last := a.messages[len(a.messages)-1]
	if len(a.messages) != before+1 || !strings.Contains(last.Text(), "a turn is running") {
		t.Errorf("messages = %+v", a.messages[before:])
	}
}

func TestCustomCommandInAgentModeWithoutAnAgentOpensThePicker(t *testing.T) {
	a := commandApp(t)
	a.mode = "agent"
	a.modeAuto = false
	a.namedAgent = ""
	a.handleCommand("/release")
	if !a.agentPickerOpen || !a.agentPickerSubmit || a.editor.Value() != "Cut the release." {
		t.Errorf("picker open=%v submit=%v editor=%q", a.agentPickerOpen, a.agentPickerSubmit, a.editor.Value())
	}
}

func TestUnknownCommandIsStillUnknown(t *testing.T) {
	a := commandApp(t)
	before := len(a.messages)
	a.handleCommand("/nosuchthing")
	if len(a.messages) != before+1 || !strings.Contains(a.messages[len(a.messages)-1].Text(), "unknown command") {
		t.Errorf("messages = %+v", a.messages[before:])
	}
}

func TestCommandsListing(t *testing.T) {
	a := commandApp(t)
	a.handleCommand("/commands")
	out := a.messages[len(a.messages)-1].Text()
	for _, want := range []string{"/triage <id> [project]", "/release <id> [global]", "shadowed by the built-in /clear"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
}
