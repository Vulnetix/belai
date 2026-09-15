package components

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestEditorHeightAccessors(t *testing.T) {
	e := NewEditor()
	e.SetHeight(5)
	if e.Height() != 5 {
		t.Fatalf("Height = %d, want 5", e.Height())
	}
	e.SetValue("a\nb\nc")
	if e.LineCount() < 3 {
		t.Fatalf("LineCount = %d, want >= 3", e.LineCount())
	}
}

func TestCtrlJInsertsNewline(t *testing.T) {
	e := NewEditor()
	_ = e.Focus()
	e.SetValue("")
	_ = e.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(e.Value(), "\n") {
		t.Fatalf("expected newline after ctrl+j, got %q", e.Value())
	}
}

func TestInitialInsertNewlineBinding(t *testing.T) {
	e := NewEditor()
	help := e.textarea.KeyMap.InsertNewline.Help().Key
	if help != "ctrl+j" {
		t.Fatalf("InsertNewline help key = %q, want ctrl+j", help)
	}
}
