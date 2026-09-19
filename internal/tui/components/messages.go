// Package components holds the Bubble Tea building blocks for the Signet TUI.
package components

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/vulnetix/signet/internal/filediff"
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

	// diff is what a mutating tool changed on disk, observed around the tool
	// rather than returned by it. Render-only: it never reaches buildTurns and
	// so never reaches a model. diffSeq keys the render cache, since the diff
	// arrives after the row already exists.
	diff    *filediff.Change
	diffSeq int

	// progress is a bounded tail of a still-running tool's output, and
	// progressN counts every line ever seen so the row can say how much
	// scrolled past. Only the tail is kept: a running command's earlier output
	// is superseded by the authoritative result that lands when it finishes,
	// so retaining all of it would double the memory for no gain.
	//
	// Written and read only on the Bubble Tea goroutine. Cleared by SetContent
	// when the real result arrives.
	progress  []string
	progressN int

	// Meta carries render-only metadata emitted by the tool (e.g. Read's
	// start_line). It never enters the conversation and keys the render cache.
	Meta map[string]any

	// SubagentID keys a subagent activity row to its subagent ("" for the main
	// thread). Subagent rows are render-only: they carry tool activity forwarded
	// from a subagent, never enter buildTurns, and render with a dim gutter
	// naming the subagent.
	SubagentID string

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
	// diffSeq changes when a diff is attached, which happens after the row has
	// already been rendered once.
	diffSeq int
	// metaLen changes when Meta is attached, so a late-arriving start_line
	// causes a re-render.
	metaLen int
	// subagentID distinguishes subagent activity rows (render-only gutter).
	subagentID string
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
		diffSeq:    m.diffSeq,
		metaLen:    len(m.Meta),
		subagentID: m.SubagentID,
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
//
// The live progress tail goes with it: once the authoritative result is here,
// the partial view of it is noise.
func (m *Message) SetContent(s string) {
	m.Content = s
	m.buf = nil
	m.progress = nil
	m.progressN = 0
	m.rc = renderCache{}
}

// progressRingLines is how much of a running tool's output is kept. It only
// has to cover the largest preview an expanded row will show before the real
// result lands, so a small ring is enough and bounds the memory a runaway
// command can cost.
const progressRingLines = 32

// AppendProgress adds whole lines of live output from a still-running tool,
// keeping only the most recent progressRingLines of them.
//
// s holds one or more newline-separated lines, matching what the agent's
// progress event carries.
func (m *Message) AppendProgress(s string) {
	if s == "" {
		return
	}
	m.rc = renderCache{}
	for _, line := range strings.Split(s, "\n") {
		m.progress = append(m.progress, line)
		m.progressN++
	}
	if n := len(m.progress) - progressRingLines; n > 0 {
		m.progress = append(m.progress[:0], m.progress[n:]...)
	}
}

// ProgressTail returns the last n lines of live output and how many lines came
// before them. The count is what the row's hint reports, and it counts every
// line seen rather than every line kept.
func (m Message) ProgressTail(n int) (lines []string, earlier int) {
	if len(m.progress) == 0 || n <= 0 {
		return nil, 0
	}
	if n >= len(m.progress) {
		return m.progress, m.progressN - len(m.progress)
	}
	return m.progress[len(m.progress)-n:], m.progressN - n
}

// HasProgress reports whether a running tool row has live output to show.
func (m Message) HasProgress() bool { return len(m.progress) > 0 }

// SetDiff attaches what a tool changed on disk.
func (m *Message) SetDiff(c *filediff.Change) {
	m.diff = c
	m.diffSeq++
	m.rc = renderCache{}
}

// Diff returns the attached change, or nil.
func (m Message) Diff() *filediff.Change { return m.diff }

// hasDiff reports whether the row has a change worth drawing.
func (m Message) hasDiff() bool {
	return m.diff != nil && (len(m.diff.Files) > 0 || m.diff.Unavailable != "")
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
		tagProvenance(sub, e.idx, *msg)
		b.WriteString(s)
		lm = append(lm, sub...)
		if i == len(entries)-1 {
			break
		}
		if e.framed || entries[i+1].framed {
			b.WriteString("\n\n")
			lm = append(lm, SourceLine{Chrome: true, Owner: -1})
		} else {
			b.WriteString("\n")
		}
	}
	return b.String(), lm
}

// FilePath returns the path of a successful Read result, from the render-only
// Meta first (the resolved, confined path the tool actually opened) and then
// from the tool arguments. It is empty for anything that is not a Read with a
// known path, which is what makes a row a file panel: its content is a file
// the thread has output.
func (m Message) FilePath() string {
	if m.Role != "tool" || m.ToolName != "Read" {
		return ""
	}
	if m.Meta != nil {
		if p, ok := m.Meta["path"].(string); ok && p != "" {
			return p
		}
	}
	path, _ := readArgs(m.ToolArgs)
	return path
}

// hasTruncation reports whether a rendered message carries a truncation hint,
// i.e. it is collapsed with hidden content that ctrl+o would reveal.
func hasTruncation(lm LineMap) bool {
	for _, l := range lm {
		if l.MarkerWidth > 0 && l.Hidden != "" {
			return true
		}
	}
	return false
}

// tagProvenance stamps every line of one message's render with the message
// index that produced it and the two hover-relevant facts about that message:
// whether it is a file panel and whether it is currently collapsed. It runs
// after every render — including cache hits — so the cache never needs to know
// about provenance, and a late-arriving path or expansion cannot leave stale
// tags behind.
func tagProvenance(lm LineMap, owner int, msg Message) {
	file := msg.FilePath() != ""
	collapsed := hasTruncation(lm)
	for i := range lm {
		lm[i].Owner = owner
		lm[i].File = file
		lm[i].Collapsed = collapsed
	}
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

	head := toolNameStyle(msg.ToolName).Render("⌁ " + msg.ToolName)
	plain := "⌁ " + msg.ToolName

	// Subagent activity rows carry a dim gutter naming their subagent, so a
	// reader can tell a forwarded subagent tool call from the parent's own.
	gutterPlain := ""
	if msg.SubagentID != "" {
		gutterPlain = subagentLabel(msg.SubagentID) + "  "
		head = MutedStyle.Render(gutterPlain) + head
		plain = gutterPlain + plain
	}

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
	if gutterPlain != "" {
		// The gutter is chrome: the copyable region still starts at the "⌁ ".
		prefixCol = visibleLen(gutterPlain) + visibleLen("⌁ ")
	}
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

	expand := expandAll || msg.Expanded

	out := statusLine

	// The body: the result if it has arrived, the live tail if the tool is
	// still running, and nothing at all if neither.
	switch content := strings.TrimRight(msg.Text(), "\n"); {
	case content == "" && msg.HasProgress():
		// Still running. Showing the tail keeps a long command legible while
		// it works, rather than a bare header with a ticking clock.
		preview, trunc := progressPreview(msg, expand)
		rendered, contentLm := renderToolContent(preview, width, false, trunc)
		out += "\n" + rendered
		lm = append(lm, contentLm...)

	case content == "":
		// Nothing to show yet.

	case msg.ToolName == "Read" && !isErr:
		// Read results are source, not prose: they get line numbers, and
		// syntax colours once expanded.
		rendered, contentLm := readToolRow(msg, width, expand)
		out += "\n" + rendered
		lm = append(lm, contentLm...)

	default:
		preview, trunc := previewOf(content, msg.ToolName, expand)
		rendered, contentLm := renderToolContent(preview, width, isErr, trunc)
		out += "\n" + rendered
		lm = append(lm, contentLm...)
	}

	// What the command changed goes beneath its output, where it reads as the
	// consequence of the command rather than a separate event. It is rendered
	// whether or not the command printed anything: a silent `sed -i` is
	// precisely the case where the diff is the only evidence of what happened.
	if msg.hasDiff() {
		if diffText, diffLm := diffToolRow(msg, width, expand); diffText != "" {
			out += "\n" + diffText
			lm = append(lm, diffLm...)
		}
	}
	return out, lm
}

// progressPreview renders the tail of a running tool's output. Only a bounded
// tail is retained, so an expanded row shows everything still held rather than
// everything ever produced — the full output arrives with the result.
func progressPreview(msg Message, expand bool) (string, truncation) {
	n := previewLines(msg.ToolName)
	if expand {
		n = progressRingLines
	}
	lines, earlier := msg.ProgressTail(n)
	preview := strings.Join(lines, "\n")
	if earlier <= 0 {
		return preview, truncation{}
	}
	// The hidden text is not recoverable — it was dropped to bound memory — so
	// the hint stands for a count only and copying it yields what is left.
	return preview, truncation{
		hidden: preview,
		label:  "… " + strconv.Itoa(earlier) + " earlier lines",
		atTop:  true,
	}
}

// previewLines is how many lines of a tool result to show while the transcript
// is collapsed. Bash gets three because a command's verdict is usually in its
// last few lines, and one line of a running command says nothing at all. Read
// gets three for the same reason in reverse: one line of a file is not enough
// to recognise it. Everything else stays at one — a Grep or Glob row is
// already a summary, and a WebFetch row's first line is its title.
func previewLines(toolName string) int {
	switch toolName {
	case "Bash", "Read":
		return 3
	default:
		return 1
	}
}

// tailAnchored reports whether a collapsed preview shows the end of a result
// rather than its start. A command's tail is where its verdict is; a file's
// head is where its identity is.
func tailAnchored(toolName string) bool { return toolName == "Bash" }

// previewOf reduces a tool result to what the collapsed transcript shows, plus
// the truncation that stands for the rest.
func previewOf(content, toolName string, expand bool) (string, truncation) {
	if expand {
		return content, truncation{}
	}
	n := previewLines(toolName)
	lines := strings.Split(content, "\n")
	if len(lines) <= n {
		return content, truncation{}
	}
	if tailAnchored(toolName) {
		cut := len(lines) - n
		return strings.Join(lines[cut:], "\n"), truncation{
			hidden: strings.Join(lines[:cut], "\n"),
			label:  "… " + strconv.Itoa(cut) + " earlier lines",
			atTop:  true,
		}
	}
	return strings.Join(lines[:n], "\n"), truncation{
		hidden: strings.Join(lines[n:], "\n"),
		label:  "… " + strconv.Itoa(len(lines)-n) + " more lines",
	}
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

// truncation describes the part of a tool result a collapsed row is not
// showing, and the on-screen hint that stands for it. atTop puts the hint
// above the content, which is what a tail-anchored preview needs: the hint
// summarises what came before, so it reads wrongly underneath.
type truncation struct {
	hidden string
	label  string
	atTop  bool
}

// renderToolContent indents and wraps a tool result. When trunc is non-empty a
// hint is placed after wrapping — above the body, or on the last body line
// when it fits there, or on a line of its own. Placing it post-wrap is what
// makes its cell range exactly known: a hint folded into the wrap could be
// split across two lines, leaving the marker unlocatable and the remainder
// uncopyable.
//
// The body is wrapped and measured unstyled and only then styled, so every
// SourceLine.Text stays free of ANSI as linemap.go:16-23 requires.
func renderToolContent(content string, width int, isErr bool, trunc truncation) (string, LineMap) {
	prefix := "  "
	pcol := visibleLen(prefix)
	inner := max(width-pcol, 8)

	wrapped := lipgloss.NewStyle().Width(inner).Render(content)
	plainLines := make([]string, 0)
	for _, line := range strings.Split(wrapped, "\n") {
		plainLines = append(plainLines, strings.TrimRight(line, " "))
	}

	// Place the hint. markerCol is a cell offset within the body; the prefix
	// is accounted for when the row is built.
	markerLine, markerCol := -1, 0
	hint := trunc.label
	switch {
	case trunc.hidden == "":
	case trunc.atTop:
		markerLine, markerCol = 0, 0
		plainLines = append([]string{""}, plainLines...)
	default:
		last := len(plainLines) - 1
		if w := visibleLen(plainLines[last]); w > 0 && w+2+visibleLen(hint) <= inner {
			markerLine, markerCol = last, w+2
		} else {
			markerLine, markerCol = len(plainLines), 0
			plainLines = append(plainLines, "")
		}
	}

	var fg lipgloss.TerminalColor
	if isErr {
		fg = ColorDanger
	}

	rows := make([]Row, 0, len(plainLines))
	for i, line := range plainLines {
		r := Row{Gutter: pcol, Segs: []Seg{NewSeg(prefix, nil)}}
		if line != "" {
			r.Segs = append(r.Segs, NewSeg(line, fg))
		}
		// The hint stays muted even on an error row: it is chrome, not output.
		if i == markerLine {
			if gap := markerCol - visibleLen(line); gap > 0 {
				r.Segs = append(r.Segs, NewSeg(spaces(gap), nil))
			}
			r.Segs = append(r.Segs, NewSeg(hint, ColorMuted))
			r.MarkerCol = pcol + markerCol
			r.MarkerWidth = visibleLen(hint)
			r.Hidden = trunc.hidden
		}
		rows = append(rows, r)
	}
	return renderRows(rows, width)
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
		"Write":     {"path"},
		"Edit":      {"path"},
		"Grep":      {"pattern", "query"},
		"Glob":      {"pattern", "query"},
		"WebSearch": {"query", "q"},
		"WebFetch":  {"url"},
		// Native catalogue tools: surface the most descriptive argument first.
		"Git":     {"command"},
		"JQ":      {"filter", "input"},
		"YQ":      {"filter", "input"},
		"Sed":     {"expression"},
		"Awk":     {"program"},
		"Cut":     {"fields"},
		"Tr":      {"set1"},
		"Sort":    {"path", "input"},
		"Uniq":    {"path", "input"},
		"WC":      {"path", "input"},
		"Paste":   {"path", "input"},
		"Join":    {"a"},
		"Find":    {"name", "path"},
		"Cat":     {"path"},
		"Head":    {"path"},
		"Tail":    {"path"},
		"LS":      {"path"},
		"File":    {"path"},
		"Strings": {"path"},
		"Diff":    {"a"},
		"Cmp":     {"a"},
		"Echo":    {"text"},
	}

	keys := keyOrder[name]
	if len(keys) == 0 {
		keys = []string{"command", "path", "pattern", "filter", "query", "url", "args"}
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

// vendorToolColors maps cloud/SaaS tools to their vendor colour for the tool
// row header. Purely cosmetic: it orients the user, nothing downstream reads
// it. Local native tools fall through to the standard tool colour.
var vendorToolColors = map[string]lipgloss.TerminalColor{
	"AWS":         lipgloss.Color("#FF9900"),
	"GH":          lipgloss.Color("#8957E5"),
	"Glab":        lipgloss.Color("#FC6D26"),
	"AZ":          lipgloss.Color("#0078D4"),
	"GCloud":      lipgloss.Color("#4285F4"),
	"Kubectl":     lipgloss.Color("#326CE5"),
	"Terraform":   lipgloss.Color("#7B42BC"),
	"Pulumi":      lipgloss.Color("#8A3391"),
	"Heroku":      lipgloss.Color("#79589F"),
	"Fly":         lipgloss.Color("#8B5CF6"),
	"Netlify":     lipgloss.Color("#00C7B7"),
	"Doctl":       lipgloss.Color("#0080FF"),
	"Stripe":      lipgloss.Color("#635BFF"),
	"OnePassword": lipgloss.Color("#0085FF"),
	"Bitwarden":   lipgloss.Color("#175DDC"),
}

// toolNameStyle returns the style for a tool row's name: the vendor colour for
// a known cloud/SaaS tool, the standard tool colour for the other native
// catalogue tools, and the muted style for everything else (unchanged).
func toolNameStyle(name string) lipgloss.Style {
	if c, ok := vendorToolColors[name]; ok {
		return lipgloss.NewStyle().Foreground(c)
	}
	if isNativeTool(name) {
		return WarnStyle
	}
	return MutedStyle
}

// nativeToolNames is the set of first-class native tools (local + cloud). A
// native tool gets the standard tool colour even when it has no vendor colour.
var nativeToolNames = map[string]bool{
	"Cat": true, "Head": true, "Tail": true, "LS": true, "Find": true,
	"Git": true, "JQ": true, "YQ": true, "Sed": true, "Awk": true,
	"Cut": true, "Sort": true, "Uniq": true, "WC": true, "Tr": true,
	"Paste": true, "Join": true, "Echo": true, "Date": true, "Pwd": true,
	"Env": true, "Diff": true, "Cmp": true, "File": true, "Strings": true,
	"AWS": true, "GH": true, "Glab": true, "AZ": true, "GCloud": true,
	"Kubectl": true, "Terraform": true, "Pulumi": true, "Heroku": true,
	"Fly": true, "Vercel": true, "Netlify": true, "Doctl": true,
	"Stripe": true, "OnePassword": true, "Bitwarden": true,
}

func isNativeTool(name string) bool { return nativeToolNames[name] }

// subagentLabel renders a human-facing gutter label from a subagent ID. The
// ID prefix encodes the kind: e→explore, g→survey, c→clarify, bg:→background.
func subagentLabel(id string) string {
	switch {
	case strings.HasPrefix(id, "bg:"):
		return strings.TrimPrefix(id, "bg:")
	case strings.HasPrefix(id, "e"):
		return "explore " + strings.TrimPrefix(id, "e")
	case strings.HasPrefix(id, "g"):
		return "survey " + strings.TrimPrefix(id, "g")
	case strings.HasPrefix(id, "c"):
		return "clarify " + strings.TrimPrefix(id, "c")
	default:
		return id
	}
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
// The body is wrapped unstyled and styled one line at a time: wrapping
// pre-styled text would leave escape bytes in SourceLine.Text, which
// linemap.go:16-23 forbids and LineMap.Text would then slice as if they were
// visible cells.
func systemRow(content string, width int) (string, LineMap) {
	body := strings.TrimRight(content, "\n")
	marker := "│ "
	wrapped := lipgloss.NewStyle().Width(max(width-visibleLen(marker), 8)).Render(body)

	var rows []Row
	for _, line := range strings.Split(wrapped, "\n") {
		rows = append(rows, Row{
			Gutter: visibleLen(marker),
			Segs: []Seg{
				NewSeg(marker, ColorMuted),
				NewSeg(strings.TrimRight(line, " "), ColorMuted),
			},
		})
	}
	return renderRows(rows, width)
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
