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

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/session"
)

// Log wraps a session.Writer. A nil Writer makes every method a no-op, so a
// caller whose transcript failed to open keeps working without one.
type Log struct {
	w *session.Writer

	mu    sync.Mutex
	text  strings.Builder
	calls []map[string]any

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
	case agent.EventToolResultKind:
		l.Flush()
		l.w.Tool(e.ToolCallID, e.ToolName, e.ToolArgs, "done", e.ToolResult)
	case agent.EventWarningKind:
		if e.Warning != "" {
			l.w.System(e.Warning)
		}
	}
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
