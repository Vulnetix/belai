package components

import (
	"strings"
	"testing"
)

func TestTurnPanelRendersAssistantContent(t *testing.T) {
	msg := Message{Role: "assistant", Content: "hello world"}
	out := turnPanel(msg, 40, false)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected content in panel, got:\n%s", out)
	}
	if !strings.Contains(out, "signet") {
		t.Fatalf("expected title in panel, got:\n%s", out)
	}
}

func TestTurnPanelTruncatesLongAssistantContent(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five"}
	msg := Message{Role: "assistant", Content: strings.Join(lines, "\n")}
	out := turnPanel(msg, 40, false)
	for i := 0; i < assistantPreviewLines; i++ {
		if !strings.Contains(out, lines[i]) {
			t.Fatalf("expected line %d %q in truncated panel, got:\n%s", i, lines[i], out)
		}
	}
	if strings.Contains(out, "five") {
		t.Fatalf("hidden line should not appear in truncated panel, got:\n%s", out)
	}
	if !strings.Contains(out, "1 more lines") {
		t.Fatalf("expected hidden-line hint in truncated panel, got:\n%s", out)
	}
}

func TestTurnPanelExpandedShowsAllLines(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five"}
	msg := Message{Role: "assistant", Content: strings.Join(lines, "\n")}
	out := turnPanel(msg, 40, true)
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Fatalf("expected line %q in expanded panel, got:\n%s", l, out)
		}
	}
	if strings.Contains(out, "more lines") {
		t.Fatalf("expanded panel should not have truncation hint, got:\n%s", out)
	}
}

func TestTurnPanelEmptyBodyStillRendersFrame(t *testing.T) {
	msg := Message{Role: "assistant", Content: ""}
	out := turnPanel(msg, 40, false)
	if !strings.Contains(out, "signet") {
		t.Fatalf("expected empty panel to render title, got:\n%s", out)
	}
}

func TestTurnPanelUserTitle(t *testing.T) {
	msg := Message{Role: "user", Content: "hi"}
	out := turnPanel(msg, 40, false)
	if !strings.Contains(out, "you") {
		t.Fatalf("expected user title, got:\n%s", out)
	}
}

func TestToolRowRendersNameAndStatus(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"echo hi"}`,
		Content:  "hi",
		Status:   "✓",
	}
	out := toolRow(msg, 60, false)
	if !strings.Contains(out, "Bash") {
		t.Fatalf("expected tool name, got:\n%s", out)
	}
	if !strings.Contains(out, "echo hi") {
		t.Fatalf("expected command argument, got:\n%s", out)
	}
	if !strings.Contains(out, "hi") {
		t.Fatalf("expected result preview, got:\n%s", out)
	}
	if !strings.Contains(out, "✓") {
		t.Fatalf("expected success status, got:\n%s", out)
	}
}

func TestToolRowExtractsInvocationForRead(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"path":"internal/foo.go"}`,
		Content:  "package foo",
		Status:   "✓",
	}
	out := toolRow(msg, 80, false)
	if !strings.Contains(out, "internal/foo.go") {
		t.Fatalf("expected path argument, got:\n%s", out)
	}
}

func TestToolRowShowsFirstLineAndHint(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"seq 3"}`,
		Content:  "1\n2\n3",
		Status:   "✓",
	}
	out := toolRow(msg, 80, false)
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected preview line, got:\n%s", out)
	}
	preview := lines[1]
	if !strings.HasPrefix(strings.TrimSpace(preview), "1") {
		t.Fatalf("expected preview to start with first output line, got:\n%s", out)
	}
	if !strings.Contains(out, "2 more lines") {
		t.Fatalf("expected hidden-line hint, got:\n%s", out)
	}
}

func TestToolRowExpandedShowsFullContent(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"seq 3"}`,
		Content:  "1\n2\n3",
		Status:   "✓",
	}
	out := toolRow(msg, 80, true)
	for _, want := range []string{"1", "2", "3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected line %q in expanded output, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "more lines") {
		t.Fatalf("expanded output should not have hint, got:\n%s", out)
	}
}

func TestToolRowBashErrorIsRed(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"false"}`,
		Content:  "exit status 1",
		Status:   "",
	}
	out := toolRow(msg, 80, false)
	if !strings.Contains(out, "✗") {
		t.Fatalf("expected error status for bash failure, got:\n%s", out)
	}
	if !strings.Contains(out, "exit status 1") {
		t.Fatalf("expected error preview, got:\n%s", out)
	}
}

func TestToolRowWithheldStatus(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"path":"secret.txt"}`,
		Content:  "tool result withheld: permission denied for \"Read\"",
		Status:   "",
	}
	out := toolRow(msg, 80, false)
	if !strings.Contains(out, "withheld") {
		t.Fatalf("expected withheld status, got:\n%s", out)
	}
}

func TestToolRowEmptyContentNoBody(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"echo hi"}`,
		Content:  "",
		Status:   "",
	}
	out := toolRow(msg, 80, false)
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("expected single-line row when content empty, got %d lines:\n%s", strings.Count(out, "\n")+1, out)
	}
}

func TestToolRowFallsBackToRawArgsWhenInvalidJSON(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `not json`,
		Content:  "ok",
		Status:   "✓",
	}
	out := toolRow(msg, 80, false)
	if !strings.Contains(out, "not json") {
		t.Fatalf("expected raw args fallback, got:\n%s", out)
	}
}

func TestMessageListRespectsExpandAll(t *testing.T) {
	lines := []string{"one", "two", "three", "four", "five"}
	list := MessageList{
		Messages: []Message{{Role: "assistant", Content: strings.Join(lines, "\n")}},
		Width:    40,
	}
	collapsed := list.View()
	if strings.Contains(collapsed, "five") {
		t.Fatalf("collapsed list should not show hidden lines")
	}

	list.ExpandAll = true
	expanded := list.View()
	if !strings.Contains(expanded, "five") {
		t.Fatalf("expanded list should show all lines, got:\n%s", expanded)
	}
}

func TestToolResultIsError(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"Bash with exit status", "exit status 1", true},
		{"Bash success", "hello", false},
		{" withheld", "tool result withheld: unsafe", true},
		{"Read withheld", "tool result withheld: permission denied", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toolResultIsError("Bash", tc.content)
			if got != tc.want {
				t.Fatalf("toolResultIsError(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestFormatToolInvocation(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args string
		want string
	}{
		{"Bash command", "Bash", `{"command":"git status"}`, "git status"},
		{"Read path", "Read", `{"path":"foo.go"}`, "foo.go"},
		{"Grep pattern", "Grep", `{"pattern":"TODO"}`, "TODO"},
		{"Glob pattern", "Glob", `{"pattern":"*.go"}`, "*.go"},
		{"WebSearch query", "WebSearch", `{"query":"golang"}`, "golang"},
		{"WebFetch url", "WebFetch", `{"url":"https://example.com"}`, "https://example.com"},
		{"Unknown args", "Other", `{"foo":"bar"}`, "bar"},
		{"Invalid JSON", "Bash", `not json`, "not json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatToolInvocation(tc.tool, tc.args)
			if got != tc.want {
				t.Fatalf("formatToolInvocation(%q, %q) = %q, want %q", tc.tool, tc.args, got, tc.want)
			}
		})
	}
}
