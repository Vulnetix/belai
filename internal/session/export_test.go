package session

import (
	"strings"
	"testing"
)

func fixedExportEntries() []Entry {
	return []Entry{
		{ID: "m1", Type: EntryTypeSessionMeta, Content: `{"schema":2,"cwd":"/tmp/proj","createdAt":1700000000000}`},
		{ID: "n1", Type: EntryTypeSessionName, Content: "demo session"},
		{ID: "u1", Type: "user", Role: "user", Content: "please build"},
		{ID: "a1", Type: "assistant", Role: "assistant", Content: "I ran a command", Meta: map[string]any{
			"provider":     "openai",
			"model":        "gpt-5",
			"total_tokens": float64(42),
			"tool_calls": []any{map[string]any{
				"name": "Bash",
				"args": `{"command":"echo hi"}`,
			}},
		}},
		{ID: "t1", Type: "tool", Role: "tool", Content: "hello ``` ` world", Meta: map[string]any{
			"tool_name": "Bash",
			"truncated": true,
		}},
		{ID: "s1", Type: "system", Role: "system", Content: "system note"},
		{ID: "sa1", Type: "tool", Role: "tool", Content: "subagent result", SubagentID: "sa-1", Meta: map[string]any{"tool_name": "Read"}},
		{ID: "c1", Type: "summary", Content: "earlier work"},
	}
}

func TestExportMarkdownGolden(t *testing.T) {
	got := ExportMarkdown(fixedExportEntries(), ExportOptions{ID: "sess-1234", MaxToolResultRunes: 10})
	want := "# Session export\n\n" +
		"- name: demo session\n" +
		"- id: sess-1234\n" +
		"- cwd: /tmp/proj\n" +
		"- created: 2023-11-14T22:13:20Z\n" +
		"- providers: openai\n" +
		"- models: gpt-5\n" +
		"- tokens: 42\n\n" +
		"## User\n\n" +
		"please build\n\n" +
		"## Assistant\n\n" +
		"I ran a command\n\n" +
		"### Tool call: Bash\n\n" +
		"````\n" +
		"{\"command\":\"echo hi\"}\n" +
		"````\n\n" +
		"### Tool result: Bash\n\n" +
		"hello ``` … (truncated, 17 chars total)\n\n" +
		"*(tool result was truncated)*\n\n" +
		"> earlier work\n"
	if got != want {
		t.Fatalf("ExportMarkdown mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestExportMarkdownDeterministic(t *testing.T) {
	opts := ExportOptions{ID: "sess-1234", MaxToolResultRunes: 10}
	a := ExportMarkdown(fixedExportEntries(), opts)
	b := ExportMarkdown(fixedExportEntries(), opts)
	if a != b {
		t.Fatalf("two runs differ:\n%s\n---\n%s", a, b)
	}
}

func TestExportMarkdownSkipsSystemSubagentAndState(t *testing.T) {
	got := ExportMarkdown(fixedExportEntries(), ExportOptions{ID: "sess-1234"})
	for _, missing := range []string{"system note", "subagent result", "session_meta"} {
		if strings.Contains(got, missing) {
			t.Fatalf("export should not contain %q:\n%s", missing, got)
		}
	}
	// The session name is in the header, not the body; the body keeps the
	// user/assistant/tool/summary rows.
	for _, present := range []string{"## User", "## Assistant", "### Tool result: Bash", "> earlier work"} {
		if !strings.Contains(got, present) {
			t.Fatalf("export missing %q:\n%s", present, got)
		}
	}
}

func TestExportMarkdownReasoningOptIn(t *testing.T) {
	entries := []Entry{
		{ID: "r1", Type: "reasoning", Role: "reasoning", Content: "private chain of thought"},
	}
	if got := ExportMarkdown(entries, ExportOptions{ID: "x"}); strings.Contains(got, "private chain of thought") {
		t.Fatalf("reasoning must be skipped by default:\n%s", got)
	}
	if got := ExportMarkdown(entries, ExportOptions{ID: "x", IncludeReasoning: true}); !strings.Contains(got, "## Reasoning") || !strings.Contains(got, "private chain of thought") {
		t.Fatalf("reasoning must render when opted in:\n%s", got)
	}
}
