package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/signet/internal/agent"
	"github.com/vulnetix/signet/internal/agentprofile"
	"github.com/vulnetix/signet/internal/bgagent"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/tui/components"
)

// The agent ledger is the one record of what every subagent and background
// agent is doing: its loop state, the tool it is inside, how many tools it has
// run, and an audit trail of each step. The footer pulse, the runs panel and
// the /agents screen all read it; nothing in it ever reaches a model.

// auditCap bounds the audit trail. The oldest entries fall off first.
const auditCap = 1000

// auditTextCap bounds one audit row, in runes.
const auditTextCap = 160

// agentLedger holds live loop state per agent id plus the audit trail.
type agentLedger struct {
	agents map[string]*agentLive
	order  []string
	audit  []auditEntry
	// text accumulates a background agent's streamed reply until a tool call or
	// the end of the turn flushes it into the agent's thread as one row.
	text map[string]*strings.Builder
	// pulse steps the footer glyph once per tick while an agent is running.
	pulse   int
	ticking bool
}

// agentLive is one agent's loop state.
type agentLive struct {
	ID      string
	Label   string
	Kind    string // background | explore | survey | clarify | subagent
	State   string
	Started time.Time
	Ended   time.Time
	Iter    int
	MaxIter int
	Tool    string // the tool running now; "" between tools
	Tools   int
	Errors  int
	Last    string // the latest line of output, flattened
	// announced marks a background agent whose end the main thread has heard.
	announced bool
}

// auditEntry is one step in the audit trail.
type auditEntry struct {
	At   time.Time
	ID   string
	What string // state | tool | result | reply | error
	Text string
}

// agentPulseMsg steps the footer glyph and refreshes background loop state.
type agentPulseMsg struct{}

// agentKind names the family an agent id belongs to.
func agentKind(id string) string {
	switch {
	case strings.HasPrefix(id, "bg:"):
		return "background"
	case strings.HasPrefix(id, "e"):
		return "explore"
	case strings.HasPrefix(id, "g"):
		return "survey"
	case strings.HasPrefix(id, "c"):
		return "clarify"
	default:
		return "subagent"
	}
}

// liveAgent returns the ledger record for id, creating it on first sight.
func (a *App) liveAgent(id, label string) *agentLive {
	if a.ledger.agents == nil {
		a.ledger.agents = map[string]*agentLive{}
	}
	if l, ok := a.ledger.agents[id]; ok {
		if label != "" && label != id {
			l.Label = label
		}
		return l
	}
	if label == "" {
		label = subagentGutterLabel(id)
	}
	l := &agentLive{ID: id, Label: label, Kind: agentKind(id), Started: time.Now()}
	a.ledger.agents[id] = l
	a.ledger.order = append(a.ledger.order, id)
	return l
}

// lookupLive returns the ledger record for id without creating one.
func (a *App) lookupLive(id string) (*agentLive, bool) {
	l, ok := a.ledger.agents[id]
	return l, ok
}

// noteAgentState records a lifecycle state. A repeated state is not audited.
func (a *App) noteAgentState(id, label, state string) {
	l := a.liveAgent(id, label)
	if l.State == state {
		return
	}
	l.State = state
	switch state {
	case "running", "queued":
		l.Ended = time.Time{}
	case "done", "failed", "cancelled", "stopped":
		l.Ended = time.Now()
		l.Tool = ""
	}
	a.noteAudit(id, "state", state)
}

// noteAgentTool records a tool start.
func (a *App) noteAgentTool(id, name, args string) {
	l := a.liveAgent(id, "")
	l.Tool = name
	l.Tools++
	text := name
	if args = auditLine(args); args != "" {
		text += " " + args
	}
	a.noteAudit(id, "tool", text)
}

// noteAgentResult records a tool result and clears the running tool.
func (a *App) noteAgentResult(id, name, result string) {
	l := a.liveAgent(id, "")
	l.Tool = ""
	first := firstLine(result)
	if first != "" {
		l.Last = first
	}
	text := name
	if first != "" {
		text += " → " + first
	}
	a.noteAudit(id, "result", text)
}

// noteAgentError records a failure.
func (a *App) noteAgentError(id string, err error) {
	if err == nil {
		return
	}
	l := a.liveAgent(id, "")
	l.Errors++
	l.Last = auditLine(err.Error())
	a.noteAudit(id, "error", err.Error())
}

// noteAgentText accumulates a streamed reply delta.
func (a *App) noteAgentText(id, delta string) {
	if delta == "" {
		return
	}
	if a.ledger.text == nil {
		a.ledger.text = map[string]*strings.Builder{}
	}
	b, ok := a.ledger.text[id]
	if !ok {
		b = &strings.Builder{}
		a.ledger.text[id] = b
	}
	b.WriteString(delta)
	if last := lastLine(b.String()); last != "" {
		a.liveAgent(id, "").Last = last
	}
}

// flushAgentText returns and clears the reply streamed so far, auditing it.
func (a *App) flushAgentText(id string) string {
	b, ok := a.ledger.text[id]
	if !ok {
		return ""
	}
	delete(a.ledger.text, id)
	text := strings.TrimSpace(b.String())
	if text != "" {
		a.noteAudit(id, "reply", text)
	}
	return text
}

// noteAudit appends one row to the bounded audit trail.
func (a *App) noteAudit(id, what, text string) {
	a.ledger.audit = append(a.ledger.audit, auditEntry{At: time.Now(), ID: id, What: what, Text: auditLine(text)})
	if over := len(a.ledger.audit) - auditCap; over > 0 {
		a.ledger.audit = append(a.ledger.audit[:0:0], a.ledger.audit[over:]...)
	}
}

// auditLine flattens text to one display line: control and bidi runes go,
// whitespace runs collapse, and the result is capped. Agent output is
// untrusted, so it never keeps a rune that could move the cursor or reorder
// the row.
func auditLine(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if isBidiRune(r) {
			continue
		}
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			n++
			space = false
		}
		if n >= auditTextCap {
			return strings.TrimSpace(b.String()) + "…"
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func isBidiRune(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}

// firstLine returns the first non-blank line of s, flattened.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if l := auditLine(line); l != "" {
			return l
		}
	}
	return ""
}

// lastLine returns the last non-blank line of s, flattened.
func lastLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := auditLine(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// liveAgents returns the ledger in display order: agents still working first,
// then finished ones, each group in the order they were first seen.
func (a *App) liveAgents() []*agentLive {
	out := make([]*agentLive, 0, len(a.ledger.order))
	for _, id := range a.ledger.order {
		out = append(out, a.ledger.agents[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		return agentStateRank(out[i].State) < agentStateRank(out[j].State)
	})
	return out
}

func agentStateRank(state string) int {
	switch state {
	case "running":
		return 0
	case "queued":
		return 1
	case "paused", "idle":
		return 2
	default:
		return 3
	}
}

// agentCounts reports how many agents are working, and how many are parked.
func (a *App) agentCounts() (live, parked int) {
	for _, l := range a.ledger.agents {
		switch l.State {
		case "running", "queued":
			live++
		case "paused", "idle":
			parked++
		}
	}
	return live, parked
}

// pulseGlyph is the one-cell state mark used by the footer and /agents.
func (a *App) pulseGlyph(state string) string {
	switch state {
	case "running":
		if !a.settings.SpinnerEnabled() {
			return "●"
		}
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		return frames[a.ledger.pulse%len(frames)]
	case "queued":
		return "○"
	case "paused":
		return "‖"
	case "idle":
		return "◌"
	case "done":
		return "✓"
	case "failed", "cancelled", "stopped":
		return "✗"
	default:
		return "·"
	}
}

// agentElapsed is the agent's run time, frozen once it has ended.
func agentElapsed(l *agentLive) time.Duration {
	end := l.Ended
	if end.IsZero() {
		end = time.Now()
	}
	d := end.Sub(l.Started)
	if d < 0 {
		return 0
	}
	return d
}

// compactDuration renders a duration in at most two units: 8s, 2m05s, 1h02m.
func compactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// agentIterLabel renders "iter 3/12", "iter 3", or "" before the first pass.
func agentIterLabel(l *agentLive) string {
	switch {
	case l.Iter <= 0:
		return ""
	case l.MaxIter > 0:
		return fmt.Sprintf("iter %d/%d", l.Iter, l.MaxIter)
	default:
		return fmt.Sprintf("iter %d", l.Iter)
	}
}

// agentStep says what the agent is doing right now.
func agentStep(l *agentLive) string {
	switch {
	case l.Tool != "":
		return "⚙ " + l.Tool
	case l.State == "running":
		return "thinking"
	default:
		return ""
	}
}

// agentDetail is the short suffix shown beside a roster chip.
func (a *App) agentDetail(id string) string {
	l, ok := a.lookupLive(id)
	if !ok {
		return ""
	}
	var parts []string
	if l.Tool != "" {
		parts = append(parts, l.Tool)
	}
	if it := agentIterLabel(l); it != "" {
		parts = append(parts, strings.TrimPrefix(it, "iter "))
	}
	return strings.Join(parts, " ")
}

// agentSummary is the one-line status used by the runs panel.
func (a *App) agentSummary(id, state string) string {
	l, ok := a.lookupLive(id)
	if !ok {
		return state
	}
	parts := []string{state}
	if step := agentStep(l); step != "" {
		parts = append(parts, step)
	}
	if it := agentIterLabel(l); it != "" {
		parts = append(parts, it)
	}
	if l.Tools > 0 {
		parts = append(parts, countOf(l.Tools, "tool"))
	}
	parts = append(parts, compactDuration(agentElapsed(l)))
	return strings.Join(parts, " · ")
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// armAgentPulse schedules the next pulse while an agent is working. Only one
// tick is ever pending.
func (a *App) armAgentPulse() tea.Cmd {
	if a.ledger.ticking {
		return nil
	}
	if live, _ := a.agentCounts(); live == 0 && !a.bgRunning() {
		return nil
	}
	a.ledger.ticking = true
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return agentPulseMsg{} })
}

// handleAgentPulse steps the glyph, refreshes background state and re-arms.
func (a *App) handleAgentPulse() tea.Cmd {
	a.ledger.ticking = false
	a.ledger.pulse++
	a.syncBackgroundLedger()
	return a.armAgentPulse()
}

// bgRunning reports whether a background agent is mid-run. A finished agent
// stays in the manager, so presence alone would keep the pulse ticking.
func (a *App) bgRunning() bool {
	if a.bgManager == nil {
		return false
	}
	for _, s := range a.bgManager.List() {
		if s.State == bgagent.StateRunning {
			return true
		}
	}
	return false
}

// syncBackgroundLedger copies each background agent's loop state from the
// manager: its state between turns and its iteration count.
func (a *App) syncBackgroundLedger() {
	if a.bgManager == nil {
		return
	}
	seen := map[string]bool{}
	for _, s := range a.bgManager.List() {
		id := "bg:" + s.Name
		seen[id] = true
		l := a.liveAgent(id, s.Name)
		l.Iter = s.Iteration
		if inst, ok := a.bgManager.Lookup(s.Name); ok {
			l.MaxIter = agentMaxIter(inst.Profile, a.settings)
		}
		state := string(s.State)
		if s.State == bgagent.StateRunning && l.State == "queued" {
			state = "queued"
		}
		a.noteAgentState(id, s.Name, state)
		a.setRosterState(id, s.Name, state)
	}
	// An agent the manager no longer holds has finished or been stopped.
	for id, l := range a.ledger.agents {
		if l.Kind != "background" || seen[id] {
			continue
		}
		if agentStateRank(l.State) < 3 {
			a.noteAgentState(id, "", "done")
			a.setRosterState(id, "", "done")
		}
	}
}

// agentMaxIter is the iteration ceiling a profile runs under, 0 when it has
// none (scheduled and monitor agents run until stopped).
func agentMaxIter(p agentprofile.AgentProfile, s config.Settings) int {
	switch p.Mode {
	case agentprofile.ModeSingle:
		return 1
	case agentprofile.ModeLoop:
		if p.MaxIterations > 0 {
			return p.MaxIterations
		}
		return s.Resilience.MaxIterationsOr(config.DefaultMaxIterations)
	default:
		return p.MaxIterations
	}
}

// setRosterState keeps the roster chip for a background agent in step with the
// manager, adding the chip if the pool never announced it.
func (a *App) setRosterState(id, label, state string) {
	if label == "" {
		label = strings.TrimPrefix(id, "bg:")
	}
	a.upsertSubagent(agent.SubagentUpdate{ID: id, Label: label, State: state})
}

// noteAgentStarted registers a background agent that was just started: its
// ledger record, its roster chip, and one line in the main thread.
func (a *App) noteAgentStarted(key string) tea.Cmd {
	id := "bg:" + key
	l := a.liveAgent(id, key)
	l.Started = time.Now()
	l.Ended = time.Time{}
	l.Tool = ""
	l.announced = false
	a.noteAgentState(id, key, "running")
	a.setRosterState(id, key, "running")
	a.addSystem("▸ " + key + " started in the background · f8 to follow")
	return tea.Batch(a.watchAgentEvents(key), a.armAgentPulse())
}

// noteAgentStopped records a stop the user asked for.
func (a *App) noteAgentStopped(key string) {
	id := "bg:" + key
	a.flushAgentThread(id)
	a.noteAgentState(id, key, "stopped")
	a.setRosterState(id, key, "cancelled")
	if l, ok := a.lookupLive(id); ok {
		l.announced = true
	}
	a.addSystem("■ " + key + " stopped")
}

// flushAgentThread moves a background agent's streamed reply into its thread.
func (a *App) flushAgentThread(id string) {
	if text := a.flushAgentText(id); text != "" {
		a.messages = append(a.messages, components.Message{Role: "system", Content: text, SubagentID: id})
	}
}

// handleBgAgentEvent routes one background-agent event. The agent's work goes
// to its own thread, which is hidden from the main view until it is followed;
// the main thread only hears that the agent finished or failed.
func (a *App) handleBgAgentEvent(m bgAgentEventMsg) tea.Cmd {
	name := m.AgentName
	id := "bg:" + name
	switch m.Kind {
	case agent.EventErrorKind:
		if m.Err != nil {
			a.flushAgentThread(id)
			a.noteAgentError(id, m.Err)
			a.messages = append(a.messages, components.Message{Role: "system", Content: "error: " + m.Err.Error(), SubagentID: id})
			a.addSystem("■ " + name + " error: " + auditLine(m.Err.Error()))
		}
	case agent.EventTextKind:
		a.noteAgentText(id, m.Text)
	case agent.EventToolStartKind:
		a.flushAgentThread(id)
		a.noteAgentTool(id, m.ToolName, m.ToolArgs)
		a.messages = append(a.messages, components.Message{
			Role:       "tool",
			SubagentID: id,
			ToolName:   m.ToolName,
			ToolArgs:   m.ToolArgs,
			ToolCallID: m.ToolCallID,
			StartedAt:  time.Now(),
		})
	case agent.EventToolResultKind:
		a.noteAgentResult(id, m.ToolName, m.ToolResult)
		a.settleAgentToolRow(id, m.ToolName, m.ToolCallID, m.ToolResult)
	case agent.EventSubagentKind:
		// The pool announces each background turn through the roster shape.
		if m.Subagent != nil {
			a.handleSubagentUpdate(*m.Subagent)
		}
	case agent.EventDoneKind:
		a.flushAgentThread(id)
	}
	a.syncBackgroundLedger()
	if a.bgManager != nil {
		if inst, ok := a.bgManager.Lookup(name); ok && inst.State != bgagent.StateDone {
			return tea.Batch(a.watchAgentEvents(name), a.armAgentPulse())
		}
	}
	// The agent's loop has ended. The main thread hears it once, on the done
	// that closes the stream.
	if l, ok := a.lookupLive(id); ok && m.Kind == agent.EventDoneKind && !l.announced {
		l.announced = true
		a.addSystem(fmt.Sprintf("■ %s done · %s · %s · f8 to read", name, countOf(l.Tools, "tool"), compactDuration(agentElapsed(l))))
	}
	return a.armAgentPulse()
}

// settleAgentToolRow fills in the result on the thread row its call opened.
func (a *App) settleAgentToolRow(id, name, callID, result string) {
	for i := len(a.messages) - 1; i >= 0; i-- {
		mm := a.messages[i]
		if mm.SubagentID != id || mm.Role != "tool" {
			continue
		}
		if callID != "" && mm.ToolCallID != callID {
			continue
		}
		if callID == "" && mm.Content != "" {
			continue
		}
		mm.SetContent(result)
		mm.Status = toolResultStatus(name, result)
		a.messages[i] = mm
		return
	}
}

// followedPulse is the footer's loop line for the followed thread, nil when
// the main thread is on show.
func (a *App) followedPulse() *components.AgentPulse {
	if a.threadFilter == "" {
		return nil
	}
	l, ok := a.lookupLive(a.threadFilter)
	if !ok {
		return &components.AgentPulse{Label: subagentGutterLabel(a.threadFilter), State: "done", Glyph: a.pulseGlyph("done")}
	}
	return &components.AgentPulse{
		Glyph:   a.pulseGlyph(l.State),
		Label:   l.Label,
		State:   l.State,
		Step:    agentStep(l),
		Iter:    agentIterLabel(l),
		Tools:   l.Tools,
		Errors:  l.Errors,
		Elapsed: compactDuration(agentElapsed(l)),
		Last:    l.Last,
	}
}

// followAgent filters the transcript to one agent's thread and returns to chat.
func (a *App) followAgent(id string) {
	a.threadFilter = id
	a.follow = true
	a.popToChat()
}
