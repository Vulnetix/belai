// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/vulnetix/signet/internal/transcript"
)

// AgentToolCall records a tool call that belongs to an assistant turn. The
// raw JSON arguments are kept as text so this package stays UI-only; the
// caller parses JSON when rebuilding provider turns.
type AgentToolCall struct {
	ID   string
	Name string
	Args string // raw JSON text
}

// Message is one message in the transcript.
type Message struct {
	Role     string // user, assistant, tool, system
	Content  string
	Usage    *transcript.Usage // non-nil on metered assistant turns
	ToolName string            // set on tool turns
	ToolArgs string            // set on tool turns
	Status   string            // set on tool turns (✓, withheld, …)

	// Expanded overrides global truncation for this message.
	Expanded bool

	// Partial is set on assistant bubbles that belong to a turn that failed
	// and is being retried. They are dimmed and skipped when rebuilding the
	// provider-facing transcript.
	Partial bool

	// ToolCallID is set on tool turns. It groups a result turn back to the
	// assistant call that requested it.
	ToolCallID string

	// StartedAt marks when a tool call began executing (set on tool turns when
	// the start event lands, before any result). A non-zero value with empty
	// Content means the tool is still running, so the row shows a live elapsed
	// time instead of a ✓ status. Zero means unknown/not running.
	StartedAt time.Time

	// ToolCalls records the calls requested by an assistant turn. It is only
	// meaningful when Role == "assistant"; it lets buildTurns preserve the
	// tool-call metadata across rounds.
	ToolCalls []AgentToolCall

	// Steering marks a user turn injected mid-loop while the agent is running.
	Steering bool

	// buf accumulates streamed deltas for an in-flight message. Content stays
	// empty while buf is live; Text() materialises on read without a copy, so
	// appending one delta is O(1) amortised instead of the O(n²) of
	// Content += delta over a long reply. It is written and read only on the
	// Bubble Tea goroutine.
	buf *strings.Builder

	// rc memoises the last rendered text and line map for this message. The
	// key covers every field that affects the render, so any change (a
	// streaming tail, an appended tool call, a new status, a width change)
	// misses and re-renders. AppendText/SetContent/Materialise clear it.
	rc renderCache
}

// renderKey identifies everything that affects one message's rendered output.
// Content is keyed by length, not value: the only content mutators
// (AppendText/SetContent) clear the cache, so on an otherwise-unchanged
// message a length match means unchanged content. Comparing a length is O(1)
// where comparing a 200 KiB tool result is O(n) — the point of the memo.
type renderKey struct {
	role       string
	contentLen int
	toolName   string
	toolArgs   string
	status     string
	width      int
	expand     bool
	expanded   bool
	partial    bool
	steering   bool
	usageTotal int
	toolCallsN int
	// started marks a running tool row: its live elapsed label changes every
	// frame, so it is never cached.
	started bool
}

// renderCache is the memoised render of one message.
type renderCache struct {
	key  renderKey
	text string
	lm   LineMap
}

// renderKeyFor computes the cache key for one message at a given width.
func renderKeyFor(m *Message, width int, expandAll bool) renderKey {
	running := m.Role == "tool" && !m.StartedAt.IsZero() && m.Text() == ""
	usage := 0
	if m.Usage != nil {
		usage = m.Usage.Total()
	}
	return renderKey{
		role:       m.Role,
		contentLen: len(m.Text()),
		toolName:   m.ToolName,
		toolArgs:   m.ToolArgs,
		status:     m.Status,
		width:      width,
		expand:     expandAll,
		expanded:   m.Expanded,
		partial:    m.Partial,
		steering:   m.Steering,
		usageTotal: usage,
		toolCallsN: len(m.ToolCalls),
		started:    running,
	}
}

// AppendText appends a streamed delta to an in-flight message. Any existing
// Content is moved into the buffer first so a message can start with a
// non-streamed prefix and then receive deltas.
func (m *Message) AppendText(s string) {
	if s == "" {
		return
	}
	m.rc = renderCache{}
	if m.buf == nil {
		m.buf = &strings.Builder{}
		if m.Content != "" {
			m.buf.WriteString(m.Content)
			m.Content = ""
		}
	}
	m.buf.WriteString(s)
}

// Text returns the message content, materialising from the streaming buffer
// when one is live. The returned string is not copied when it comes from a
// builder, so callers must not mutate it.
func (m Message) Text() string {
	if m.buf != nil {
		return m.buf.String()
	}
	return m.Content
}

// SetContent replaces the whole content and drops any streaming buffer.
func (m *Message) SetContent(s string) {
	m.Content = s
	m.buf = nil
	m.rc = renderCache{}
}

// Materialise flushes the streaming buffer into Content and drops it, so the
// message is a plain value again (used when a turn ends).
func (m *Message) Materialise() {
	if m.buf != nil {
		m.Content = m.buf.String()
		m.buf = nil
		m.rc = renderCache{}
	}
}

// MessageList renders the transcript.
type MessageList struct {
	Messages  []Message
	Width     int
	ExpandAll bool // when true, render every message in full

	// ShowReasoning and ShowTools gate the dim reasoning panel and tool rows,
	// mirroring the ctrl+r / ctrl+t toggles resolved by the caller.
	ShowReasoning bool
	ShowTools     bool
}

const (
	messageMinWidth       = 32
	assistantPreviewLines = 4
	toolPreviewLines      = 1
)

// View renders the transcript: conversational turns as flat titled panels,
// tool calls and system notices as single-line rows between them. Empty
// assistant/user frames with no tool calls are skipped so a tool-calls-only
// turn never renders a bare box. Framed panels are separated by a blank line;
// consecutive flat rows sit on adjacent lines.
func (m MessageList) View() string {
	s, _ := m.Render()
	return s
}

// Render renders the transcript and returns the per-line provenance of every
// row it emits. The text is byte-identical to what View returns today; the
// map is the side channel drag-selection uses for hit-testing and copying.
//
// Per-message output is memoised: a message whose cache key is unchanged
// reuses its rendered text and line map instead of re-splitting and
// re-joining its (possibly large) content. The streaming tail and running tool
// rows miss on every frame and re-render; everything else renders once per
// change.
func (m MessageList) Render() (string, LineMap) {
	width := max(m.Width, messageMinWidth)

	type entry struct {
		idx    int
		framed bool
	}
	var entries []entry
	for i := range m.Messages {
		msg := &m.Messages[i]
		switch msg.Role {
		case "reasoning":
			if !m.ShowReasoning {
				continue
			}
			entries = append(entries, entry{i, true})
		case "tool":
			if !m.ShowTools {
				continue
			}
			entries = append(entries, entry{i, false})
		case "system":
			entries = append(entries, entry{i, false})
		default:
			if strings.TrimSpace(msg.Text()) == "" && len(msg.ToolCalls) == 0 {
				continue
			}
			entries = append(entries, entry{i, true})
		}
	}

	var b strings.Builder
	var lm LineMap
	for i, e := range entries {
		msg := &m.Messages[e.idx]
		key := renderKeyFor(msg, width, m.ExpandAll)
		var s string
		var sub LineMap
		if !key.started && msg.rc.key == key {
			s, sub = msg.rc.text, msg.rc.lm
		} else {
			switch msg.Role {
			case "tool":
				s, sub = toolRow(*msg, width, m.ExpandAll)
			case "system":
				s, sub = systemRow(msg.Text(), width)
			case "reasoning":
				s, sub = reasoningPanel(*msg, width, m.ExpandAll)
			default:
				s, sub = turnPanel(*msg, width, m.ExpandAll)
			}
			if !key.started {
				msg.rc = renderCache{key: key, text: s, lm: sub}
			}
		}
		b.WriteString(s)
		lm = append(lm, sub...)
		if i == len(entries)-1 {
			break
		}
		if e.framed || entries[i+1].framed {
			b.WriteString("\n\n")
			lm = append(lm, SourceLine{Chrome: true})
		} else {
			b.WriteString("\n")
		}
	}
	return b.String(), lm
}

// reasoningPanel renders streamed chain-of-thought as a dim, unbordered
// sibling of the assistant panel, truncated like any other turn.
func reasoningPanel(msg Message, width int, expandAll bool) (string, LineMap) {
	body := strings.TrimRight(msg.Text(), "\n")
	var marker, hidden string
	if !expandAll && !msg.Expanded {
		body, marker, hidden = truncateBody(body, assistantPreviewLines)
	}
	body = MutedStyle.Render(body)
	return Panel{
		Title:  "reasoning",
		Body:   body,
		Width:  width,
		Accent: lipgloss.TerminalColor(ColorMuted),
		Marker: marker,
		Hidden: hidden,
	}.Render()
}

// turnPanel renders a user or assistant turn. When the turn is longer than
// assistantPreviewLines and the transcript is not expanded, only the first
// few lines are shown with a trailing count of hidden lines.
func turnPanel(msg Message, width int, expandAll bool) (string, LineMap) {
	title, accent := "signet", lipgloss.TerminalColor(ColorTeal)
	if msg.Role == "user" {
		title, accent = "user prompt", lipgloss.TerminalColor(ColorTealSoft)
		if msg.Steering {
			title, accent = "user steering", lipgloss.TerminalColor(ColorAmber)
		}
	} else if msg.Role != "assistant" {
		title, accent = msg.Role, lipgloss.TerminalColor(ColorMuted)
	}

	meta := ""
	if msg.Usage != nil {
		if n := msg.Usage.Total(); n > 0 {
			meta = formatTokens(n) + " tok"
		}
	}
	if msg.Partial {
		meta = "retrying…"
	}

	body := strings.TrimRight(msg.Text(), "\n")
	if strings.TrimSpace(body) == "" && len(msg.ToolCalls) > 0 {
		body = toolCallSummary(msg.ToolCalls, width)
	}
	var marker, hidden string
	if !expandAll && !msg.Expanded {
		body, marker, hidden = truncateBody(body, assistantPreviewLines)
	}
	if msg.Partial {
		body = MutedStyle.Render(body)
		title = MutedStyle.Render(title)
	}

	return Panel{
		Title:  title,
		Meta:   meta,
		Body:   body,
		Width:  width,
		Accent: accent,
		Marker: marker,
		Hidden: hidden,
	}.Render()
}

// toolCallSummary renders a muted one-line substitute for an assistant turn
// whose only output was tool calls. Tool names are deduped, order preserved,
// and the line is truncated to the panel's inner width.
func toolCallSummary(calls []AgentToolCall, width int) string {
	seen := map[string]bool{}
	var names []string
	for _, c := range calls {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	label := "requested " + strconv.Itoa(len(names)) + " tools"
	if len(names) == 0 {
		label = "requested tools"
	}
	line := label + " · " + strings.Join(names, ", ")
	inner := max(width-4, 8)
	return MutedStyle.Render(truncateRunes(line, inner))
}

// truncateBody keeps up to maxLines of body and appends a muted hint when
// content was hidden. It returns the hint's plain text and the hidden
// remainder alongside the rendered body so the caller can hand both to the
// panel: a selection over the hint then copies the full remainder instead of
// the "… N more lines" marker.
func truncateBody(body string, maxLines int) (out, marker, hidden string) {
	if maxLines < 1 {
		return body, "", ""
	}
	lines := strings.Split(body, "\n")
	if len(lines) <= maxLines {
		return body, "", ""
	}
	kept := strings.Join(lines[:maxLines], "\n")
	hidden = strings.Join(lines[maxLines:], "\n")
	marker = "… " + strconv.Itoa(len(lines)-maxLines) + " more lines"
	return kept + "\n" + MutedStyle.Render(marker), marker, hidden
}

// toolRow renders one tool call as a flat row — tool activity is subordinate
// to the turn that caused it, so it never gets a frame of its own. The first
// line shows the tool name, its human-readable invocation, and the status.
// When the result is available, a preview of the first line of stdout/stderr
// is shown beneath; bash errors are rendered in red.
func toolRow(msg Message, width int, expandAll bool) (string, LineMap) {
	isErr := toolResultIsError(msg.ToolName, msg.Text())

	status := strings.TrimSpace(msg.Status)
	if status == "" {
		switch {
		case isErr:
			status = "✗"
		case strings.HasPrefix(msg.Text(), "tool result withheld:"):
			status = "withheld"
		case !msg.StartedAt.IsZero() && msg.Text() == "":
			// Running: show live elapsed time rather than a premature ✓.
			status = "· " + time.Since(msg.StartedAt).Round(100*time.Millisecond).String()
		default:
			status = "✓"
		}
	}

	head := MutedStyle.Render("⌁ " + msg.ToolName)
	plain := "⌁ " + msg.ToolName

	if invocation := formatToolInvocation(msg.ToolName, msg.ToolArgs); invocation != "" {
		head += "  " + MutedStyle.Render(invocation)
		plain += "  " + invocation
	}

	statusLine := alignStatus(head, plain, status, width)

	// The header's map is built from the rendered line, not the logical
	// strings: alignStatus may have truncated the head, and the copyable
	// region is what survives — everything after the "⌁ " prefix, up to the
	// right-aligned status and its padding. The prefix glyph, the padding and
	// the ✓/✗/withheld glyph all stay out of copies.
	prefixCol := visibleLen("⌁ ")
	statusPlain := ansi.Strip(statusLine)
	headEnd := visibleLen(statusPlain)
	if status != "" {
		headEnd -= visibleLen(status)
	}
	if headEnd < prefixCol {
		headEnd = prefixCol
	}
	lm := LineMap{{
		Col:   prefixCol,
		Width: headEnd - prefixCol,
		Text:  ansi.Cut(statusPlain, prefixCol, headEnd),
	}}

	content := strings.TrimRight(msg.Text(), "\n")
	if content == "" {
		return statusLine, lm
	}

	expand := expandAll || msg.Expanded
	var hidden string
	preview := content
	if !expand {
		lines := strings.Split(content, "\n")
		preview = lines[0]
		if len(lines) > toolPreviewLines {
			hidden = strings.Join(lines[toolPreviewLines:], "\n")
			preview += "  " + MutedStyle.Render("… "+strconv.Itoa(len(lines)-toolPreviewLines)+" more lines")
		}
	}
	rendered, contentLm := renderToolContent(preview, width, isErr, hidden)
	return statusLine + "\n" + rendered, append(lm, contentLm...)
}

// alignStatus right-aligns the status on the same line as the tool header,
// truncating the header if necessary.
func alignStatus(head, plain, status string, width int) string {
	if status == "" {
		return truncateLine(head, plain, width)
	}

	pad := width - visibleLen(plain) - visibleLen(status)
	if pad < 1 {
		trimTo := width - visibleLen(status) - 2
		head = truncateLine(head, plain, trimTo)
		plain = truncateRunes(plain, max(trimTo, 1))
		pad = max(width-visibleLen(plain)-visibleLen(status), 1)
	}
	return head + spaces(pad) + statusStyle(status).Render(status)
}

// renderToolContent indents and wraps a tool result line. When hidden is
// non-empty, the preview carries an inline "… N more lines" hint and hidden
// is the remainder it hides; the hint's position is located in the rendered
// line (the wrap decides where it lands — the logical string is not
// consulted), so a selection over it copies the full remainder.
func renderToolContent(content string, width int, isErr bool, hidden string) (string, LineMap) {
	prefix := "  "
	inner := max(width-visibleLen(prefix), 8)
	body := content
	if isErr {
		body = DangerStyle.Render(body)
	}
	rendered := lipgloss.NewStyle().Width(inner).Render(body)

	pcol := visibleLen(prefix)
	plainLines := make([]string, 0)
	for _, line := range strings.Split(rendered, "\n") {
		plainLines = append(plainLines, ansi.Strip(prefix+strings.TrimRight(line, " ")))
	}

	markerLine, markerIdx := -1, -1
	if hidden != "" {
		hint := "… " + strconv.Itoa(strings.Count(hidden, "\n")+1) + " more lines"
		// The hint is what the caller appended at the end, so the last
		// rendered line holding it is the marker's line.
		for i := len(plainLines) - 1; i >= 0; i-- {
			if idx := strings.LastIndex(plainLines[i], hint); idx >= 0 {
				markerLine, markerIdx = i, idx
				break
			}
		}
	}

	var b strings.Builder
	var lm LineMap
	first := true
	for i, line := range plainLines {
		if !first {
			b.WriteString("\n")
		}
		first = false
		b.WriteString(line)
		sl := SourceLine{Col: pcol, Width: visibleLen(line) - pcol, Text: line[pcol:]}
		if i == markerLine && markerIdx >= 0 {
			sl.MarkerCol = pcol + visibleLen(line[:markerIdx])
			sl.MarkerWidth = visibleLen(line) - sl.MarkerCol
			sl.Hidden = hidden
		}
		lm = append(lm, sl)
	}
	return b.String(), lm
}

// formatToolInvocation extracts the most descriptive argument from a tool's
// JSON args for display next to the tool name.
func formatToolInvocation(name, argsJSON string) string {
	argsJSON = strings.TrimSpace(argsJSON)
	if argsJSON == "" {
		return ""
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		s := strings.ReplaceAll(argsJSON, "\n", " ")
		return truncateRunes(s, 60)
	}

	keyOrder := map[string][]string{
		"Bash":      {"command", "cmd"},
		"Read":      {"path", "file"},
		"Grep":      {"pattern", "query"},
		"Glob":      {"pattern", "query"},
		"WebSearch": {"query", "q"},
		"WebFetch":  {"url"},
	}

	keys := keyOrder[name]
	if len(keys) == 0 {
		keys = []string{"command", "path", "pattern", "query", "url", "args"}
	}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return truncateRunes(s, 120)
			}
		}
	}
	for _, v := range args {
		if s, ok := v.(string); ok && s != "" {
			return truncateRunes(s, 120)
		}
	}
	return ""
}

// toolResultIsError reports whether a tool result represents a failure that
// should be highlighted in red. Bash non-zero exits are detected by the
// "exit status" marker in their output; all "tool result withheld" strings
// indicate the tool did not return useful data.
func toolResultIsError(name, content string) bool {
	if strings.HasPrefix(content, "tool result withheld:") {
		return true
	}
	if name == "Bash" && strings.Contains(content, "exit status") {
		return true
	}
	return false
}

// systemRow renders a system notice as a dim, marked line. The "│ " marker
// is two cells, so the selectable text starts at column 2.
func systemRow(content string, width int) (string, LineMap) {
	body := strings.TrimRight(content, "\n")
	marker := MutedStyle.Render("│ ")
	wrapped := lipgloss.NewStyle().Foreground(ColorMuted).Width(max(width-2, 8)).Render(body)

	var b strings.Builder
	var lm LineMap
	mcol := visibleLen("│ ")
	first := true
	for _, line := range strings.Split(wrapped, "\n") {
		line = strings.TrimRight(line, " ")
		if !first {
			b.WriteString("\n")
		}
		first = false
		b.WriteString(marker + line)
		lm = append(lm, SourceLine{Col: mcol, Width: visibleLen(line), Text: line})
	}
	return b.String(), lm
}

func statusStyle(status string) lipgloss.Style {
	switch {
	case strings.Contains(status, "✓"), strings.Contains(status, "ok"):
		return AccentStyle
	case strings.Contains(status, "✗"), strings.Contains(status, "denied"),
		strings.Contains(status, "error"), strings.Contains(status, "withheld"):
		return DangerStyle
	default:
		return MutedStyle
	}
}

// truncateLine clips a styled line whose plain-text twin is `plain`.
func truncateLine(styled, plain string, width int) string {
	if width < 1 || visibleLen(plain) <= width {
		return styled
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(styled)
}
