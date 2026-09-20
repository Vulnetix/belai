package components

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestTurnPanelRendersAssistantContent(t *testing.T) {
	msg := Message{Role: "assistant", Content: "hello world"}
	out, _ := turnPanel(msg, 40, false)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("expected content in panel, got:\n%s", out)
	}
	if !strings.Contains(out, "model") {
		t.Fatalf("expected title in panel, got:\n%s", out)
	}
}

func TestTurnPanelTruncatesLongAssistantContent(t *testing.T) {
	msg := Message{Role: "assistant", Content: "one\n\ntwo\n\nthree\n\nfour\n\nfive"}
	out, _ := turnPanel(msg, 40, false)
	for _, want := range []string{"one", "two", "three", "four"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in truncated panel, got:\n%s", want, out)
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
	out, _ := turnPanel(msg, 40, true)
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
	out, _ := turnPanel(msg, 40, false)
	if !strings.Contains(out, "model") {
		t.Fatalf("expected empty panel to render title, got:\n%s", out)
	}
}

func TestTurnPanelUserTitle(t *testing.T) {
	msg := Message{Role: "user", Content: "hi"}
	out, _ := turnPanel(msg, 40, false)
	if !strings.Contains(out, "user prompt") {
		t.Fatalf("expected user prompt title, got:\n%s", out)
	}
}

func TestTurnPanelSteeringTitle(t *testing.T) {
	msg := Message{Role: "user", Content: "keep going", Steering: true}
	out, _ := turnPanel(msg, 40, false)
	if !strings.Contains(out, "user steering") {
		t.Fatalf("expected user steering title, got:\n%s", out)
	}
}

func TestTurnPanelEmptyWithToolCallsRendersSummary(t *testing.T) {
	msg := Message{
		Role:    "assistant",
		Content: "",
		ToolCalls: []AgentToolCall{
			{ID: "1", Name: "Grep"},
			{ID: "2", Name: "Read"},
			{ID: "3", Name: "Grep"},
		},
	}
	out, _ := turnPanel(msg, 60, false)
	if !strings.Contains(out, "requested 2 tools") {
		t.Fatalf("expected tool summary, got:\n%s", out)
	}
	if !strings.Contains(out, "Grep, Read") {
		t.Fatalf("expected deduped tool names, got:\n%s", out)
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
	out, _ := toolRow(msg, 60, false)
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
	out, _ := toolRow(msg, 80, false)
	if !strings.Contains(out, "internal/foo.go") {
		t.Fatalf("expected path argument, got:\n%s", out)
	}
}

// TestToolRowBashPreviewIsTailAnchored pins the collapsed shape of a Bash row:
// the last three lines, because that is where a command's verdict is, and a
// hint above them for what scrolled past.
func TestToolRowBashPreviewIsTailAnchored(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Bash",
		ToolArgs: `{"command":"seq 6"}`,
		Content:  "1\n2\n3\n4\n5\n6",
		Status:   "✓",
	}
	out, lm := toolRow(msg, 80, false)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("want header + hint + 3 tail lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "3 earlier lines") {
		t.Fatalf("want the hint above the tail, got:\n%s", out)
	}
	for i, want := range []string{"4", "5", "6"} {
		if strings.TrimSpace(lines[2+i]) != want {
			t.Fatalf("line %d = %q, want %q:\n%s", 2+i, lines[2+i], want, out)
		}
	}
	// Selecting the hint must still copy everything it stands for.
	var hidden string
	for _, sl := range lm {
		if sl.MarkerWidth > 0 {
			hidden = sl.Hidden
		}
	}
	if hidden != "1\n2\n3" {
		t.Fatalf("hint hides %q, want the three earlier lines", hidden)
	}
}

// TestToolRowReadPreviewIsHeadAnchored: a file's head is what identifies it,
// so Read truncates from the bottom and puts its hint below.
func TestToolRowReadPreviewIsHeadAnchored(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"path":"main.go"}`,
		Content:  "one\ntwo\nthree\nfour\nfive",
		Status:   "✓",
	}
	out, _ := toolRow(msg, 80, false)
	lines := strings.Split(out, "\n")
	if len(lines) != 4 {
		t.Fatalf("want header + 3 head lines, got %d:\n%s", len(lines), out)
	}
	// Read rows are numbered source, so each line carries its file line number.
	for i, want := range []string{"1 one", "2 two"} {
		if strings.TrimSpace(lines[1+i]) != want {
			t.Fatalf("line %d = %q, want %q:\n%s", 1+i, lines[1+i], want, out)
		}
	}
	if !strings.Contains(lines[3], "2 more lines") {
		t.Fatalf("want the hint on the last shown line, got:\n%s", out)
	}
}

// TestReadRowOmitsNumbersOnPartialRead: Read's offset is a byte count, so a
// partial read gives no way to know which line it landed on. Numbering it
// anyway would print confident, wrong line numbers.
func TestReadRowOmitsNumbersOnPartialReadWithoutMeta(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"path":"main.go","offset":4096}`,
		Content:  "one\ntwo\nthree",
		Status:   "✓",
	}
	out, _ := toolRow(msg, 80, true)
	for _, line := range strings.Split(out, "\n")[1:] {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "1 ") {
			t.Fatalf("partial read without meta must not be numbered:\n%s", out)
		}
	}
	if !strings.Contains(out, "one") {
		t.Fatalf("content missing:\n%s", out)
	}
}

func TestReadRowNumbersPartialReadWithMetaStartLine(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Read",
		ToolArgs: `{"path":"main.go","offset":4096}`,
		Content:  "one\ntwo\nthree",
		Status:   "✓",
		Meta:     map[string]any{"start_line": 42},
	}
	out, _ := toolRow(msg, 80, true)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "42 one") {
		t.Fatalf("expected line 42 to be numbered:\n%s", plain)
	}
	if !strings.Contains(plain, "43 two") {
		t.Fatalf("expected line 43 to be numbered:\n%s", plain)
	}
	if !strings.Contains(plain, "44 three") {
		t.Fatalf("expected line 44 to be numbered:\n%s", plain)
	}
}

// TestReadRowGutterIsStableAcrossExpansion: the gutter is sized for the whole
// file, so expanding a row must not shift the code sideways.
func TestReadRowGutterIsStableAcrossExpansion(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 120; i++ {
		b.WriteString("line\n")
	}
	msg := Message{
		Role: "tool", ToolName: "Read", ToolArgs: `{"path":"main.go"}`,
		Content: strings.TrimRight(b.String(), "\n"), Status: "✓",
	}

	collapsed, _ := toolRow(msg, 80, false)
	expanded, _ := toolRow(msg, 80, true)

	col := func(out string) int {
		line := strings.Split(out, "\n")[1]
		return strings.Index(line, "line")
	}
	if col(collapsed) != col(expanded) {
		t.Fatalf("code column moved on expand: %d then %d\n%s\n---\n%s",
			col(collapsed), col(expanded), collapsed, expanded)
	}
}

// TestToolRowOtherToolsStayAtOneLine: a Grep or Glob result is already a
// summary, so it keeps the single-line preview.
func TestToolRowOtherToolsStayAtOneLine(t *testing.T) {
	msg := Message{
		Role:     "tool",
		ToolName: "Grep",
		ToolArgs: `{"pattern":"func"}`,
		Content:  "a.go:1:func a\nb.go:2:func b\nc.go:3:func c",
		Status:   "✓",
	}
	out, _ := toolRow(msg, 80, false)
	if lines := strings.Split(out, "\n"); len(lines) != 2 {
		t.Fatalf("want header + 1 preview line, got %d:\n%s", len(lines), out)
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
	out, _ := toolRow(msg, 80, true)
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
	out, _ := toolRow(msg, 80, false)
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
	out, _ := toolRow(msg, 80, false)
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
	out, _ := toolRow(msg, 80, false)
	if strings.Count(out, "\n") != 0 {
		t.Fatalf("expected single-line row when content empty, got %d lines:\n%s", strings.Count(out, "\n")+1, out)
	}
}

// TestToolRowRunningElapsed pins the live-elapsed status: a tool that has
// started but produced no result yet shows a running elapsed label, not the
// premature ✓ that would otherwise appear.
func TestToolRowRunningElapsed(t *testing.T) {
	msg := Message{
		Role:      "tool",
		ToolName:  "Read",
		ToolArgs:  `{"path":"x"}`,
		Content:   "",
		Status:    "",
		StartedAt: time.Now().Add(-1300 * time.Millisecond),
	}
	out, _ := toolRow(msg, 80, false)
	if !strings.Contains(out, "· 1.3s") {
		t.Fatalf("expected running elapsed '· 1.3s', got:\n%s", out)
	}
	if strings.Contains(out, "✓") {
		t.Fatalf("running tool must not show ✓:\n%s", out)
	}
}

// TestToolRowDoneIgnoresStartedAt pins that a completed tool shows its real
// status (here ✓), not the stale running elapsed label.
func TestToolRowDoneIgnoresStartedAt(t *testing.T) {
	msg := Message{
		Role:      "tool",
		ToolName:  "Read",
		ToolArgs:  `{"path":"x"}`,
		Content:   "contents",
		Status:    "",
		StartedAt: time.Now().Add(-500 * time.Millisecond),
	}
	out, _ := toolRow(msg, 80, false)
	if !strings.Contains(out, "✓") {
		t.Fatalf("completed tool should show ✓:\n%s", out)
	}
	if strings.Contains(out, "· ") {
		t.Fatalf("completed tool must not show elapsed:\n%s", out)
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
	out, _ := toolRow(msg, 80, false)
	if !strings.Contains(out, "not json") {
		t.Fatalf("expected raw args fallback, got:\n%s", out)
	}
}

func TestMessageListRespectsExpandAll(t *testing.T) {
	list := MessageList{
		Messages: []Message{{Role: "assistant", Content: "one\n\ntwo\n\nthree\n\nfour\n\nfive"}},
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
		{"Write path", "Write", `{"path":"x.go","content":"package x"}`, "x.go"},
		{"Edit path", "Edit", `{"path":"x.go","old_string":"a","new_string":"b"}`, "x.go"},
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

// TestFormatToolInvocationNeverPreviewsWriteContent pins the preview key order:
// a Write or Edit row must preview the path, never the entire file body.
func TestFormatToolInvocationNeverPreviewsWriteContent(t *testing.T) {
	for _, name := range []string{"Write", "Edit"} {
		got := formatToolInvocation(name, `{"path":"x.go","content":"package x"}`)
		if got != "x.go" {
			t.Fatalf("formatToolInvocation(%q) = %q, want path x.go", name, got)
		}
		if strings.Contains(got, "package x") {
			t.Fatalf("formatToolInvocation(%q) leaked file content: %q", name, got)
		}
	}
}

func TestMessageListSkipsEmptyAssistantFrame(t *testing.T) {
	list := MessageList{
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: ""},
			{Role: "system", Content: "done"},
		},
		Width:     60,
		ShowTools: true,
	}
	out := list.View()
	if strings.Contains(out, "model") {
		t.Fatalf("empty assistant frame should be skipped, got:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatalf("system row should still render, got:\n%s", out)
	}
}

func TestMessageListNoBlankLineBetweenToolRows(t *testing.T) {
	tool := Message{Role: "tool", ToolName: "Grep", ToolArgs: `{"pattern":"x"}`, Status: "✓"}
	list := MessageList{
		Messages:  []Message{tool, tool},
		Width:     60,
		ShowTools: true,
	}
	out := list.View()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// A blank line between two tool rows would surface as an empty line; the
	// two rows must be on adjacent lines.
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("expected no blank line between tool rows, got:\n%s", out)
		}
	}
}

func TestMessageListReasoningPanelGated(t *testing.T) {
	list := MessageList{
		Messages: []Message{{Role: "reasoning", Content: "private thought"}},
		Width:    60,
	}
	if strings.Contains(list.View(), "private thought") {
		t.Fatalf("reasoning must be hidden by default")
	}
	list.ShowReasoning = true
	if !strings.Contains(list.View(), "private thought") {
		t.Fatalf("reasoning should render when enabled")
	}
	if !strings.Contains(list.View(), "model · reasoning") {
		t.Fatalf("reasoning panel title missing")
	}
}

// TestMessageListCoalescesAdjacentSystemNotices pins the signet group: two
// adjacent system notices render in one signet panel, one line per notice,
// and never as separate rows.
func TestMessageListCoalescesAdjacentSystemNotices(t *testing.T) {
	list := MessageList{
		Width: 60,
		Messages: []Message{
			{Role: "system", Content: "first notice"},
			{Role: "system", Content: "second notice"},
		},
	}
	out := list.View()
	if strings.Count(out, "signet") != 1 {
		t.Fatalf("want exactly one signet title for the group, got:\n%s", out)
	}
	if !strings.Contains(out, "first notice") || !strings.Contains(out, "second notice") {
		t.Fatalf("both notices should render in the panel:\n%s", out)
	}
}

// TestSignetPanelTruncatesLongGroup pins the deliberate change from the old
// untruncated system rows: more than signetPreviewLines notices collapse to a
// hint whose selection copies the hidden notices.
func TestSignetPanelTruncatesLongGroup(t *testing.T) {
	var msgs []Message
	for i := 0; i < signetPreviewLines+2; i++ {
		msgs = append(msgs, Message{Role: "system", Content: "notice " + strconv.Itoa(i)})
	}
	idxs := make([]int, len(msgs))
	for i := range idxs {
		idxs[i] = i
	}
	s, lm, owners := signetPanel(msgs, idxs, 60, false)
	if !strings.Contains(s, "2 more lines") {
		t.Fatalf("expected truncation hint, got:\n%s", s)
	}
	if strings.Contains(s, "notice 6") || strings.Contains(s, "notice 7") {
		t.Fatalf("hidden notices should not render, got:\n%s", s)
	}
	var hidden string
	for _, sl := range lm {
		if sl.MarkerWidth > 0 {
			hidden = sl.Hidden
		}
	}
	if hidden != "notice 6\nnotice 7" {
		t.Fatalf("hidden = %q, want the two hidden notices", hidden)
	}
	nonChrome := 0
	for _, sl := range lm {
		if !sl.Chrome {
			nonChrome++
		}
	}
	if len(owners) != nonChrome {
		t.Fatalf("owners %d != non-chrome body lines %d", len(owners), nonChrome)
	}
}

func TestMessageListShowToolsGated(t *testing.T) {
	tool := Message{Role: "tool", ToolName: "Grep", ToolArgs: `{"pattern":"x"}`, Status: "✓"}
	list := MessageList{Messages: []Message{tool}, Width: 60}
	if strings.Contains(list.View(), "Grep") {
		t.Fatalf("tool rows must be hidden when ShowTools is false")
	}
	list.ShowTools = true
	if !strings.Contains(list.View(), "Grep") {
		t.Fatalf("tool rows should render when ShowTools is true")
	}
}

// The whole selection feature rests on this: one content line maps 1:1 to one
// screen row, so the map length must always equal the frame's line count.
func TestMessageListRenderMapMatchesFrameLineCount(t *testing.T) {
	cases := []struct {
		name string
		list MessageList
	}{
		{"empty", MessageList{Width: 60}},
		{
			name: "turns tools and system rows",
			list: MessageList{
				Width:     60,
				ShowTools: true,
				Messages: []Message{
					{Role: "user", Content: "do the thing"},
					{Role: "assistant", Content: "on it", ToolCalls: []AgentToolCall{{Name: "Read", Args: `{"path":"a.txt"}`}}},
					{Role: "tool", ToolName: "Read", Content: "line one\nline two\nline three"},
					{Role: "system", Content: "a notice"},
					{Role: "assistant", Content: "done"},
				},
			},
		},
		{
			name: "long body truncated",
			list: MessageList{
				Width:    60,
				Messages: []Message{{Role: "assistant", Content: strings.Repeat("a line\n", 40)}},
			},
		},
		{
			name: "reasoning shown",
			list: MessageList{
				Width:         60,
				ShowReasoning: true,
				Messages: []Message{
					{Role: "reasoning", Content: "thinking about it"},
					{Role: "assistant", Content: "answer"},
				},
			},
		},
		{
			name: "expanded",
			list: MessageList{
				Width:     60,
				ShowTools: true,
				ExpandAll: true,
				Messages: []Message{
					{Role: "assistant", Content: strings.Repeat("a line\n", 40)},
					{Role: "tool", ToolName: "Bash", Content: strings.Repeat("out\n", 40)},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, lm := tc.list.Render()
			if out != tc.list.View() {
				t.Fatal("View and Render disagree on the rendered text")
			}
			want := strings.Count(out, "\n") + 1
			if out == "" {
				want = 0
			}
			if len(lm) != want {
				t.Fatalf("map has %d entries for %d lines", len(lm), want)
			}
		})
	}
}

func TestMessageListRenderMapRecoversCleanText(t *testing.T) {
	list := MessageList{
		Width:     60,
		ShowTools: true,
		Messages: []Message{
			{Role: "assistant", Content: "hello world"},
			{Role: "system", Content: "a notice"},
		},
	}

	out, lm := list.Render()
	lines := strings.Split(out, "\n")

	var found bool
	for i, sl := range lm {
		if sl.Chrome || sl.Width == 0 {
			continue
		}
		if cut := ansi.Cut(ansi.Strip(lines[i]), sl.Col, sl.Col+sl.Width); cut != sl.Text {
			t.Fatalf("line %d: cut %q != mapped %q", i, cut, sl.Text)
		}
		if strings.Contains(sl.Text, "hello world") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no mapped line carried the assistant text:\n%s", out)
	}
}

func TestMessageListRenderMarksTruncationMarker(t *testing.T) {
	list := MessageList{
		Width:    60,
		Messages: []Message{{Role: "assistant", Content: strings.Repeat("a line\n", 40)}},
	}

	_, lm := list.Render()
	var markers int
	for _, sl := range lm {
		if sl.MarkerWidth > 0 {
			markers++
			if sl.Hidden == "" {
				t.Fatal("a marker line must carry the text it hides")
			}
		}
	}
	if markers != 1 {
		t.Fatalf("%d marker lines in a truncated turn, want 1", markers)
	}
}

func TestMessageStreamingBuffer(t *testing.T) {
	var m Message

	// AppendText moves an existing prefix into the buffer and accumulates.
	m.Content = "pre"
	m.AppendText("fix")
	if got := m.Text(); got != "prefix" {
		t.Fatalf("Text after prefix append = %q, want prefix", got)
	}
	if m.Content != "" {
		t.Fatalf("Content should stay empty while streaming, got %q", m.Content)
	}

	// Further deltas accumulate in O(1) amortised, and empty deltas are no-ops.
	m.AppendText("")
	m.AppendText(" is streaming")
	if got := m.Text(); got != "prefix is streaming" {
		t.Fatalf("Text = %q", got)
	}

	// SetContent replaces the whole value and drops the buffer.
	m.SetContent("final")
	if got := m.Text(); got != "final" {
		t.Fatalf("Text after SetContent = %q", got)
	}
	if m.buf != nil {
		t.Fatalf("SetContent must drop the buffer")
	}

	// Materialise flushes the buffer back into Content.
	m.AppendText("again")
	m.Materialise()
	if m.Content != "finalagain" || m.buf != nil {
		t.Fatalf("Materialise = %q buf=%v, want finalagain nil", m.Content, m.buf)
	}
}

func TestRenderMemoisesAndInvalidates(t *testing.T) {
	ml := mixedTranscript()
	ml.Width = 80

	first, _ := ml.Render()
	if ml.Messages[0].rc.text == "" {
		t.Fatalf("unchanged message should be memoised after render")
	}

	second, _ := ml.Render()
	if first != second {
		t.Fatalf("identical render differs on second pass:\n%s\n---\n%s", first, second)
	}

	// A content mutation clears the memo, so the next render reflects it.
	ml.Messages[0].AppendText(" (updated)")
	if ml.Messages[0].rc.text != "" {
		t.Fatalf("AppendText must clear the render cache, got %q", ml.Messages[0].rc.text)
	}
	third, _ := ml.Render()
	if third == first {
		t.Fatalf("render did not change after AppendText")
	}
}

func TestRunningToolRowNotMemoised(t *testing.T) {
	ml := MessageList{
		Width:     80,
		ShowTools: true,
		Messages: []Message{
			{Role: "tool", ToolName: "Bash", ToolArgs: `{"command":"sleep 10"}`, StartedAt: time.Now().Add(-time.Second)},
		},
	}
	ml.Render()
	if ml.Messages[0].rc.text != "" {
		t.Fatalf("running tool row must re-render every frame (live elapsed), got cached %q", ml.Messages[0].rc.text)
	}
}

func TestTagProvenanceSetsCopyable(t *testing.T) {
	textMsg := Message{Role: "assistant", Content: "hello"}
	lm := LineMap{{Text: "hello", Col: 0, Width: 5}}
	tagProvenance(lm, 0, textMsg)
	if !lm[0].Copyable {
		t.Fatal("a text-bearing message must mark its lines copyable")
	}

	emptyMsg := Message{Role: "tool", ToolName: "Read"}
	lm2 := LineMap{{Text: "", Col: 0, Width: 2}}
	tagProvenance(lm2, 0, emptyMsg)
	if lm2[0].Copyable {
		t.Fatal("an empty message must not mark its lines copyable")
	}
}

// TestMessageListStreamingAssistantHoistsPrecedingSignet pins the rule that
// a signet notice emitted before the model panel starts streaming must not
// interrupt the model panel; it is hoisted to render after the streaming
// assistant so the panel can keep streaming characters.
func TestMessageListStreamingAssistantHoistsPrecedingSignet(t *testing.T) {
	streaming := Message{Role: "assistant"}
	streaming.AppendText("hello")
	if !streaming.IsStreaming() {
		t.Fatal("test setup should produce a streaming assistant")
	}

	list := MessageList{
		Width:    60,
		Messages: []Message{{Role: "system", Content: "classifying"}, streaming},
	}
	out := list.View()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var modelIdx, signetIdx int
	for i, l := range lines {
		if strings.Contains(l, "model") {
			modelIdx = i
		}
		if strings.Contains(l, "signet") {
			signetIdx = i
		}
	}
	if modelIdx == 0 && signetIdx == 0 {
		t.Fatalf("could not locate model and signet panels in:\n%s", out)
	}
	if signetIdx < modelIdx {
		t.Fatalf("signet panel should render after the streaming model panel, got:\n%s", out)
	}
	if !strings.Contains(out, "classifying") {
		t.Fatalf("signet notice should still render, got:\n%s", out)
	}
}

// TestMessageListStreamingSignetAfterModel keeps the normal order when the
// signet notice already follows the streaming model panel in the transcript.
func TestMessageListStreamingSignetAfterModel(t *testing.T) {
	streaming := Message{Role: "assistant"}
	streaming.AppendText("hello")
	list := MessageList{
		Width:    60,
		Messages: []Message{streaming, {Role: "system", Content: "warning"}},
	}
	out := list.View()
	if !strings.Contains(out, "hello") || !strings.Contains(out, "warning") {
		t.Fatalf("both model content and notice should render, got:\n%s", out)
	}
	modelLines := strings.Count(out, "model")
	signetLines := strings.Count(out, "signet")
	if modelLines < 1 || signetLines != 1 {
		t.Fatalf("expected one streaming model panel and one signet panel, got model=%d signet=%d:\n%s", modelLines, signetLines, out)
	}
}

// TestMessageListStreamingCoalescesPhaseSignets checks that notices both
// before and after a streaming model panel are gathered into one trailing
// signet panel rather than splitting them around the model panel.
func TestMessageListStreamingCoalescesPhaseSignets(t *testing.T) {
	streaming := Message{Role: "assistant"}
	streaming.AppendText("hello")
	list := MessageList{
		Width: 60,
		Messages: []Message{
			{Role: "system", Content: "first notice"},
			streaming,
			{Role: "system", Content: "second notice"},
		},
	}
	out := list.View()
	if strings.Count(out, "signet") != 1 {
		t.Fatalf("want exactly one trailing signet panel, got:\n%s", out)
	}
	if !strings.Contains(out, "first notice") || !strings.Contains(out, "second notice") {
		t.Fatalf("both notices should render in the trailing signet panel, got:\n%s", out)
	}
}

// TestMessageListRetrySignetsFollowStreamingModel pins the retry case: a
// warning before the fresh streaming assistant and a retry notice after the
// fresh streaming assistant both end up in one signet panel after the model
// panel that is still streaming.
func TestMessageListRetrySignetsFollowStreamingModel(t *testing.T) {
	partial := Message{Role: "assistant", Content: "partial output", Partial: true}
	retrying := Message{Role: "assistant"}
	retrying.AppendText("retry output")
	list := MessageList{
		Width: 60,
		Messages: []Message{
			partial,
			{Role: "system", Content: "retrying (1/10) after 800ms"},
			retrying,
			{Role: "system", Content: "retrying (2/10) after 1.2s"},
		},
	}
	out := list.View()
	if strings.Count(out, "model") != 2 {
		t.Fatalf("expected two model panels, got:\n%s", out)
	}
	if strings.Count(out, "signet") != 1 {
		t.Fatalf("expected one trailing signet panel for retry notices, got:\n%s", out)
	}
	// The signet panel must come after both model panels.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	lastModel, lastSignet := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "model") {
			lastModel = i
		}
		if strings.Contains(l, "signet") {
			lastSignet = i
		}
	}
	if lastSignet < lastModel {
		t.Fatalf("signet panel should follow the last model panel, got:\n%s", out)
	}
}

// TestMessageListCompletedAssistantKeepsPrecedingSignet ensures the hoisting
// only applies while a model panel is actively streaming; once the assistant
// has materialised, a preceding signet notice stays where it was.
func TestMessageListCompletedAssistantKeepsPrecedingSignet(t *testing.T) {
	list := MessageList{
		Width: 60,
		Messages: []Message{
			{Role: "system", Content: "classified"},
			{Role: "assistant", Content: "hello"},
		},
	}
	out := list.View()
	if strings.Count(out, "signet") != 1 {
		t.Fatalf("expected one signet panel, got:\n%s", out)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	var signetIdx, modelIdx int
	for i, l := range lines {
		if strings.Contains(l, "signet") {
			signetIdx = i
		}
		if strings.Contains(l, "model") {
			modelIdx = i
		}
	}
	if signetIdx > modelIdx {
		t.Fatalf("preceding signet should stay before a completed assistant, got:\n%s", out)
	}
}
