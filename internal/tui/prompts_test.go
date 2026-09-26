package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/promptlib"
)

// key is a small helper for rune-based key presses.
func keyRune(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func TestCtrlSOverwritesLoadedGlobalEntryInPlace(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	entry, err := promptlib.Create(config.ScopeGlobal, workdir, "deploy", "old body")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	a := New(Options{Workdir: workdir})
	a.loadedPrompt = &entry
	a.editor.SetValue("new body")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !a.promptAction || a.promptConfirm != "" {
		t.Fatalf("ctrl+s should open the action bar: action=%v confirm=%q", a.promptAction, a.promptConfirm)
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.promptConfirm != "overwrite" {
		t.Fatalf("enter should arm overwrite confirm, got %q", a.promptConfirm)
	}
	a.handleChatKey(keyRune('y'))

	// The global file was overwritten in place.
	reloaded, err := promptlib.Load(config.ScopeGlobal, workdir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(reloaded.Entries) != 1 || reloaded.Entries[0].Prompt != "new body" || reloaded.Entries[0].Path != entry.Path {
		t.Fatalf("global entry not overwritten in place: %+v", reloaded.Entries)
	}
	// No project entry was minted — the f7 bug is that a global entry became
	// project-local on re-save.
	proj, _ := promptlib.Load(config.ScopeProject, workdir)
	if len(proj.Entries) != 0 {
		t.Fatalf("project library should be untouched, got %+v", proj.Entries)
	}
}

func TestActionBarDeleteConfirm(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	entry, _ := promptlib.Create(config.ScopeProject, workdir, "deploy", "body")
	a := New(Options{Workdir: workdir})
	a.loadedPrompt = &entry
	a.editor.SetValue("body")

	// d arms delete; n cancels back to the bar without deleting.
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	a.handleChatKey(keyRune('d'))
	if a.promptConfirm != "delete" {
		t.Fatalf("d should arm delete, got %q", a.promptConfirm)
	}
	a.handleChatKey(keyRune('n'))
	if a.promptConfirm != "" {
		t.Fatalf("n should return to the bar, got %q", a.promptConfirm)
	}
	if _, err := os.Stat(entry.Path); err != nil {
		t.Fatalf("n must not delete the file: %v", err)
	}

	// d then y deletes the file.
	a.handleChatKey(keyRune('d'))
	a.handleChatKey(keyRune('y'))
	if _, err := os.Stat(entry.Path); !os.IsNotExist(err) {
		t.Fatalf("y should delete the file: %v", err)
	}
	if a.loadedPrompt != nil {
		t.Fatal("deleting must drop the loaded entry")
	}
}

func TestComposerBadgePersistsAcrossEdits(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.loadedPrompt = &promptlib.Entry{Name: "deploy", Prompt: "original", Scope: config.ScopeProject}
	a.editor.SetValue("original")

	view := a.renderComposer()
	if !strings.Contains(view, "deploy") || strings.Contains(view, "deploy*") {
		t.Fatalf("clean badge should name the entry without a dirty marker:\n%s", view)
	}

	a.editor.SetValue("original edited")
	view = a.renderComposer()
	if !strings.Contains(view, "deploy*") {
		t.Fatalf("edited badge should carry the dirty marker:\n%s", view)
	}
}

func TestComposerBadgeMarksGlobalAndAbsentForHistory(t *testing.T) {
	a := New(Options{Workdir: t.TempDir()})
	a.loadedPrompt = &promptlib.Entry{Name: "deploy", Prompt: "x", Scope: config.ScopeGlobal}
	a.editor.SetValue("x")
	if !strings.Contains(a.renderComposer(), "deploy g") {
		t.Fatalf("global badge should carry the g marker:\n%s", a.renderComposer())
	}

	// A plain session-history result carries no entry, so no badge.
	a.historyActive = true
	a.historyResults = []historyItem{{Prompt: "plain history"}}
	a.historyIndex = 0
	a.editor.SetValue("plain history")
	a.loadedPrompt = nil
	if strings.Contains(a.renderComposer(), "✎") {
		t.Fatalf("unnamed history result must not show the badge:\n%s", a.renderComposer())
	}
}

func TestLoadedPromptClearedOnSubmitAndViews(t *testing.T) {
	entry := &promptlib.Entry{Name: "deploy", Prompt: "x", Path: "p"}

	// Submit: a slash command routes the composer away from the entry.
	a := New(Options{Workdir: t.TempDir()})
	a.loadedPrompt = entry
	a.editor.SetValue("/help")
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if a.loadedPrompt != nil {
		t.Fatal("submit must clear the loaded prompt")
	}

	// push() resets the editor, so the badge must go too.
	b := New(Options{Workdir: t.TempDir()})
	b.loadedPrompt = entry
	b.push(viewSettings)
	if b.loadedPrompt != nil {
		t.Fatal("push must clear the loaded prompt")
	}

	// New session and resume both drop it.
	c := New(Options{Workdir: t.TempDir()})
	c.loadedPrompt = entry
	c.startNewSession()
	if c.loadedPrompt != nil {
		t.Fatal("startNewSession must clear the loaded prompt")
	}

	d := New(Options{Workdir: t.TempDir()})
	d.loadedPrompt = entry
	d.clearForResume()
	if d.loadedPrompt != nil {
		t.Fatal("clearForResume must clear the loaded prompt")
	}
}

func TestDisabledEntriesAbsentFromHistory(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	enabled, _ := promptlib.Create(config.ScopeGlobal, workdir, "enabled", "on")
	_, err := promptlib.SetEnabled(enabled, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}

	a := New(Options{Workdir: workdir})
	got := a.buildHistoryResults("")
	if len(got) != 0 {
		t.Fatalf("disabled entries must not reach the cycle, got %+v", got)
	}
}

func TestPromptsManagerToggleAndReorder(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	if _, err := promptlib.Create(config.ScopeProject, workdir, "a", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := promptlib.Create(config.ScopeProject, workdir, "b", "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := promptlib.Create(config.ScopeProject, workdir, "c", "c"); err != nil {
		t.Fatal(err)
	}

	a := New(Options{Workdir: workdir})
	a.push(viewPrompts)

	// Toggle the first entry off.
	a.promptsState.selected = 0
	a.handlePromptsKey(tea.KeyMsg{Type: tea.KeySpace})
	listing, _ := promptlib.Load(config.ScopeProject, workdir)
	if len(listing.Entries) != 3 || listing.Entries[0].Enabled {
		t.Fatalf("toggle should disable the first entry: %+v", listing.Entries)
	}

	// Move the (now disabled) first entry down with J.
	a.handlePromptsKey(keyRune('J'))
	if a.promptsState.selected != 1 {
		t.Fatalf("J should move selection down, got %d", a.promptsState.selected)
	}
	listing, _ = promptlib.Load(config.ScopeProject, workdir)
	want := []string{"b", "a", "c"}
	if len(listing.Entries) != 3 {
		t.Fatalf("entries = %+v", listing.Entries)
	}
	for i := range want {
		if listing.Entries[i].Name != want[i] {
			t.Fatalf("reorder produced %+v, want names %v", listing.Entries, want)
		}
	}
	// Disabled state survives the renumber.
	if listing.Entries[1].Name != "a" || listing.Entries[1].Enabled {
		t.Fatalf("disabled entry lost its state: %+v", listing.Entries)
	}
}

func TestEditorUnsetFallsBackToInline(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	_, _ = promptlib.Create(config.ScopeProject, workdir, "deploy", "body")
	a := New(Options{Workdir: workdir})
	a.push(viewPrompts)
	a.promptsState.selected = 0

	a.handlePromptsKey(keyRune('e'))
	if a.promptsState.mode != "edit" {
		t.Fatalf("missing editor should fall back to inline edit, mode=%q", a.promptsState.mode)
	}
	if a.editor.Value() != "body" {
		t.Fatalf("inline editor should hold the body, got %q", a.editor.Value())
	}
	// The inline meta advertises ctrl+j for newlines.
	if !strings.Contains(a.promptsView(), "ctrl+j newline") {
		t.Fatalf("inline editor meta should advertise ctrl+j newline:\n%s", a.promptsView())
	}
}

func TestPromptEditedReloadsAndReenablesMouse(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	entry, _ := promptlib.Create(config.ScopeProject, workdir, "deploy", "body")
	a := New(Options{Workdir: workdir})
	a.push(viewPrompts)

	// Mouse enabled by default: the handler must batch the re-enable command.
	if cmd := a.handlePromptEdited(promptEditedMsg{path: entry.Path}); cmd == nil {
		t.Fatal("mouse enabled should return a command to re-enable it")
	} else if msg := cmd(); msg == nil {
		t.Fatal("mouse re-enable command produced no message")
	}
	if a.promptsState.selected != 0 {
		t.Fatalf("reload should re-find the entry by path, selected=%d", a.promptsState.selected)
	}

	// Mouse disabled: no re-enable command.
	off := false
	a.settings.UI = &config.UISettings{Mouse: &off}
	if cmd := a.handlePromptEdited(promptEditedMsg{path: entry.Path}); cmd != nil {
		t.Fatal("mouse disabled should not emit a re-enable command")
	}
}

func TestEditorRefusedWhileWorking(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	_, _ = promptlib.Create(config.ScopeProject, workdir, "deploy", "body")
	a := New(Options{Workdir: workdir})
	a.cancel = func() {}
	a.push(viewPrompts)
	a.promptsState.selected = 0

	a.handlePromptsKey(keyRune('e'))
	if !strings.Contains(a.promptsState.errorMsg, "running") {
		t.Fatalf("editor while working should be refused, got %q", a.promptsState.errorMsg)
	}
}

func TestSaveAsOntoExistingNameConfirms(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	_, _ = promptlib.Create(config.ScopeProject, workdir, "deploy", "original")
	a := New(Options{Workdir: workdir})
	a.editor.SetValue("new body")

	a.handleChatKey(tea.KeyMsg{Type: tea.KeyF7})
	for _, r := range "deploy" {
		a.handleChatKey(keyRune(r))
	}
	a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a.savePromptConfirm {
		t.Fatal("duplicate name should arm the overwrite confirm")
	}
	// The original is untouched until y.
	listing, _ := promptlib.Load(config.ScopeProject, workdir)
	if listing.Entries[0].Prompt != "original" {
		t.Fatalf("original should survive until confirm, got %+v", listing.Entries)
	}
	a.handleChatKey(keyRune('y'))
	listing, _ = promptlib.Load(config.ScopeProject, workdir)
	if len(listing.Entries) != 1 || listing.Entries[0].Prompt != "new body" {
		t.Fatalf("confirm should overwrite in place, got %+v", listing.Entries)
	}
	if a.savePromptMode {
		t.Fatal("save mode should exit after overwrite")
	}
}

func TestLegacyNoticeFiresOnceAndNeverTouchesPromptsJson(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	workdir := t.TempDir()

	legacy := filepath.Join(workdir, ".vulnetix", "prompts.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"entries":[{"name":"x","prompt":"y"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	a := New(Options{Workdir: workdir})
	a.maybeNoticeLegacyPrompts()
	a.maybeNoticeLegacyPrompts()

	count := 0
	for _, m := range a.messages {
		if m.Role == "system" && strings.Contains(m.Text(), "prompts.json is no longer read") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("legacy notice should fire exactly once, got %d", count)
	}
	before, _ := os.ReadFile(legacy)
	if string(before) != `{"entries":[{"name":"x","prompt":"y"}]}` {
		t.Fatalf("legacy file must never be read or rewritten: %q", before)
	}
}
