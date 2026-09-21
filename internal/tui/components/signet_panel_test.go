package components

import (
	"strconv"
	"strings"
	"testing"
)

// TestSignetPanelCollapsedTitleBar pins the fix for issue #1: a collapsed
// signet panel advertises the ctrl+o binding in its top-right metadata,
// mirroring the helper text the ask/composer panel carries in its own frame.
func TestSignetPanelCollapsedTitleBar(t *testing.T) {
	var msgs []Message
	for i := 0; i < signetPreviewLines+2; i++ {
		msgs = append(msgs, Message{Role: "system", Content: "notice " + strconv.Itoa(i)})
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	s, _, _ := signetPanel(msgs, idxs, 60, false)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		t.Fatal("signet panel rendered no lines")
	}
	top := lines[0]
	if !strings.Contains(top, "ctrl+o") || !strings.Contains(top, "expand all") {
		t.Fatalf("collapsed signet panel title bar should advertise ctrl+o, got:\n%s", top)
	}
	if !strings.Contains(s, "2 more lines") {
		t.Fatalf("expected truncation hint, got:\n%s", s)
	}
}

// TestSignetPanelExpandedTitleBar confirms the ctrl+o hint only appears when
// the panel is collapsed; expanding the whole transcript keeps the title bar
// clean.
func TestSignetPanelExpandedTitleBar(t *testing.T) {
	var msgs []Message
	for i := 0; i < signetPreviewLines+2; i++ {
		msgs = append(msgs, Message{Role: "system", Content: "notice " + strconv.Itoa(i)})
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	s, _, _ := signetPanel(msgs, idxs, 60, true)
	if strings.Contains(s, "ctrl+o") {
		t.Fatalf("expanded signet panel should not advertise ctrl+o, got:\n%s", s)
	}
	if strings.Contains(s, "more lines") {
		t.Fatalf("expanded signet panel should not truncate, got:\n%s", s)
	}
}

// TestSignetPanelTitleBarFitsNarrowWidth confirms the metadata hint is dropped
// before the title is truncated at very narrow widths, so the frame never
// breaks even when ctrl+o cannot fit.
func TestSignetPanelTitleBarFitsNarrowWidth(t *testing.T) {
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
	s, _, _ := signetPanel(msgs, idxs, 24, false)
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		t.Fatal("signet panel rendered no lines")
	}
	top := lines[0]
	// Title must still be present; meta may have been dropped.
	if !strings.Contains(top, "signet") {
		t.Fatalf("title bar must keep the signet title, got:\n%s", top)
	}
}
