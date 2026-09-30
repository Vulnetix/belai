package session

import (
	"sync"
	"time"
	"unicode/utf8"
)

// MaxToolResultBytes caps a tool result written to a transcript, the same
// cap the TUI applies.
const MaxToolResultBytes = 32 << 10

// Writer appends one session's entries outside the TUI — a fleet worker's
// transcript — chaining each entry's ParentID to the one before, exactly as
// the TUI's appendEntry does, so a worker's session resumes, exports and
// searches like any other. It is safe for concurrent use.
type Writer struct {
	store *Store
	key   Key
	id    string

	mu   sync.Mutex
	last string
	err  error
}

// NewWriter starts a transcript for session id under the project key and
// writes its session_meta entry first.
func NewWriter(store *Store, key Key, id string, meta Meta) (*Writer, error) {
	w := &Writer{store: store, key: key, id: id}
	if err := store.Create(key, id); err != nil {
		return nil, err
	}
	w.append(meta.ToEntry(""))
	return w, w.Err()
}

// ID is the session id.
func (w *Writer) ID() string { return w.id }

// Err is the first append error; later appends are skipped after one.
func (w *Writer) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

// Entry writes any entry, chained like the rest, and returns its id ("" if
// it was not written).
func (w *Writer) Entry(e Entry) string { return w.append(e) }

func (w *Writer) append(e Entry) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return ""
	}
	if e.ID == "" {
		e.ID = MustID()
	}
	e.ParentID = w.last
	if e.Timestamp == 0 {
		e.Timestamp = time.Now().UnixMilli()
	}
	if err := w.store.AppendTo(w.key, w.id, e); err != nil {
		w.err = err
		return ""
	}
	w.last = e.ID
	return e.ID
}

// Branch moves the transcript to continue from an earlier entry: it writes a
// branch marker parented there, so later entries chain from that point and the
// file stays append-only. An empty to is refused.
func (w *Writer) Branch(to string) string {
	if to == "" {
		return ""
	}
	w.mu.Lock()
	from := w.last
	w.mu.Unlock()
	m := BranchMarker(from, to)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return ""
	}
	m.ID = MustID()
	m.Timestamp = time.Now().UnixMilli()
	if err := w.store.AppendTo(w.key, w.id, m); err != nil {
		w.err = err
		return ""
	}
	w.last = m.ID
	return m.ID
}

// Entries reads back this session's own file.
func (w *Writer) Entries() ([]Entry, error) { return w.store.ReadFrom(w.key, w.id) }

// Last is the id the next entry will parent to.
func (w *Writer) Last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// User writes a user turn.
func (w *Writer) User(text string) { w.append(Entry{Type: "user", Role: "user", Content: text}) }

// Assistant writes an assistant turn. calls lists answered tool calls as
// {id, name, args} maps; meta carries model, provider and usage.
func (w *Writer) Assistant(text string, calls []map[string]any, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	if len(calls) > 0 {
		meta["tool_calls"] = calls
	}
	w.append(Entry{Type: "assistant", Role: "assistant", Content: text, Meta: meta})
}

// Tool writes a tool result, capped at MaxToolResultBytes.
func (w *Writer) Tool(callID, name, args, status, content string) {
	meta := map[string]any{"tool_call_id": callID, "tool_name": name, "tool_args": args, "status": status}
	if len(content) > MaxToolResultBytes {
		meta["truncated"] = true
		meta["orig_len"] = len(content)
		cut := MaxToolResultBytes
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}
		content = content[:cut]
	}
	w.append(Entry{Type: "tool", Role: "tool", Content: content, Meta: meta})
}

// System writes a harness line.
func (w *Writer) System(text string) {
	w.append(Entry{Type: "system", Role: "system", Content: text})
}

// Name records the session's display name.
func (w *Writer) Name(name string) {
	w.append(Entry{Type: EntryTypeSessionName, Content: name})
}
