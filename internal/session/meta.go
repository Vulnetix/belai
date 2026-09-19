package session

import (
	"encoding/json"
	"fmt"
	"time"
)

// EntryTypeSessionMeta is the session entry type carrying per-session metadata
// (working directory, schema version, origin/resume links, active carrier).
const EntryTypeSessionMeta = "session_meta"

// SchemaVersion is the current session_meta schema. Files with no session_meta
// entry are treated as schema 1.
const SchemaVersion = 2

// Meta is the payload of a session_meta entry. It serialises as JSON in the
// entry's Content, matching plan_state / goal_state / todo_list, rather than
// the free-form Meta map used by assistant entries.
type Meta struct {
	Schema        int    `json:"schema"`
	Cwd           string `json:"cwd,omitempty"`
	Version       string `json:"version,omitempty"`
	CreatedAt     int64  `json:"createdAt,omitempty"`
	ResumedFrom   string `json:"resumedFrom,omitempty"`
	OriginCwd     string `json:"originCwd,omitempty"`
	ActivePlan    string `json:"activePlan,omitempty"`
	ActiveGoal    string `json:"activeGoal,omitempty"`
	ActiveProfile string `json:"activeProfile,omitempty"`
	Mode          string `json:"mode,omitempty"`
	// RepoMapHead is the repository HEAD the session already consulted for its
	// repo map, so a resumed session knows whether to re-check the registry.
	RepoMapHead string `json:"repoMapHead,omitempty"`
}

// ToEntry renders Meta as a session entry. Schema defaults to the current
// version when not set.
func (m Meta) ToEntry(parentID string) Entry {
	if m.Schema == 0 {
		m.Schema = SchemaVersion
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().UnixMilli()
	}
	data, err := json.Marshal(m)
	if err != nil {
		data = []byte("{}")
	}
	return Entry{
		ID:       MustID(),
		ParentID: parentID,
		Type:     EntryTypeSessionMeta,
		Content:  string(data),
	}
}

// MetaFromEntry parses a session_meta entry back into Meta.
func MetaFromEntry(e Entry) (Meta, error) {
	var m Meta
	if err := json.Unmarshal([]byte(e.Content), &m); err != nil {
		return Meta{}, fmt.Errorf("parse session meta: %w", err)
	}
	return m, nil
}

// LatestMeta merges every session_meta entry in order, last non-zero field
// wins. Merging (rather than last-entry-wins) lets the TUI append a one-field
// update when /mode, /plan or /profile changes instead of rewriting the whole
// record.
func LatestMeta(entries []Entry) (Meta, bool) {
	var out Meta
	found := false
	for _, e := range entries {
		if e.Type != EntryTypeSessionMeta {
			continue
		}
		m, err := MetaFromEntry(e)
		if err != nil {
			continue // malformed entry skipped, not fatal
		}
		if m.Schema != 0 {
			out.Schema = m.Schema
		}
		if m.Cwd != "" {
			out.Cwd = m.Cwd
		}
		if m.Version != "" {
			out.Version = m.Version
		}
		if m.CreatedAt != 0 {
			out.CreatedAt = m.CreatedAt
		}
		if m.ResumedFrom != "" {
			out.ResumedFrom = m.ResumedFrom
		}
		if m.OriginCwd != "" {
			out.OriginCwd = m.OriginCwd
		}
		if m.ActivePlan != "" {
			out.ActivePlan = m.ActivePlan
		}
		if m.ActiveGoal != "" {
			out.ActiveGoal = m.ActiveGoal
		}
		if m.ActiveProfile != "" {
			out.ActiveProfile = m.ActiveProfile
		}
		if m.Mode != "" {
			out.Mode = m.Mode
		}
		if m.RepoMapHead != "" {
			out.RepoMapHead = m.RepoMapHead
		}
		found = true
	}
	return out, found
}
