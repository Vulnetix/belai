// Package turnlog writes a headless session's turns to its JSONL transcript
// from the agent's event stream: the user line, the assistant text and tool
// calls, tool results, harness warnings and the turn boundaries. A fleet
// worker and an rc session both use it, so their transcripts resume, export,
// search and sync exactly like a TUI session's.
package turnlog

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/vulnid"
)

// Log wraps a session.Writer. A nil Writer makes every method a no-op, so a
// caller whose transcript failed to open keeps working without one.
type Log struct {
	w *session.Writer

	mu    sync.Mutex
	text  strings.Builder
	calls []map[string]any

	// diffs holds what a mutating call changed on disk until its result is
	// written: the agent reports the change just before the result.
	diffs map[string]filediff.WireChange

	// vulns notes the advisory identifiers a turn shows, so the website gets
	// the same vulnerability row the terminal draws (docs/vuln-row.md).
	vulns vulnid.Tracker

	// lastTodos is the content of the todo_list entry last written, so an
	// unchanged list is not written again.
	lastTodos string

	turnID    string
	turnStart time.Time
}

// New returns a Log writing to w (which may be nil).
func New(w *session.Writer) *Log { return &Log{w: w} }

// Writer is the underlying writer, or nil.
func (l *Log) Writer() *session.Writer { return l.w }

// User writes a user turn and returns its entry id ("" when not written).
func (l *Log) User(text string, meta map[string]any) string {
	if l.w == nil {
		return ""
	}
	return l.w.Entry(session.Entry{Type: "user", Role: "user", Content: text, Meta: meta})
}

// Name records the session's display name.
func (l *Log) Name(name string) {
	if l.w != nil {
		l.w.Name(name)
	}
}

// System writes a harness line.
func (l *Log) System(text string) {
	if l.w != nil {
		l.w.System(text)
	}
}

// ShellEntry is the transcript type and role of a `!cmd` line: what the TUI
// writes for its shell panel and what the website renders as a shell block.
const ShellEntry = "shell"

// maxDiffWireBytes caps the rendered diff a tool entry carries, as the TUI does.
const maxDiffWireBytes = 256 << 10

// maxShellBytes caps a shell entry's content, as the TUI caps a tool result.
const maxShellBytes = 32 << 10

// Shell writes a shell line's result and returns its entry id ("" when not
// written). meta carries command, shell_id and status like the TUI's entry;
// content is cut at a rune boundary when it is over the cap.
func (l *Log) Shell(content string, meta map[string]any) string {
	if l.w == nil {
		return ""
	}
	if len(content) > maxShellBytes {
		cut := maxShellBytes
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		meta = copyMeta(meta)
		meta["truncated"] = true
		meta["orig_len"] = len(content)
		content = content[:cut]
	}
	return l.w.Entry(session.Entry{Type: ShellEntry, Role: ShellEntry, Content: content, Meta: meta})
}

// Entry types a web shell line writes while it runs. The final ShellEntry still
// carries the whole result; these let the website show the output as it
// arrives. A resumed TUI session ignores both.
const (
	// ShellRunEntry marks a line that has started: meta has shell_id, command,
	// cwd, source and attach.
	ShellRunEntry = "shell_run"
	// ShellOutEntry is a slice of the running line's output, in order: meta has
	// shell_id and n.
	ShellOutEntry = "shell_out"
)

// ShellRun records that a web shell line began running.
func (l *Log) ShellRun(meta map[string]any) string {
	if l.w == nil {
		return ""
	}
	return l.w.Entry(session.Entry{Type: ShellRunEntry, Role: ShellRunEntry, Meta: meta})
}

// ShellOut records a slice of a running web shell line's output.
func (l *Log) ShellOut(content string, meta map[string]any) string {
	if l.w == nil {
		return ""
	}
	return l.w.Entry(session.Entry{Type: ShellOutEntry, Role: ShellOutEntry, Content: content, Meta: meta})
}

func copyMeta(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Flush writes the assistant text and calls gathered so far.
func (l *Log) Flush() {
	if l.w == nil {
		return
	}
	l.mu.Lock()
	text, calls := l.text.String(), l.calls
	l.text.Reset()
	l.calls = nil
	l.mu.Unlock()
	l.vulns.Observe(text)
	if strings.TrimSpace(text) != "" || len(calls) > 0 {
		l.w.Assistant(text, calls, nil)
	}
}

// Observe records one agent event.
func (l *Log) Observe(e agent.Event) {
	if l.w == nil {
		return
	}
	switch e.Kind {
	case agent.EventTextKind:
		l.mu.Lock()
		l.text.WriteString(e.Text)
		l.mu.Unlock()
	case agent.EventToolStartKind:
		if e.Tool == nil {
			return
		}
		args := e.Tool.RawArgs
		if args == "" && len(e.Tool.Args) > 0 {
			if b, err := json.Marshal(e.Tool.Args); err == nil {
				args = string(b)
			}
		}
		l.mu.Lock()
		l.calls = append(l.calls, map[string]any{"id": e.Tool.ID, "name": e.Tool.Name, "args": args})
		l.mu.Unlock()
	case agent.EventToolDiffKind:
		if e.Diff == nil || e.Diff.Empty() || e.ToolCallID == "" {
			return
		}
		l.mu.Lock()
		if l.diffs == nil {
			l.diffs = map[string]filediff.WireChange{}
		}
		l.diffs[e.ToolCallID] = e.Diff.Wire(maxDiffWireBytes)
		l.mu.Unlock()
	case agent.EventToolResultKind:
		l.Flush()
		var extra map[string]any
		l.mu.Lock()
		if d, ok := l.diffs[e.ToolCallID]; ok {
			extra = map[string]any{"diff": d}
			delete(l.diffs, e.ToolCallID)
		}
		l.mu.Unlock()
		l.w.ToolWith(e.ToolCallID, e.ToolName, e.ToolArgs, "done", e.ToolResult, extra)
		l.vulns.Observe(e.ToolResult)
	case agent.EventWarningKind:
		if e.Warning != "" {
			l.w.System(e.Warning)
		}
	case agent.EventTodosKind, agent.EventPassKind, agent.EventGoalEvalKind, agent.EventPlanEvalKind:
		l.todoList(e.Todos)
	case agent.EventGoalStateKind:
		if e.GoalState != nil {
			l.Flush()
			l.w.Entry(e.GoalState.ToEntry(l.w.Last()))
		}
	}
}

// todoList writes the session's todo list as a todo_list entry, the record the
// website's Todo panel reads. The TUI has always written it; headless, rc, fleet
// and ACP sessions go through this log, so they write it here. An unchanged list
// is not written again: the record is append-only and latest wins.
func (l *Log) todoList(list *todos.List) {
	if list == nil {
		return
	}
	entry := list.ToEntry("")
	l.mu.Lock()
	same := entry.Content == l.lastTodos
	l.lastTodos = entry.Content
	l.mu.Unlock()
	if same {
		return
	}
	l.Flush()
	entry.ParentID = l.w.Last()
	l.w.Entry(entry)
}

// TurnStarted writes a turn_state "started" line carrying facts (mode,
// provider, model, …), the record the website reads to show a turn running.
func (l *Log) TurnStarted(facts map[string]any) {
	if l.w == nil {
		return
	}
	id, err := session.NewID()
	if err != nil {
		return
	}
	l.turnID, l.turnStart = id, time.Now()
	meta := map[string]any{}
	for k, v := range facts {
		meta[k] = v
	}
	meta["turn_id"], meta["state"], meta["started_at"] = id, "started", l.turnStart.UnixMilli()
	l.w.Entry(session.Entry{Type: "turn_state", Role: "system", Meta: meta})
}

// TurnEnded flushes the turn's last text and writes its turn_state end line
// ("ended", "error" or "interrupted").
func (l *Log) TurnEnded(state string, facts map[string]any) {
	l.Flush()
	l.vulnRows()
	if l.w == nil || l.turnID == "" {
		return
	}
	now := time.Now()
	meta := map[string]any{}
	for k, v := range facts {
		meta[k] = v
	}
	meta["turn_id"], meta["state"], meta["started_at"] = l.turnID, state, l.turnStart.UnixMilli()
	meta["duration_ms"] = now.Sub(l.turnStart).Milliseconds()
	l.turnID = ""
	l.w.Entry(session.Entry{Type: "turn_state", Role: "system", Timestamp: now.UnixMilli(), Meta: meta})
}

// Open starts a transcript for a session in workdir and returns a Log writing
// to it. The transcript is the same JSONL a TUI session keeps, so the session
// lists, resumes, exports and searches like any other. When the store or the
// file cannot be opened the returned Log has no writer, every method is a
// no-op, and err says why: a run never fails because its record could not be
// kept.
func Open(workdir, id string, meta session.Meta) (*Log, error) {
	store, err := session.NewStore()
	if err != nil {
		return New(nil), err
	}
	key, err := session.KeyFor(workdir)
	if err != nil {
		return New(nil), err
	}
	if meta.Cwd == "" {
		meta.Cwd = workdir
	}
	w, err := session.NewWriter(store, key, id, meta)
	if err != nil {
		return New(nil), err
	}
	return New(w), nil
}

// vulnRows writes a vuln entry for each identifier the turn newly showed: the
// canonical identifier and what the row offers, all composed from it by vulnid
// and never from the text around it.
func (l *Log) vulnRows() {
	ids := l.vulns.Flush()
	if l.w == nil {
		return
	}
	for _, id := range ids {
		l.w.Entry(session.Entry{Type: vulnid.EntryType, Role: "system", Content: id, Meta: vulnid.EntryMeta(id)})
	}
}
