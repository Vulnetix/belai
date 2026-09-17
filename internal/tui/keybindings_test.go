package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
)

// altLiteral matches a key-name string literal that carries an alt modifier,
// in any position: "alt+s", "ctrl+alt+c", "alt+enter".
var altLiteral = regexp.MustCompile(`"[a-z0-9+]*alt\+`)

// Signet binds no alt chord anywhere, and this is the guard that keeps it that
// way. Two independent reasons, both documented in docs/architecture.md:
//
//  1. keys.Translate folds a ctrl-modified letter onto the legacy
//     tea.KeyCtrlA…KeyCtrlZ constants, which carry no alt bit. Under the kitty
//     keyboard protocol Signet pushes by default, ctrl+alt+x is therefore
//     indistinguishable from ctrl+x by the time Update sees it, so a
//     "ctrl+alt+…" case can never match — it is dead code that silently does
//     nothing.
//  2. Without the kitty protocol alt is an ESC prefix, which is ambiguous
//     against a real esc and is swallowed by several terminals and by tmux.
func TestNoAltKeyBindings(t *testing.T) {
	for _, dir := range []string{".", "components", "keys"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			if m := altLiteral.FindString(string(src)); m != "" {
				t.Errorf("%s binds an alt chord (%s…); alt chords never reach Update", f, m)
			}
		}
	}
}

// The user-facing inventory must not advertise an alt chord either, or /help
// promises a key that cannot fire.
func TestKeySectionsAdvertiseNoAlt(t *testing.T) {
	for _, s := range keySections() {
		for _, b := range s.Bindings {
			if strings.Contains(b.Keys, "alt+") {
				t.Errorf("section %q advertises the alt chord %q", s.Title, b.Keys)
			}
		}
	}
}

// The four session toggles are handled in the global KeyMsg switch, before
// view dispatch, so they must fire from every screen — not just chat.
func TestGlobalToggleKeysFireFromAnyView(t *testing.T) {
	tests := []struct {
		key   tea.KeyType
		name  string
		check func(a *App) bool
		desc  string
	}{
		{tea.KeyF2, "f2", func(a *App) bool { return a.settings.CavemanEnabled() }, "caveman"},
		{tea.KeyF3, "f3", func(a *App) bool { return a.guardrailsEnabled() }, "guardrails"},
		{tea.KeyF4, "f4", func(a *App) bool { return a.askEnabled() }, "ask"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := New(Options{Workdir: t.TempDir()})
			// A full-screen view is the case that matters: the global switch
			// has to win before viewHandlers gets the key. The assertion is a
			// flip rather than an absolute, because the global settings file
			// is shared by the whole package and may already carry a value.
			a.view = viewSettings
			before := tc.check(a)
			a.Update(tea.KeyMsg{Type: tc.key})
			if tc.check(a) == before {
				t.Fatalf("%s: %s did not flip from a non-chat view (still %v)", tc.name, tc.desc, before)
			}
		})
	}
}

// f5 cycles the mode from any screen, and keeps plan mode in sync.
func TestModeCycleKeyFromAnyView(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.view = viewSettings
	before := a.mode
	a.Update(tea.KeyMsg{Type: tea.KeyF5})
	if a.mode == before {
		t.Fatalf("f5 must cycle the mode; still %q", a.mode)
	}
}

// f6 is deliberately chat-scoped: it opens the save-prompt naming mode from
// the composer and does nothing on a full-screen view.
func TestSavePromptKeyIsChatScoped(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.editor.SetValue("remember this")
	a.Update(tea.KeyMsg{Type: tea.KeyF6})
	if !a.savePromptMode {
		t.Fatal("f6 must start the save-prompt flow from chat")
	}

	b := New(Options{Workdir: t.TempDir()})
	b.view = viewSettings
	b.editor.SetValue("remember this")
	b.Update(tea.KeyMsg{Type: tea.KeyF6})
	if b.savePromptMode {
		t.Fatal("f6 must not start the save-prompt flow from a full-screen view")
	}
}

// Toggling caveman from a full-screen view still persists and still announces
// itself, because the handler is the same one the chat view calls.
func TestCavemanToggleFromSettingsViewPersists(t *testing.T) {
	workdir := t.TempDir()
	a := New(Options{Workdir: workdir})
	a.view = viewSettings
	want := !a.settings.CavemanEnabled()
	a.Update(tea.KeyMsg{Type: tea.KeyF2})

	proj, err := config.LoadProject(workdir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if proj.Caveman == nil {
		t.Fatal("f2 from a full-screen view must write an explicit caveman value to the project scope")
	}
	if *proj.Caveman != want {
		t.Fatalf("persisted caveman = %v, want %v", *proj.Caveman, want)
	}
	if a.messages[len(a.messages)-1].Text() == "" {
		t.Fatal("the toggle must announce itself in the transcript")
	}
}
