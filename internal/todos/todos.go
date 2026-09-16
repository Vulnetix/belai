// Package todos owns the single TODO list a session tracks, whatever mode
// produced it. Goal mode, plan pursual, and agent loops all write into the same
// structure so the TUI has one thing to render and resume has one thing to
// rehydrate.
//
// A list is persisted as an append-only session entry, latest wins — the same
// shape modes.PlanState already uses. Completing a list does not delete it:
// the next prompt supersedes it with a cleared entry, and the old entry stays
// in the JSONL so earlier work remains readable.
package todos

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vulnetix/signet/internal/plans"
	"github.com/vulnetix/signet/internal/session"
)

// EntryType is the session entry type carrying a todo list.
const EntryType = "todo_list"

// Status is one item's lifecycle state.
type Status string

const (
	// StatusPending is work not yet started.
	StatusPending Status = "pending"
	// StatusActive is the item currently being worked on. At most one item in
	// a list is active.
	StatusActive Status = "active"
	// StatusDone is completed work.
	StatusDone Status = "done"
)

// Item is one tracked step. N is 1-indexed and stable for the life of the list,
// because it is what [DONE:n] markers refer to.
type Item struct {
	N      int    `json:"n"`
	Text   string `json:"text"`
	Status Status `json:"status"`
}

// List is the todo list for one prompt turn.
type List struct {
	ID    string `json:"id"`
	Items []Item `json:"items"`
	// Prompt is the user prompt this list serves, kept so a superseded list is
	// still attributable when read back out of the session.
	Prompt string `json:"prompt,omitempty"`
	// Cleared marks a list that has been superseded. A cleared list is history:
	// it is never rendered as active and never advanced.
	Cleared bool `json:"cleared,omitempty"`
}

// New builds a list from ordered step texts. Empty and blank steps are dropped
// so a stray blank line in an extracted plan does not become a todo.
func New(prompt string, steps []string) List {
	l := List{ID: session.MustID(), Prompt: prompt}
	for _, s := range steps {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		l.Items = append(l.Items, Item{N: len(l.Items) + 1, Text: s, Status: StatusPending})
	}
	l.syncActive()
	return l
}

// syncActive makes the first not-done item active and every other not-done item
// pending, so "current" is always well defined and never ambiguous.
func (l *List) syncActive() {
	seen := false
	for i := range l.Items {
		if l.Items[i].Status == StatusDone {
			continue
		}
		if !seen {
			l.Items[i].Status = StatusActive
			seen = true
			continue
		}
		l.Items[i].Status = StatusPending
	}
}

// ApplyMarkers advances the list from [DONE:n] markers in model-authored
// assistant text.
//
// The parameter name is the contract: pass ONLY the assistant's own reply.
// Tool results, file contents, and any other untrusted text must never reach
// this function — a repository file containing "[DONE:1] [DONE:2]" would
// otherwise mark real work complete, and completion is an input to whether a
// goal loop stops. The harness decides what is complete; content does not.
func (l *List) ApplyMarkers(assistantText string) {
	if l.Cleared {
		return
	}
	for _, n := range plans.ParseDoneMarkers(assistantText) {
		for i := range l.Items {
			if l.Items[i].N == n {
				l.Items[i].Status = StatusDone
			}
		}
	}
	l.syncActive()
}

// Progress builds a plans.Progress from the list, reusing the existing
// completion math rather than restating it.
func (l List) Progress() *plans.Progress {
	p := plans.NewProgress(len(l.Items))
	for _, it := range l.Items {
		if it.Status == StatusDone {
			p.MarkDone(it.N)
		}
	}
	return p
}

// MarkAllDone completes every item. It is what the goal pass loop calls when
// a GOAL_COMPLETE verdict is accepted: the harness declares the goal met, and
// the list reflects that rather than the model's own markers.
func (l *List) MarkAllDone() {
	if l.Cleared {
		return
	}
	for i := range l.Items {
		l.Items[i].Status = StatusDone
	}
}

// Complete reports whether every item is done. An empty list is not complete:
// nothing to do is not the same as finished, and treating it as finished would
// let a goal loop stop before it ever wrote a plan.
func (l List) Complete() bool {
	if len(l.Items) == 0 {
		return false
	}
	return l.Progress().Done()
}

// Window returns the render window for a status panel: the last completed item,
// the current one, the next one, and how many further items remain beyond
// those. Any of the three may be nil. The counts never include the returned
// items themselves, so "+n more" is always literally true.
func (l List) Window() (prev, current, next *Item, more int) {
	items := l.Items
	curIdx := -1
	for i := range items {
		if items[i].Status == StatusActive {
			curIdx = i
			break
		}
	}
	if curIdx == -1 {
		// No active item means the list is complete (or empty). Surface the
		// last item as prev so a finished list still shows what it finished on
		// rather than rendering blank.
		if n := len(items); n > 0 {
			return &items[n-1], nil, nil, 0
		}
		return nil, nil, nil, 0
	}
	if curIdx > 0 {
		prev = &items[curIdx-1]
	}
	current = &items[curIdx]
	if curIdx+1 < len(items) {
		next = &items[curIdx+1]
		more = len(items) - (curIdx + 2)
	}
	if more < 0 {
		more = 0
	}
	return prev, current, next, more
}

// Render renders the list as plain text for a classifier payload. It carries no
// harness markup: the caller is responsible for sealing or framing it.
func (l List) Render() string {
	if len(l.Items) == 0 {
		return "(no plan recorded)"
	}
	var b strings.Builder
	for _, it := range l.Items {
		mark := " "
		switch it.Status {
		case StatusDone:
			mark = "x"
		case StatusActive:
			mark = ">"
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", it.N, mark, it.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// ToEntry renders the list as a session entry.
func (l List) ToEntry(parentID string) session.Entry {
	data, err := json.Marshal(l)
	if err != nil {
		data = []byte("{}")
	}
	return session.Entry{
		ID:       session.MustID(),
		ParentID: parentID,
		Type:     EntryType,
		Content:  string(data),
	}
}

// FromEntry parses a session entry back into a List.
func FromEntry(e session.Entry) (List, error) {
	var l List
	if err := json.Unmarshal([]byte(e.Content), &l); err != nil {
		return List{}, fmt.Errorf("parse todo list: %w", err)
	}
	return l, nil
}

// ClearedCopy returns a superseded copy of the list. The original entry is left
// alone in the session so it stays readable; this is what gets appended to
// supersede it.
func (l List) ClearedCopy() List {
	l.Cleared = true
	return l
}

// Latest returns the most recent todo list in an append-ordered entry slice,
// and whether one was found. Latest wins, matching how session names and plan
// state are read back.
func Latest(entries []session.Entry) (List, bool) {
	var out List
	var found bool
	for _, e := range entries {
		if e.Type != EntryType {
			continue
		}
		l, err := FromEntry(e)
		if err != nil {
			continue // a malformed entry is skipped, not fatal
		}
		out, found = l, true
	}
	return out, found
}
