package tui

import (
	"fmt"
	"strings"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/tui/components"
)

// The subagent roster lives here: insertion-ordered chips that persist across
// turns, key handling for the f8 strip, and transcript filtering. app.go keeps
// only the wiring (the event handlers, the f8 global key, and refreshFooter).

// upsertSubagent inserts or updates one roster chip in insertion order. Chips
// are removed only by an explicit dismiss; terminal chips stay until then.
func (a *App) upsertSubagent(u agent.SubagentUpdate) {
	if u.ID == "" {
		return
	}
	if a.subagentIdx == nil {
		a.subagentIdx = map[string]int{}
	}
	idx, ok := a.subagentIdx[u.ID]
	if !ok {
		idx = len(a.subagents)
		a.subagents = append(a.subagents, components.SubagentChip{ID: u.ID, Label: u.Label, State: u.State})
		a.subagentIdx[u.ID] = idx
		return
	}
	if u.Label != "" {
		a.subagents[idx].Label = u.Label
	}
	a.subagents[idx].State = u.State
}

// handleSubagentUpdate applies one roster delta and, for lifecycle states,
// writes a system line so a reader can see the fan-out's progress.
func (a *App) handleSubagentUpdate(u agent.SubagentUpdate) {
	if strings.HasPrefix(u.ID, "bg:") {
		// A background agent's pool lease announces every turn. Its lifecycle
		// comes from the manager, so only the wait for a slot and the start of
		// the turn are taken from here, and neither is a main-thread line.
		if u.State == "queued" || u.State == "running" {
			a.upsertSubagent(u)
			a.noteAgentState(u.ID, u.Label, u.State)
		}
		return
	}
	a.upsertSubagent(u)
	a.noteAgentState(u.ID, u.Label, u.State)
	switch u.State {
	case "queued", "running", "done", "cancelled", "failed":
		a.addSystem(fmt.Sprintf("subagent %s: %s", subagentDisplayLabel(u), u.State))
	}
}

// subagentDisplayLabel renders a stable, human-facing label for a subagent:
// the ID plus, when it differs, the reference it is investigating.
func subagentDisplayLabel(u agent.SubagentUpdate) string {
	if u.Label == "" || u.Label == u.ID {
		return u.ID
	}
	return fmt.Sprintf("%s (%s)", u.ID, u.Label)
}

// subagentGutterLabel maps a subagent ID to the dim transcript gutter label:
// e→explore, g→survey, c→clarify, bg:→background agent name.
func subagentGutterLabel(id string) string {
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

// hasLiveSubagents reports whether any roster chip is queued or running.
func (a *App) hasLiveSubagents() bool {
	for _, c := range a.subagents {
		if c.State == "queued" || c.State == "running" {
			return true
		}
	}
	return false
}

// exploringSummary reports the explore-phase counters: how many subagents are
// terminal, the fan-out total, and the reference of the first running one.
func (a *App) exploringSummary() (done, total int, ref string) {
	total = len(a.subagents)
	for _, c := range a.subagents {
		switch c.State {
		case "done", "cancelled", "failed":
			done++
		case "running":
			if ref == "" {
				ref = c.Label
			}
		}
	}
	return done, total, ref
}

// cancelSubagent cancels a running/queued subagent through the shared pool. The
// roster flips to cancelled when the pool's terminal delta lands.
func (a *App) cancelSubagent(id string) {
	if a.agentPool != nil {
		a.agentPool.Cancel(id)
	}
}

// dismissSubagent removes a terminal chip and clears the transcript filter if
// it pointed at that subagent.
func (a *App) dismissSubagent(id string) {
	idx, ok := a.subagentIdx[id]
	if !ok {
		return
	}
	a.subagents = append(a.subagents[:idx], a.subagents[idx+1:]...)
	delete(a.subagentIdx, id)
	for i, c := range a.subagents {
		a.subagentIdx[c.ID] = i
	}
	if a.threadFilter == id {
		a.threadFilter = ""
	}
}

// filteredMessages returns the transcript rows for the current thread filter:
// every message when unfiltered, otherwise only rows whose SubagentID matches,
// plus a leading banner naming the filter.
//
// A background agent's thread is its own conversation, so the main view leaves
// it out; the footer pulse says what the agent is doing, and following it
// shows the whole thread.
func (a *App) filteredMessages() []components.Message {
	if a.threadFilter == "" {
		if !a.hasBackgroundRows() {
			return a.messages
		}
		out := make([]components.Message, 0, len(a.messages))
		for _, m := range a.messages {
			if !strings.HasPrefix(m.SubagentID, "bg:") {
				out = append(out, m)
			}
		}
		return out
	}
	var out []components.Message
	out = append(out, components.Message{
		Role:    "system",
		Content: "following " + subagentGutterLabel(a.threadFilter) + " · esc returns to main",
	})
	for _, m := range a.messages {
		if m.SubagentID == a.threadFilter {
			out = append(out, m)
		}
	}
	return out
}

// hasBackgroundRows reports whether any transcript row belongs to a
// background agent's thread.
func (a *App) hasBackgroundRows() bool {
	for _, m := range a.messages {
		if strings.HasPrefix(m.SubagentID, "bg:") {
			return true
		}
	}
	return false
}

// rebuildSubagentsFromMessages reconstructs the roster chips from rehydrated
// subagent activity rows so /resume brings finished chips (and their filtered
// threads) back. Every subagent is terminal after resume, so they all load as
// done.
func (a *App) rebuildSubagentsFromMessages() {
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	for _, m := range a.messages {
		if m.SubagentID == "" {
			continue
		}
		if _, ok := a.subagentIdx[m.SubagentID]; ok {
			continue
		}
		a.subagentIdx[m.SubagentID] = len(a.subagents)
		a.subagents = append(a.subagents, components.SubagentChip{
			ID:    m.SubagentID,
			Label: m.SubagentID,
			State: "done",
		})
	}
}
