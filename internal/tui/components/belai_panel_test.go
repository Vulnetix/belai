package components

import (
	"strconv"
	"strings"
	"testing"
)

// TestBelaiPanelCollapsedTitleBar pins the fix for issue #1: a collapsed
// belai panel advertises the ctrl+o binding in its top-right metadata,
// mirroring the helper text the ask/composer panel carries in its own frame.
func TestBelaiPanelCollapsedTitleBar(t *testing.T) {
	var msgs []Message
	for i := 0; i < belaiPreviewLines+2; i++ {
		msgs = append(msgs, Message{Role: "system", Content: "notice " + strconv.Itoa(i)})
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	s, _, _ := belaiPanel(msgs, idxs, 60, false)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		t.Fatal("belai panel rendered no lines")
	}
	top := lines[0]
	if !strings.Contains(top, "ctrl+o") || !strings.Contains(top, "expand all") {
		t.Fatalf("collapsed belai panel title bar should advertise ctrl+o, got:\n%s", top)
	}
	if !strings.Contains(s, "2 more lines") {
		t.Fatalf("expected truncation hint, got:\n%s", s)
	}
}

// TestBelaiPanelExpandedTitleBar confirms the ctrl+o hint only appears when
// the panel is collapsed; expanding the whole transcript keeps the title bar
// clean.
func TestBelaiPanelExpandedTitleBar(t *testing.T) {
	var msgs []Message
	for i := 0; i < belaiPreviewLines+2; i++ {
		msgs = append(msgs, Message{Role: "system", Content: "notice " + strconv.Itoa(i)})
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	s, _, _ := belaiPanel(msgs, idxs, 60, true)
	if strings.Contains(s, "ctrl+o") {
		t.Fatalf("expanded belai panel should not advertise ctrl+o, got:\n%s", s)
	}
	if strings.Contains(s, "more lines") {
		t.Fatalf("expanded belai panel should not truncate, got:\n%s", s)
	}
}

// TestBelaiPanelWithCollapsedToolRow shows the ctrl+o hint even when the
// panel itself is not truncated, as long as a nested tool row has hidden
// content that ctrl+o would reveal.
func TestBelaiPanelWithCollapsedToolRow(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "running command"},
		{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"seq 5"}`, Content: "1\n2\n3\n4\n5", Status: "✓"},
	}
	idxs := []int{0, 1}
	s, _, _ := belaiPanel(msgs, idxs, 60, false)
	if !strings.Contains(s, "earlier lines") {
		t.Fatalf("expected collapsed Bash row, got:\n%s", s)
	}
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		t.Fatal("belai panel rendered no lines")
	}
	top := lines[0]
	if !strings.Contains(top, "ctrl+o") || !strings.Contains(top, "expand all") {
		t.Fatalf("belai panel with collapsed tool row should advertise ctrl+o, got:\n%s", top)
	}
}

// TestBelaiPanelTitleBarFitsNarrowWidth confirms the metadata hint is dropped
// before the title is truncated at very narrow widths, so the frame never
// breaks even when ctrl+o cannot fit.
func TestBelaiPanelTitleBarFitsNarrowWidth(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "a"},
		{Role: "system", Content: "b"},
		{Role: "system", Content: "c"},
		{Role: "system", Content: "d"},
		{Role: "system", Content: "e"},
		{Role: "system", Content: "f"},
		{Role: "system", Content: "g"},
		{Role: "system", Content: "h"},
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	// 24 is panelMinWidth; the meta cannot fit, but the frame must still render.
	s, _, _ := belaiPanel(msgs, idxs, 24, false)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		t.Fatal("belai panel rendered no lines")
	}
	top := lines[0]
	// Title must still be present; meta may have been dropped.
	if !strings.Contains(top, "belai") {
		t.Fatalf("title bar must keep the belai title, got:\n%s", top)
	}
}
