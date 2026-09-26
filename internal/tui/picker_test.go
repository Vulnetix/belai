package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/vulnetix/belai/internal/tui/components"
)

func TestRenderPickerHighlightsOnlyTheSelectedRow(t *testing.T) {
	cands := []string{"a.go", "b.go", "c.go"}

	none, _ := renderPicker("files", "1/3", 80, cands, -1, 0, nil, lipgloss.TerminalColor(components.ColorTeal))
	if strings.Contains(none, components.Cursor(true)) {
		t.Fatalf("no selection must not render a cursor:\n%s", none)
	}

	first, _ := renderPicker("files", "1/3", 80, cands, 0, 0, nil, lipgloss.TerminalColor(components.ColorTeal))
	if !strings.Contains(first, components.Cursor(true)) {
		t.Fatalf("selected row must render a cursor:\n%s", first)
	}
}
