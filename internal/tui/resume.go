package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/modelinfo"
	"github.com/vulnetix/signet/internal/run"
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
	a.publishSessionID()
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
	a.rebuildSubagentsFromMessages()
	a.persistedUpTo = len(a.messages)
	// Seed the exit card's token fact from the persisted usage meta so the
	// resumed session reports the whole conversation, not only what happens
	// after this point.
	a.tokensTotal = 0
	for _, e := range entries {
		if u := usageFromMeta(e.Meta); u != nil {
			a.tokensTotal += u.TotalTokens
		}
	}
	// Seed the resumed banner variant and the exit-card clock.
	a.bannerResumed = r.Name
	if a.bannerResumed == "" {
		a.bannerResumed = shortID(a.sessionID)
	}
	a.bannerRestoredTurns = restoredTurnCount(entries)
	a.startedAt = time.Now()

	// 9. Restore model/provider/effort (CLI flag > session record > state >
	// settings/env), the recorded mode, and plan/goal carrier state.
	a.restoreModelProvider(r)
	a.restoreMode(r)
	a.restorePlanGoal(r, crossProject)

	// 10. Persist the active session id so the footer and state.json agree.
	a.saveSession()

	// 11. Backfill: a same-key schema-1 file gains a session_meta with the
	// current workdir, self-healing the recoverable-path metadata.
	if !crossProject && a.isSchema1(entries) {
		a.appendEntry(session.Meta{Schema: session.SchemaVersion, Cwd: a.workdir}.ToEntry(a.lastEntryID))
	}

	// 12. Report.
	a.reportResume(r, id, crossProject)

	// 13. Offer compaction when the resumed transcript crosses the threshold,
	// then refresh the footer.
	a.refreshFooter()
	return a.offerCompactionCmd()
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
	a.agentExplicit = false
	a.agentPickerOpen = false
	a.agentPickerSubmit = false
	a.hover = hoverTarget{}
	a.mousePresent = false
	a.saveFileMode = false
	a.saveFileMsg = -1
	a.clearLoadedPrompt()
	a.loadAgents()

	// Subagent roster and thread filter are session state, not global state.
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	a.threadFilter = ""
	a.runsOpen = false
	a.runsFocus = false
	a.runsTab = tabActivity
	a.runsSel = 0
	a.runsScroll = 0

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

// restoreModelProvider applies the recorded provider/model/effort with the
// precedence: explicit CLI flag > session record > config.State >
// settings/env. A recorded provider that no longer resolves to a configured
// status is kept current (and warned) so a resumed session never becomes
// unsendable.
func (a *App) restoreModelProvider(r rehydrated) {
	provider, model, effort := a.cfg.Provider, a.cfg.Model, a.settings.Effort

	if a.flags.Provider == "" {
		if r.Provider != "" {
			provider = r.Provider
		} else if a.state.Provider != "" {
			provider = a.state.Provider
		}
	}
	if a.flags.Model == "" {
		if r.Model != "" {
			model = r.Model
		} else if a.state.Model != "" {
			model = a.state.Model
		}
	}
	if a.flags.Effort == "" {
		if r.Effort != "" {
			effort = r.Effort
		} else if a.state.Effort != "" {
			effort = a.state.Effort
		}
	}

	if provider == a.cfg.Provider && model == a.cfg.Model && effort == a.settings.Effort {
		return
	}

	src := run.CredentialSource(run.EnvSource(os.Getenv))
	if a.resolver != nil {
		src = a.resolver
	}
	if _, status := run.Prepare(model, provider, src); !status.Configured {
		a.addSystem(fmt.Sprintf("recorded provider %q is not configured; keeping %q", provider, a.cfg.Provider))
		return
	}
	a.applyModelProvider(provider, model, effort)
}

// restoreMode restores the recorded operating mode (the session's own record,
// not state.LastMode, which is the last mode of any session).
func (a *App) restoreMode(r rehydrated) {
	if r.Mode == "" {
		return
	}
	a.mode = r.Mode
	a.modeSticky = true
	a.syncPlanMode()
}

// restorePlanGoal restores plan-mode stickiness and the active plan/goal/
// profile names. Execution is deliberately not re-armed: pendingPlanExecute is
// a one-shot consumed by the next send, and silently executing a plan because a
// previous process died mid-execution is the wrong default.
func (a *App) restorePlanGoal(r rehydrated, crossProject bool) {
	if r.Plan != nil && r.Plan.Enabled {
		a.mode = "plan"
		a.modeSticky = true
		var texts []string
		for _, td := range r.Plan.Todos {
			texts = append(texts, td.Text)
		}
		a.lastPlanText = strings.Join(texts, "\n")
		a.syncPlanMode()
	}

	plan := r.Meta.ActivePlan
	goal := r.Meta.ActiveGoal
	profile := r.Meta.ActiveProfile
	if crossProject {
		// Plans and goals live under the origin workdir; a forked session's
		// recorded names point at files that do not exist here.
		if plan != "" || goal != "" {
			a.addSystem("plan/goal names from the origin project are not available here")
		}
		plan, goal = "", ""
	}
	a.state.ActivePlan = plan
	a.state.ActiveGoal = goal
	a.state.ActiveProfile = profile
	a.setNamedAgent(profile)
	_ = config.SaveState(a.state)
}

const (
	// resumeCompactFraction is the share of the model's context window that,
	// once crossed by the resumed estimate, triggers the compaction offer.
	resumeCompactFraction = 0.5
	// resumeCompactFallbackTokens is the offer threshold when the model's
	// context window is unknown.
	resumeCompactFallbackTokens = 40_000
)

// offerCompactionCmd shows the post-resume compaction offer when the restored
// transcript crosses the context threshold. It reuses the footer's window
// resolution and the shared transcript estimate rather than inventing new
// machinery.
func (a *App) offerCompactionCmd() tea.Cmd {
	if a.classifier == nil {
		return nil
	}
	msgs := a.transcriptMessages()
	if !hasUserAndAssistant(msgs) {
		return nil
	}
	est := transcript.EstimateContext(msgs)
	window, ok := modelinfo.ResolveWith(a.cfg.Model, a.settings.ContextWindows, a.selectedModelWindow())
	threshold := resumeCompactFallbackTokens
	if ok {
		threshold = int(float64(window) * resumeCompactFraction)
	}
	if est.Tokens <= threshold {
		return nil
	}
	a.push(viewResumeCompact)
	return nil
}

func hasUserAndAssistant(msgs []transcript.Message) bool {
	user, assistant := false, false
	for _, m := range msgs {
		if m.Role == "user" {
			user = true
		}
		if m.Role == "assistant" {
			assistant = true
		}
	}
	return user && assistant
}

// restoredTurnCount counts the user entries in a resumed session, the same
// rule sessionInfoFromEntries uses for its cheap Turns signal.
func restoredTurnCount(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Type == "user" || e.Role == "user" {
			n++
		}
	}
	return n
}

// persistCarrierMeta appends a partial session_meta entry reflecting the
// current active carrier. LatestMeta's merge semantics mean later one-field
// updates layer over earlier ones rather than replacing them.
func (a *App) persistCarrierMeta() {
	a.appendEntry(session.Meta{
		Schema:        session.SchemaVersion,
		Mode:          a.mode,
		ActivePlan:    a.state.ActivePlan,
		ActiveGoal:    a.state.ActiveGoal,
		ActiveProfile: a.state.ActiveProfile,
		RepoMapHead:   a.repoMap.Head,
	}.ToEntry(a.lastEntryID))
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
