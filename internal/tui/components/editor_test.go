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

func TestCursorOffsetAcrossLines(t *testing.T) {
	e := NewEditor()
	e.SetValue("first\nsecond\nthird")
	e.CursorEnd()
	if got := e.CursorOffset(); got != 18 {
		t.Fatalf("CursorOffset at end = %d, want 18", got)
	}
}

func TestReplaceRangeInsertsAtCursor(t *testing.T) {
	e := NewEditor()
	_ = e.Focus()
	e.SetValue("hello world")
	e.textarea.SetCursor(6)
	start := e.CursorOffset()
	if start != 6 {
		t.Fatalf("cursor offset = %d, want 6", start)
	}

	e.ReplaceRange(start, start+5, "friend")
	if got := e.Value(); got != "hello friend" {
		t.Fatalf("value = %q, want hello friend", got)
	}
	if got := e.CursorOffset(); got != 12 {
		t.Fatalf("cursor offset = %d, want 12", got)
	}
}

func TestReplaceRangeOnMultiLineValue(t *testing.T) {
	e := NewEditor()
	e.SetValue("line one\nline two")
	e.CursorEnd()
	off := e.CursorOffset()
	e.ReplaceRange(0, off, "replacement")
	if got := e.Value(); got != "replacement" {
		t.Fatalf("value = %q, want replacement", got)
	}
	if got := e.CursorOffset(); got != 11 {
		t.Fatalf("cursor offset = %d, want 11", got)
	}
}
