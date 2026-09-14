package session

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// Entry is a single JSONL session record. Entries form a tree through
// ID/ParentID links: the root entry of a session has an empty ParentID and
// every other entry points at the entry it branches from.
type Entry struct {
	ID        string         `json:"id"`
	ParentID  string         `json:"parentId,omitempty"`
	Type      string         `json:"type,omitempty"` // user, assistant, tool, system, ...
	Role      string         `json:"role,omitempty"`
	Content   string         `json:"content,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// NewID returns a random RFC 4122 version 4 UUID string.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// MustID is NewID that panics on failure. It exists for callers that have no
// meaningful error path (e.g. tests and interactive setup).
func MustID() string {
	id, err := NewID()
	if err != nil {
		panic(err)
	}
	return id
}

// Node is a tree node wrapping an entry and its children.
type Node struct {
	Entry    Entry
	Children []*Node
}

// BuildTree turns a flat, append-ordered entry list into a forest of nodes.
// Roots are entries whose ParentID is empty or points at a missing entry
// (orphans are surfaced as roots rather than silently dropped).
func BuildTree(entries []Entry) []*Node {
	nodes := make(map[string]*Node, len(entries))
	order := make([]*Node, 0, len(entries))
	for _, e := range entries {
		if _, exists := nodes[e.ID]; exists {
			continue // duplicate id: keep first
		}
		n := &Node{Entry: e}
		nodes[e.ID] = n
		order = append(order, n)
	}
	var roots []*Node
	for _, n := range order {
		if n.Entry.ParentID == "" {
			roots = append(roots, n)
			continue
		}
		if p, ok := nodes[n.Entry.ParentID]; ok {
			p.Children = append(p.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	return roots
}

// DisplayName derives a short human-facing name for a session: the first
// user-typed message, flattened and truncated, or a short session-id prefix
// when there is no user content.
func DisplayName(entries []Entry, sessionID string) string {
	for _, e := range entries {
		if e.Type == "user" || e.Role == "user" {
			name := strings.Join(strings.Fields(e.Content), " ")
			if name == "" {
				continue
			}
			if len(name) > 60 {
				name = name[:57] + "..."
			}
			return name
		}
	}
	if len(sessionID) >= 8 {
		return sessionID[:8]
	}
	return sessionID
}
