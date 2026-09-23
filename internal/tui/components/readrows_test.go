package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestSplitReadGutter(t *testing.T) {
	body, first, trailer, ok := splitReadGutter("    41\tone\n    42\t\ttwo\n[Read: lines 41–42 of 90; truncated — continue with offset=43]")
	if !ok || first != 41 || body != "one\n\ttwo" || !strings.HasPrefix(trailer, "[Read: lines 41–42") {
		t.Fatalf("got body=%q first=%d trailer=%q ok=%v", body, first, trailer, ok)
	}

	for _, content := range []string{
		"plain file\nno gutter",      // attachment / old transcript
		"     1\tone\n     3\tthree", // numbers must run consecutively
		"[Read: offset 9 is past the end of the file (3 lines); nothing more to read]",
		"",
	} {
		if _, _, _, ok := splitReadGutter(content); ok {
			t.Fatalf("%q: detected a gutter that is not there", content)
		}
	}
}

// The tool's own gutter is redrawn, not doubled, and the trailer stays.
func TestReadRowRedrawsToolGutter(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"file_path":"main.go","offset":7}`,
		Content:  "     7\tone\n     8\ttwo\n[Read: lines 7–8 of 20; truncated — continue with offset=9]",
		Status:   "✓",
		Meta:     map[string]any{"start_line": 7, "numbered": true},
	}
	out, _ := toolRow(msg, 80, true)
	plain := ansi.Strip(out)
	for _, want := range []string{"7 one", "8 two", "continue with offset=9"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing %q:\n%s", want, plain)
		}
	}
	// The tool pads numbers to six columns; the TUI sizes its own gutter.
	if strings.Contains(plain, "     7") {
		t.Fatalf("gutter drawn twice:\n%s", plain)
	}
}

func TestFileTextStripsGutter(t *testing.T) {
	msg := Message{Role: "tool", ToolName: "Read", Content: "     1\tpackage main\n     2\t\n     3\tfunc main() {}"}
	if got := msg.FileText(); got != "package main\n\nfunc main() {}" {
		t.Fatalf("FileText = %q", got)
	}
	att := Message{Role: "tool", ToolName: "Read", Content: "verbatim\nbytes\n"}
	if got := att.FileText(); got != "verbatim\nbytes\n" {
		t.Fatalf("verbatim FileText = %q", got)
	}
}
