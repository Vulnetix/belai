// Package turnlog writes a headless session's turns to its JSONL transcript
// from the agent's event stream: the user line, the assistant text and tool
// calls, tool results, harness warnings and the turn boundaries. A fleet
// worker and an rc session both use it, so their transcripts resume, export,
// search and sync exactly like a TUI session's.
package turnlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/budget"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionmeasure"
	"github.com/vulnetix/belai/internal/testrun"
	"github.com/vulnetix/belai/internal/todos"
	"github.com/vulnetix/belai/internal/transcript"
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

	// provider, model and price say who the assistant entries are by and what
	// they cost (SetModel). usage is the provider-reported usage of the model
	// calls since the last assistant entry, written onto the next one.
	provider, model string
	price           *budget.Price
	usage           transcript.Usage
	hasUsage        bool

	turnID    string
	turnStart time.Time
}

// New returns a Log writing to w (which may be nil).
func New(w *session.Writer) *Log { return &Log{w: w} }

// Writer is the underlying writer, or nil.
func (l *Log) Writer() *session.Writer { return l.w }

// SetModel names the model the following assistant entries are by, and the
// price a call on it is costed at: the one the Software Factory sent, or nil
// for Belai's built-in table. A local provider is free and a model with no
// price gets tokens and no cost (budget.CostFor). Call it again when the
// session changes model.
func (l *Log) SetModel(provider, model string, price *budget.Price) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.provider, l.model, l.price = provider, model, price
	l.mu.Unlock()
}

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

// A2A writes one A2A entry (a2a_broadcast_in, a2a_reply_out, …; see
// docs/a2a.md#transcripts): render-only, never replayed as a model message.
// It is safe from any goroutine.
func (l *Log) A2A(entryType, content string, meta map[string]any) {
	if l == nil || l.w == nil || !strings.HasPrefix(entryType, "a2a_") {
		return
	}
	l.w.Entry(session.Entry{Type: entryType, Role: "a2a", Content: content, Meta: copyMeta(meta)})
}

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
		l.w.Assistant(text, calls, l.assistantMeta())
	}
}

// assistantMeta is the meta of the assistant entry being written: the model
// and, when a model call reported usage since the last entry, its tokens, cache
// tokens and cost. Taking the usage clears it, so a call is counted once.
func (l *Log) assistantMeta() map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	meta := map[string]any{}
	if l.provider != "" {
		meta["provider"] = l.provider
	}
	if l.model != "" {
		meta["model"] = l.model
	}
	if l.hasUsage {
		sessionmeasure.AddUsage(meta, l.provider, l.model, l.price, l.usage)
		l.usage, l.hasUsage = transcript.Usage{}, false
	}
	return meta
}

// CheckRuns records the suites a test pass ran as check_run entries. A suite
// that never ran (refused, no binary) is not a check.
func (l *Log) CheckRuns(results []testrun.Result) {
	if l == nil || l.w == nil {
		return
	}
	for _, r := range results {
		if c, ok := sessionmeasure.FromTestrun(r, sessionmeasure.KindTest); ok {
			l.w.Entry(c.Entry())
		}
	}
}

// ShellCheck records a finished shell line as a check_run when it is a known
// build, test or lint command. exit is the line's exit code; the caller passes
// only lines that ran to an exit.
func (l *Log) ShellCheck(command string, exit int, dur time.Duration) {
	if l == nil || l.w == nil {
		return
	}
	if cls, ok := sessionmeasure.ClassifyLine(command); ok {
		l.w.Entry(sessionmeasure.Check{Kind: cls.Kind, Program: cls.Program, Subcommand: cls.Subcommand,
			ExitCode: exit, DurationMS: max(dur.Milliseconds(), 0)}.Entry())
	}
}

// Check records one check run.
func (l *Log) Check(c sessionmeasure.Check) {
	if l != nil && l.w != nil {
		l.w.Entry(c.Entry())
	}
}

// Commit records a commit the harness made.
func (l *Log) Commit(c sessionmeasure.Commit) {
	if l != nil && l.w != nil {
		l.w.Entry(c.Entry())
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
	case agent.EventModelCallKind:
		if e.Usage != nil {
			l.mu.Lock()
			l.usage.PromptTokens += e.Usage.PromptTokens
			l.usage.CompletionTokens += e.Usage.CompletionTokens
			l.usage.TotalTokens += e.Usage.TotalTokens
			l.usage.CacheReadTokens += e.Usage.CacheReadTokens
			l.usage.CacheWriteTokens += e.Usage.CacheWriteTokens
			l.hasUsage = true
			l.mu.Unlock()
		}
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
		if sessionmeasure.IsBash(e.ToolName) {
			if c, ok := sessionmeasure.FromBashArgs(e.ToolArgs, e.ToolResult, e.Duration.Milliseconds()); ok {
				l.w.Entry(c.Entry())
			}
		}
		l.vulns.Observe(e.ToolResult)
	case agent.EventWarningKind:
		if e.Warning != "" {
			l.w.System(e.Warning)
		}
	case agent.EventRetryKind:
		// A retried model call is a harness fact the transcript keeps, so a
		// turn that later fails shows what was tried: the count and one
		// cleaned, capped line of why.
		line := fmt.Sprintf("retrying the model call (%d", e.RetryAttempt)
		if e.RetryMax > 0 {
			line += fmt.Sprintf("/%d", e.RetryMax)
		}
		line += ")"
		if e.RetryReason != "" {
			line += ": " + sanitize.Line(e.RetryReason, 200)
		}
		l.w.System(line)
	case agent.EventAskDecidedKind:
		// An ask the decision model answered in the user's place: a harness
		// line of kind, outcome and confidence, never the question.
		if e.Decided != nil {
			l.w.System(e.Decided.Line())
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
	// The footer facts name the model the turn runs on, so its assistant
	// entries carry it. A switch drops a price set for the earlier model.
	if p, _ := facts["provider"].(string); p != "" {
		if m, _ := facts["model"].(string); m != "" {
			l.mu.Lock()
			if p != l.provider || m != l.model {
				l.price = nil
			}
			l.provider, l.model = p, m
			l.mu.Unlock()
		}
	}
	meta["turn_id"], meta["state"], meta["started_at"] = id, "started", l.turnStart.UnixMilli()
	l.w.Entry(session.Entry{Type: "turn_state", Role: "system", Meta: meta})
}

// TurnID is the id the running turn's turn_state lines carry, or "" outside
// a turn (or without a writer). Call it from the goroutine that runs the turn.
func (l *Log) TurnID() string { return l.turnID }

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
