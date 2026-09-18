package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/session"
	"github.com/vulnetix/signet/internal/transcript"
)

// resumeSession loads a stored session into the running TUI. Same-project
// resume appends in place; cross-project resume rehydrates the history
// read-only and forks the continuation into the current project's key,
// recording resumedFrom and originCwd. The TUI is reloaded in place — the same
// process, no re-exec.
func (a *App) resumeSession(key session.Key, sessionID string) tea.Cmd {
	// 1. Stop the world: an in-flight agent goroutine must not keep writing
	// into the newly loaded transcript.
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	a.endPhase()
	a.events = nil
	a.pendingEvent = agent.Event{}
	a.pendingEventSet = false
	a.preSend = false

	// 2. Read. On error nothing is mutated; the current session survives a
	// bad id.
	id, err := a.store.ResolveIn(key, sessionID)
	if err != nil {
		a.addSystem("resume: " + err.Error())
		return nil
	}
	entries, err := a.store.ReadFrom(key, id)
	if err != nil {
		a.addSystem("resume: " + err.Error())
		return nil
	}
	if len(entries) == 0 {
		a.addSystem("resume: session has no entries")
		return nil
	}

	// 3. Destination. Same key appends in place; a different key forks into
	// the current project rather than widening the sandbox to another tree.
	cur, _ := session.KeyFor(a.workdir)
	crossProject := key != cur
	newID := id
	originCwd := ""
	if m, ok := session.LatestMeta(entries); ok {
		originCwd = m.Cwd
	}
	if crossProject {
		newID = session.MustID()
		if err := a.store.ForkAcross(key, id, cur, newID); err != nil {
			a.addSystem("resume: " + err.Error())
			return nil
		}
	}

	// 4. Clear every in-flight and per-session field a fresh transcript must
	// not inherit, matching startNewSession plus the fields it predates.
	a.clearForResume()

	// 5. Drop the cached agent session and reset the working directory so the
	// next send re-resolves the carrier and reseals the system prompt.
	a.invalidateAgentSession()

	// 6. Adopt the session identity.
	a.sessionID = newID
	a.sessionKey = cur
	a.sessionWorkdir = a.workdir

	// 7. Continue the chain. startNewSession and applyCompaction zero
	// lastEntryID because they begin a new file; resuming must not, or the
	// file grows a second root and BuildTree returns two forests.
	a.lastEntryID = entries[len(entries)-1].ID

	// Record the fork provenance after the chain pointer is set so the new
	// meta entry branches from the copied tail rather than the old session.
	if crossProject {
		a.appendEntry(session.Meta{
			Schema:      session.SchemaVersion,
			Cwd:         a.workdir,
			ResumedFrom: id,
			OriginCwd:   originCwd,
		}.ToEntry(a.lastEntryID))
	}

	// 8. Rehydrate the transcript and its state.
	r := rehydrateSession(entries)
	a.messages = r.Messages
	a.sessionName = r.Name
	a.nameRequested = r.Name != "" // stops shouldAutoName renaming a named session
	a.summary = r.Summary
	a.parentSession = r.Parent
	a.rehydrateTodos(entries)
	a.persistedUpTo = len(a.messages)

	// 9. Model/provider/mode restore is applied in a later phase; the current
	// configuration and mode remain in force for now.

	// 10. Persist the active session id so the footer and state.json agree.
	a.saveSession()

	// 11. Backfill: a same-key schema-1 file gains a session_meta with the
	// current workdir, self-healing the recoverable-path metadata.
	if !crossProject && a.isSchema1(entries) {
		a.appendEntry(session.Meta{Schema: session.SchemaVersion, Cwd: a.workdir}.ToEntry(a.lastEntryID))
	}

	// 12. Report.
	a.reportResume(r, id, crossProject)

	// 13. The compaction offer is wired in a later phase; for now resume just
	// refreshes the footer.
	a.refreshFooter()
	return nil
}

// resumeByID resolves a full or partial id, preferring the current project and
// then scanning every project on disk.
func (a *App) resumeByID(idOrPrefix string) tea.Cmd {
	cur, _ := session.KeyFor(a.workdir)
	if id, err := a.store.ResolveIn(cur, idOrPrefix); err == nil {
		return a.resumeSession(cur, id)
	}
	key, id, err := a.store.ResolveAnywhere(cur, idOrPrefix)
	if err != nil {
		a.addSystem("resume: " + err.Error())
		return nil
	}
	return a.resumeSession(key, id)
}

// clearForResume resets every field a resumed transcript must not inherit.
func (a *App) clearForResume() {
	a.sessionName = ""
	a.parentSession = ""
	a.nameRequested = false
	a.messages = nil
	a.summary = ""
	a.usage = nil
	a.usageStale = false
	a.todos = nil
	a.namedAgent = ""
	a.namedAgentTools = nil
	a.agentPickerOpen = false
	a.hover = hoverTarget{}
	a.mousePresent = false
	a.saveFileMode = false
	a.saveFileMsg = -1
	a.loadAgents()

	// Context metering.
	a.est = transcript.Estimate{}
	a.estKey = ""
	a.estInit = false

	// Plan/execute one-shot state.
	a.lastPlanText = ""
	a.pendingPlanExecute = false
	a.planExecuteName = ""
	a.pendingPlanRevision = 0
	a.pendingDirective = ""

	// Attachments.
	a.attachments = map[int]*attachment{}
	a.attachOrder = nil
	a.attachSeq = 0

	// Prompt history cycling.
	a.historyActive = false
	a.historyQuery = ""
	a.historyOriginal = ""
	a.historyIndex = 0
	a.historyResults = nil

	// Selection state.
	a.sel = selection{}
	a.lastBody = ""
	a.lastFrame = frame{}

	a.pending = ""
	a.follow = true
}

// isSchema1 reports whether a session file has no session_meta entry (schema
// 1 is implicit for such files).
func (a *App) isSchema1(entries []session.Entry) bool {
	for _, e := range entries {
		if e.Type == session.EntryTypeSessionMeta {
			return false
		}
	}
	return true
}

// reportResume adds the resume system lines.
func (a *App) reportResume(r rehydrated, id string, crossProject bool) {
	line := fmt.Sprintf("resumed %s · %d messages", shortID(id), len(r.Messages))
	if crossProject {
		line += " · forked into this project"
	}
	a.addSystem(line)
	if r.TextOnly {
		a.addSystem("tool activity from before this build is not in the restored history")
	}
	if r.Dropped > 0 {
		a.addSystem(fmt.Sprintf("%d unpaired tool calls/results were dropped from the restored history", r.Dropped))
	}
}
