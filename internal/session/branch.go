package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/vaultenv"
)

// EntryTypeBranch marks a navigation inside a session: the entry after it
// parents to an earlier entry, so the file stays append-only while the
// conversation continues from that point. Meta carries from_entry (the leaf
// the user left) and to_entry (the leaf the new branch grows from).
const EntryTypeBranch = "branch"

// BranchMarker builds the marker entry for moving the active leaf from one
// entry to another. Append it with ParentID set to to, so every later entry
// chains from the chosen point.
func BranchMarker(from, to string) Entry {
	return Entry{
		Type:     EntryTypeBranch,
		ParentID: to,
		Meta:     map[string]any{"from_entry": from, "to_entry": to},
	}
}

// HasBranches reports whether the session was ever navigated. A session that
// was not is a single chain in file order, which loaders may keep reading
// flat; this keeps files from before branching loading exactly as they did.
func HasBranches(entries []Entry) bool {
	for _, e := range entries {
		if e.Type == EntryTypeBranch {
			return true
		}
	}
	return false
}

// ActiveLeaf is the entry the session continues from: the last one written,
// because every append parents to the entry before it.
func ActiveLeaf(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	return entries[len(entries)-1].ID
}

// Path returns the entries on the chain from the root to leaf, in root-to-leaf
// order, following ParentID links. An unknown leaf yields nil, a missing parent
// ends the chain there, and a cycle is cut rather than followed.
func Path(entries []Entry, leaf string) []Entry {
	byID := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if _, dup := byID[e.ID]; !dup {
			byID[e.ID] = e
		}
	}
	var rev []Entry
	seen := map[string]bool{}
	for id := leaf; id != "" && !seen[id]; {
		e, ok := byID[id]
		if !ok {
			break
		}
		seen[id] = true
		rev = append(rev, e)
		id = e.ParentID
	}
	if len(rev) == 0 {
		return nil
	}
	out := make([]Entry, len(rev))
	for i, e := range rev {
		out[len(rev)-1-i] = e
	}
	return out
}

// ActivePath is the conversation a loader should rebuild: the whole file in
// order when it was never navigated, otherwise the chain to the active leaf.
func ActivePath(entries []Entry) []Entry {
	if !HasBranches(entries) {
		return entries
	}
	return Path(entries, ActiveLeaf(entries))
}

// TreeRow is one line of the tree view.
type TreeRow struct {
	Entry Entry
	// Depth grows by one under each branch point, so a linear run stays flush.
	Depth int
	// OnPath marks rows on the chain to the active leaf.
	OnPath bool
	// Leaf marks the active leaf itself.
	Leaf bool
	// Fork marks a row whose subtree splits into more than one branch.
	Fork bool
	// Label is one flattened, capped line: the role and the start of the text.
	Label string
}

// TreeRows flattens the session into display rows, depth first, older branches
// before newer ones. Unless all is set only user turns and final assistant
// replies (no tool calls) show, since those are the points a conversation can
// continue from; state rows are never shown.
func TreeRows(entries []Entry, all bool) []TreeRow {
	leaf := ActiveLeaf(entries)
	onPath := map[string]bool{}
	for _, e := range Path(entries, leaf) {
		onPath[e.ID] = true
	}
	var rows []TreeRow
	var walk func(n *Node, depth int)
	walk = func(n *Node, depth int) {
		next := depth
		if len(n.Children) > 1 {
			next = depth + 1
		}
		if shown(n.Entry, all) {
			rows = append(rows, TreeRow{
				Entry:  n.Entry,
				Depth:  depth,
				OnPath: onPath[n.Entry.ID],
				Leaf:   n.Entry.ID == leaf,
				Fork:   len(n.Children) > 1,
				Label:  rowLabel(n.Entry),
			})
		}
		for _, c := range n.Children {
			walk(c, next)
		}
	}
	for _, r := range BuildTree(entries) {
		walk(r, 0)
	}
	return rows
}

func shown(e Entry, all bool) bool {
	switch e.Type {
	case "user":
		return true
	case "assistant":
		return all || !hasToolCalls(e)
	case "tool", "reasoning", "system", EntryTypeBranch:
		return all
	}
	return false
}

func hasToolCalls(e Entry) bool {
	v, ok := e.Meta["tool_calls"]
	if !ok {
		return false
	}
	if l, ok := v.([]any); ok {
		return len(l) > 0
	}
	if l, ok := v.([]map[string]any); ok {
		return len(l) > 0
	}
	return v != nil
}

func rowLabel(e Entry) string {
	role := e.Type
	text := strings.Join(strings.Fields(sanitizeLine(e.Content)), " ")
	if e.Type == EntryTypeBranch {
		text = "branch"
	}
	const max = 80
	if r := []rune(text); len(r) > max {
		text = string(r[:max-3]) + "..."
	}
	if text == "" {
		return role
	}
	return role + ": " + text
}

// sanitizeLine drops control characters so a stored line cannot move the
// terminal cursor or forge a second row.
func sanitizeLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// FindEntry resolves an id or unique id prefix to an entry.
func FindEntry(entries []Entry, idOrPrefix string) (Entry, error) {
	idOrPrefix = strings.TrimSpace(idOrPrefix)
	if idOrPrefix == "" {
		return Entry{}, fmt.Errorf("no entry id given")
	}
	var match *Entry
	for i := range entries {
		e := &entries[i]
		if e.ID == idOrPrefix {
			return *e, nil
		}
		if strings.HasPrefix(e.ID, idOrPrefix) {
			if match != nil && match.ID != e.ID {
				return Entry{}, fmt.Errorf("entry id %q is ambiguous", idOrPrefix)
			}
			match = e
		}
	}
	if match == nil {
		return Entry{}, fmt.Errorf("no entry matches %q", idOrPrefix)
	}
	return *match, nil
}

// Target says where the session continues when the user picks an entry. A user
// turn puts its text back for editing and continues from the entry before it,
// so the prompt can be reworded; a final assistant reply continues from itself.
// Anything else is not a point a conversation can resume from.
func Target(e Entry) (leaf, prefill string, err error) {
	switch {
	case e.Type == "user":
		return e.ParentID, e.Content, nil
	case e.Type == "assistant" && !hasToolCalls(e):
		return e.ID, "", nil
	}
	return "", "", fmt.Errorf("pick a user message or a final assistant reply")
}

// ForkPath copies the chain ending at leaf from one session into a new session
// id, then records where it came from. The destination must not exist. The
// source file is only read.
func (s *Store) ForkPath(src Key, srcID string, dst Key, dstID, leaf string) error {
	entries, err := s.ReadFrom(src, srcID)
	if err != nil {
		return err
	}
	chain := Path(entries, leaf)
	if len(chain) == 0 {
		return fmt.Errorf("entry %q is not in session %q", leaf, srcID)
	}
	path := s.sessionPathForKey(dst, dstID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create forked session: %w", err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	chain = append(chain, Meta{ResumedFrom: srcID}.ToEntry(chain[len(chain)-1].ID))
	for _, e := range chain {
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}
		data = vaultenv.Default.ScrubBytes(data)
		if _, err := w.Write(append(data, '\n')); err != nil {
			return fmt.Errorf("write forked entry: %w", err)
		}
	}
	return w.Flush()
}
