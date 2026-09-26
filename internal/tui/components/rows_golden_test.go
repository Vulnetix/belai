package components

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// goldenFixtures exercise every shape the transcript renderers can produce: a
// short tool result, one long enough to wrap, an error body, a collapsed
// result carrying a marker, an expanded one carrying none, a blank interior
// line, a system notice, a markdown assistant turn, and a coalesced belai
// group.
func goldenFixtures() []struct {
	name string
	ml   MessageList
} {
	tool := func(name, args, content, status string) Message {
		return Message{Role: "tool", ToolName: name, ToolArgs: args, Content: content, Status: status}
	}
	return []struct {
		name string
		ml   MessageList
	}{
		{"tool_short", MessageList{ShowTools: true, Messages: []Message{
			tool("Read", `{"path":"internal/tui/app.go"}`, "package tui", "✓")}}},
		{"tool_wrapping", MessageList{ShowTools: true, Messages: []Message{
			tool("Bash", `{"command":"go build ./..."}`,
				"a single very long line of output that has to wrap several times before it runs out of things to say", "✓")}}},
		{"tool_error", MessageList{ShowTools: true, Messages: []Message{
			tool("Bash", `{"command":"go test ./..."}`,
				"FAIL\tgithub.com/vulnetix/belai/internal/run\t0.2s\nexit status 1", "✗")}}},
		{"tool_collapsed_marker", MessageList{ShowTools: true, Messages: []Message{
			tool("Bash", `{"command":"ls -la"}`, "first\nsecond\nthird\nfourth\nfifth", "✓")}}},
		{"tool_expanded", MessageList{ShowTools: true, ExpandAll: true, Messages: []Message{
			tool("Bash", `{"command":"ls -la"}`, "first\nsecond\nthird\nfourth\nfifth", "✓")}}},
		{"tool_blank_interior_line", MessageList{ShowTools: true, ExpandAll: true, Messages: []Message{
			tool("Bash", `{"command":"cat notes"}`, "head\n\ntail", "✓")}}},
		{"tool_withheld", MessageList{ShowTools: true, Messages: []Message{
			tool("WebFetch", `{"url":"https://example.com"}`,
				"tool result withheld: provider error\nsecond line", "withheld")}}},
		{"system_short", MessageList{Messages: []Message{
			{Role: "system", Content: "retrying (2/3) after 800ms — rate limited"}}}},
		{"system_wrapping", MessageList{Messages: []Message{
			{Role: "system", Content: "a system notice long enough that it has to wrap onto a second and probably a third line at narrow widths"}}}},
		{"assistant_markdown", MessageList{Messages: []Message{
			{Role: "assistant", Content: "## Title\n\n- one\n- two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfunc main() {}\n```"}}}},
		{"belai_group", MessageList{Messages: []Message{
			{Role: "system", Content: "first notice"},
			{Role: "system", Content: "second notice"},
			{Role: "system", Content: "third notice"}}}},
		{"mixed", mixedTranscript()},
	}
}

var goldenWidths = []int{32, 41, 53, 60, 80, 120}

// TestRowRenderersGolden pins the exact bytes toolRow and systemRow emit, so a
// refactor of how they are built has to prove it changed nothing. Captured
// under Ascii — the profile has no bearing on layout, and a plain golden is
// reviewable in a diff where an escape-laden one is not.
//
// Refresh with: UPDATE_GOLDEN=1 go test ./internal/tui/components/
func TestRowRenderersGolden(t *testing.T) {
	path := filepath.Join("testdata", "rows.golden")

	var got strings.Builder
	withProfile(t, termenv.Ascii, func() {
		for _, f := range goldenFixtures() {
			for _, width := range goldenWidths {
				ml := f.ml
				ml.Width = width
				rendered, _ := ml.Render()
				fmt.Fprintf(&got, "=== %s w=%d\n%s\n", f.name, width, rendered)
			}
		}
	})

	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if got.String() != string(want) {
		t.Errorf("rendered output changed; diff against %s", path)
		gotLines := strings.Split(got.String(), "\n")
		wantLines := strings.Split(string(want), "\n")
		for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
			g, w := "", ""
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Errorf("line %d:\n  got  %q\n  want %q", i, g, w)
			}
		}
	}
}

// TestToolRowErrorBodyIsColoured pins behaviour that was dead code until tool
// bodies rendered through Row: renderToolContent styled error output with
// DangerStyle and then stripped it, so a failing command's output was never
// actually red. The hint stays muted — it is chrome, not output.
func TestToolRowErrorBodyIsColoured(t *testing.T) {
	withProfile(t, termenv.TrueColor, func() {
		ml := MessageList{Width: 60, ShowTools: true, Messages: []Message{{
			Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"go test ./..."}`,
			Content: "FAIL\tinternal/run\nexit status 1\nmore\nlines",
		}}}
		rendered, _ := ml.Render()

		danger := fgSeq(ColorDanger)
		if danger == "" {
			t.Fatal("expected a danger sequence under TrueColor")
		}
		if !strings.Contains(rendered, danger) {
			t.Fatalf("error body is not rendered in the danger colour:\n%q", rendered)
		}
		if !strings.Contains(rendered, fgSeq(ColorMuted)+"… ") {
			t.Fatalf("truncation hint is not muted:\n%q", rendered)
		}
	})
}

// TestRowRenderersGoldenHoldsUnderColour renders the same fixtures under
// TrueColor and asserts the visible text is identical to the Ascii golden.
// Colour may change how a row looks; it must never change what it says or how
// wide it is.
func TestRowRenderersGoldenHoldsUnderColour(t *testing.T) {
	for _, f := range goldenFixtures() {
		for _, width := range goldenWidths {
			var plain, coloured string
			withProfile(t, termenv.Ascii, func() {
				ml := f.ml
				ml.Width = width
				plain, _ = ml.Render()
			})
			withProfile(t, termenv.TrueColor, func() {
				ml := f.ml
				ml.Width = width
				coloured, _ = ml.Render()
			})
			if ansi.Strip(coloured) != plain {
				t.Errorf("%s w=%d: colour changed the visible text\n plain=%q\n  got =%q",
					f.name, width, plain, ansi.Strip(coloured))
			}
		}
	}
}
