package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/processlib"
	"github.com/vulnetix/signet/internal/tui/components"
)

func TestIsProcessInput(t *testing.T) {
	if !isProcessInput("!!sleep 30") {
		t.Fatal("expected !!sleep 30 to be process input")
	}
	if isProcessInput("!ls") {
		t.Fatal("expected !ls to be shell input, not process")
	}
}

func TestHandleProcessCreatesLibraryEntry(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{Workdir: dir})
	cmd := "echo hello"
	// handleProcess returns a tea.Cmd; executing it arms the watcher and may
	// emit the start event.
	fn := a.handleProcess("!!" + cmd)
	if fn == nil {
		t.Fatal("expected a command from handleProcess")
	}
	listing, err := processlib.Load(config.ScopeProject, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(listing.Entries) != 1 {
		t.Fatalf("expected 1 process entry, got %d", len(listing.Entries))
	}
	if listing.Entries[0].Command != cmd {
		t.Fatalf("command = %q, want %q", listing.Entries[0].Command, cmd)
	}
	// A live Process tool row should have been appended.
	found := false
	for _, m := range a.messages {
		if m.Role == "tool" && m.ToolName == "Process" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a Process tool row in messages")
	}
}

func TestProcessLibraryToggleReorderDelete(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{Workdir: dir})
	a.enterProcesses()
	if _, err := processlib.CreateUnique(config.ScopeProject, dir, "sleep 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := processlib.CreateUnique(config.ScopeProject, dir, "echo b"); err != nil {
		t.Fatal(err)
	}

	_, _ = a.handleProcessesKey(tea.KeyMsg{Type: tea.KeySpace}) // toggle a off
	listing, _ := processlib.Load(config.ScopeProject, dir)
	if listing.Entries[0].Enabled {
		t.Fatal("expected first entry to be disabled")
	}
	_, _ = a.handleProcessesKey(tea.KeyMsg{Type: tea.KeySpace}) // toggle a back on

	a.processesState.selected = 0
	_, _ = a.handleProcessesKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'J'}})
	listing, _ = processlib.Load(config.ScopeProject, dir)
	if listing.Entries[0].Name != "echo" {
		t.Fatalf("expected echo first after reorder, got %s", listing.Entries[0].Name)
	}

	a.processesState.selected = 0
	_, _ = a.handleProcessesKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	_, _ = a.handleProcessesKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	listing, _ = processlib.Load(config.ScopeProject, dir)
	if len(listing.Entries) != 1 {
		t.Fatalf("expected 1 entry after delete, got %d", len(listing.Entries))
	}
}

func TestProcessProgressAppendsToToolRow(t *testing.T) {
	a := &App{messages: []components.Message{{Role: "tool", ToolName: "Process", ToolCallID: "proc-p1"}}}
	a.handleProcessProgress(processProgressMsg{id: "p1", text: "line one\nline two"})
	lines, _ := a.messages[0].ProgressTail(10)
	if len(a.messages) != 1 || len(lines) != 2 {
		t.Fatalf("expected two progress lines on the row, got %d: %+v", len(lines), a.messages[0])
	}
}
