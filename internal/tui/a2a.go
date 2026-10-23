package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/a2a"
	"github.com/vulnetix/belai/internal/a2alink"
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repomemory"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tools"
	"github.com/vulnetix/belai/internal/tui/components"
)

// A2A messaging in the TUI (docs/a2a.md). The node tails the same-host bus
// on its own goroutine and hands the UI loop what passed the filter. The UI
// records every A2A row in the transcript (render-only, its own colour),
// tells the model about a matching broadcast with a harness directive, runs
// the side-turn that answers a message, and delivers a reply as a classified
// attachment tied to the question.

// a2aState is the App's A2A endpoint.
type a2aState struct {
	node   *a2a.Node
	relay  *a2a.Relay
	cancel context.CancelFunc
	// pending are directives for the next turn: broadcasts the model was not
	// running to hear.
	pending []string
	// ref is the A2A message the model is acting on, stamped on a
	// RepoMemoryWrite note.
	ref atomic.Pointer[repomemory.Stamp]
	// hostID is this host's id, for the repo memory stamp.
	hostID string
	mu     sync.Mutex
}

// a2aEventMsg carries one node event to the UI loop.
type a2aEventMsg a2a.Event

// a2aAdmittedMsg is a received message or reply after the classifier: body
// is the admitted text, or empty with the verdict that withheld it.
type a2aAdmittedMsg struct {
	ev       a2a.Event
	body     string
	withheld rolemanager.Sentinel
	err      error
}

// a2aSideMsg is a side-turn's answer to a message.
type a2aSideMsg struct {
	q   a2a.Envelope
	res agent.SideReplyResult
	err error
}

// startA2A opens the A2A node when a2a.enabled is on. It runs before session
// sync, which attaches the relay. Same-host messaging needs no login.
func (a *App) startA2A() {
	if a.a2a != nil || !a.settings.A2AEnabled() {
		return
	}
	g, err := config.GlobalDir()
	if err != nil {
		return
	}
	hostID, err := sessionsync.HostID(g)
	if err != nil {
		return
	}
	node, err := a2a.Open(a2a.Options{
		Dir:       a2a.Dir(g),
		HostID:    hostID,
		Hostname:  sessionsync.Hostname(),
		SessionID: a.sessionID,
		Profile:   a.namedAgent,
		Filter:    a.settings.A2AFilter(),
		Git:       a.gitSyncRaw(),
	})
	if err != nil {
		a.addSystem("A2A: " + err.Error())
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.a2a = &a2aState{node: node, cancel: cancel, hostID: hostID}
	go node.Run(ctx)
}

// a2aInbox attaches the relay to the A2A node, for a syncer being built with
// client: the inbox poll carries the organisation's envelopes and the
// session controls carry the blackout. nil when A2A is off.
func (a *App) a2aInbox(client *sessionsync.Client, hostID string, wake func()) *sessionsync.A2AInbox {
	if a.a2a == nil || client == nil {
		return nil
	}
	node := a.a2a.node
	relay := a2a.NewRelay(client, hostID, node.Bus(), node.Audit(), func() string { return node.Self().SessionID })
	relay.OnBlackout = func(bool) { node.NotifyState() }
	ctx, cancel := context.WithCancel(context.Background())
	prev := a.a2a.cancel
	a.a2a.cancel = func() { cancel(); prev() }
	go relay.Run(ctx)
	a.a2a.relay = relay
	node.SetRelay(relay)
	return &sessionsync.A2AInbox{After: relay.After, Deliver: func(items []sessionsync.A2AItem) {
		relay.Deliver(items)
		if wake != nil {
			wake()
		}
	}, Advance: relay.Advance}
}

// a2aControls applies the organisation's A2A blackout from the session
// controls.
func (a *App) a2aControls(next func(string, sessionsync.Controls)) func(string, sessionsync.Controls) {
	var relay *a2a.Relay
	if a.a2a != nil {
		relay = a.a2a.relay
	}
	if relay == nil {
		return next
	}
	return func(id string, c sessionsync.Controls) {
		if next != nil {
			next(id, c)
		}
		if c.A2ABlackout != nil {
			relay.SetBlackout(*c.A2ABlackout)
		}
	}
}

// stopA2A ends the session's broadcast and stops the node.
func (a *App) stopA2A() {
	if a.a2a == nil {
		return
	}
	a.a2a.node.Close()
	a.a2a.cancel()
}

// watchA2A waits for the next node event.
func (a *App) watchA2A() tea.Cmd {
	if a.a2a == nil {
		return nil
	}
	ch := a.a2a.node.Events()
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return nil
		}
		return a2aEventMsg(ev)
	}
}

// a2aSyncSession moves the node to the live session after a /resume, /new or
// profile switch. It runs on every tick.
func (a *App) a2aSyncSession() {
	if a.a2a == nil {
		return
	}
	a.a2a.node.SetSession(a.sessionID, a.namedAgent)
}

// a2aFooterLabel is the status bar's A2A segment.
func (a *App) a2aFooterLabel() string {
	if a.a2a == nil {
		return ""
	}
	switch a.a2a.node.State() {
	case a2a.StateLocalOnly:
		return "A2A: local only"
	case a2a.StateBlackout:
		return "A2A: blackout"
	}
	return ""
}

// a2aToolHub is the hub a session build adds A2AMessage and A2AAck over, or
// nil when A2A is off.
func (a *App) a2aToolHub() tools.A2AHub {
	if a.a2a == nil {
		return nil
	}
	return a2alink.Hub{Node: a.a2a.node}
}

// repoMemoryStamp is the source of the harness facts a RepoMemoryWrite note
// carries: this host, this session's Agent Card, and the A2A message the
// model is acting on.
func (a *App) repoMemoryStamp() func() repomemory.Stamp {
	st := a.a2a
	sessionID := a.sessionID
	return func() repomemory.Stamp {
		out := repomemory.Stamp{SessionID: sessionID}
		if st == nil {
			return out
		}
		self := st.node.Self()
		out.HostID, out.AgentID, out.SessionID = st.hostID, self.ID, self.SessionID
		if ref := st.ref.Load(); ref != nil {
			out.MessageID, out.ContextID = ref.MessageID, ref.ContextID
		}
		return out
	}
}

// a2aRow appends one A2A row and persists it in order.
func (a *App) a2aRow(kind, text string, env a2a.Envelope, extra map[string]any) {
	var node *a2a.Node
	if a.a2a != nil {
		node = a.a2a.node
	}
	meta := a2alink.EntryMeta(node, env, extra)
	a.messages = append(a.messages, components.Message{Role: components.A2ARole, ToolName: kind, Content: text, Meta: meta})
	a.persistTail()
}

// handleA2AEvent records and acts on one node event.
func (a *App) handleA2AEvent(ev a2a.Event) tea.Cmd {
	if a.a2a == nil {
		return nil
	}
	env := ev.Env
	switch {
	case ev.State != "":
		text := a2alink.StateText(ev.State)
		a.a2aRow(components.A2AState, text, a2a.Envelope{}, map[string]any{"state": ev.State})
		a.refreshFooter()
	case ev.Out:
		switch env.Kind {
		case a2a.KindBroadcast:
			a.a2aRow(components.A2ABroadcastOut, a2alink.FilesLine(env.Work.Files), env, nil)
		case a2a.KindMessage:
			a.a2aRow(components.A2AMessageOut, env.Text(), env, nil)
		case a2a.KindAck:
			a.a2aRow(components.A2AAck, a2alink.AckLine(env), env, map[string]any{"direction": "out"})
		}
		// A reply is recorded with its side-turn's calls (handleA2ASide); a
		// broadcast_end is not a row.
	case env.Kind == a2a.KindBroadcast:
		a.a2aRow(components.A2ABroadcastIn, a2alink.FilesLine(env.Work.Files), env, map[string]any{"overlap": ev.Overlap})
		a.a2aTell(agent.PeerNote{Directive: a2alink.BroadcastDirective(env, ev.Overlap)}, env)
	case env.Kind == a2a.KindAck:
		a.a2aRow(components.A2AAck, a2alink.AckLine(env), env, map[string]any{"direction": "in"})
	case env.Kind == a2a.KindMessage || env.Kind == a2a.KindReply:
		return a.a2aAdmit(ev)
	}
	return nil
}

// a2aAdmit classifies a received message or reply off the UI loop: it is
// another model's text (tools.KindPeer), admitted under the posture gate.
func (a *App) a2aAdmit(ev a2a.Event) tea.Cmd {
	raw := ev.Env.Text()
	guarded := a.effectivePosture().Level(posture.ToolResultUnsafe) != posture.Ignore
	pipe := run.NewPipeline(a.cfg, a.client, a.cache)
	return func() tea.Msg {
		if !guarded {
			return a2aAdmittedMsg{ev: ev, body: sanitize.Text(raw)}
		}
		dec, err := pipe.Process(context.Background(), tools.Result{Kind: tools.KindPeer, Content: raw})
		if err != nil || dec.Action != rolemanager.ActionProceed {
			return a2aAdmittedMsg{ev: ev, withheld: dec.Sentinel, err: err}
		}
		return a2aAdmittedMsg{ev: ev, body: dec.Content}
	}
}

// handleA2AAdmitted records the admitted (or withheld) message or reply and
// acts on it.
func (a *App) handleA2AAdmitted(m a2aAdmittedMsg) tea.Cmd {
	if a.a2a == nil {
		return nil
	}
	env := m.ev.Env
	node := a.a2a.node
	withheld := m.body == ""
	text := m.body
	extra := map[string]any{}
	if withheld {
		label := "withheld"
		if m.withheld != "" {
			label = m.withheld.Label()
		}
		text = "(withheld by the security classifier: " + label + ")"
		extra["withheld"] = true
		node.Audit().Record(audit.A2AWithheld, a2a.OutcomeWithheld, node.Self().SessionID, env, true)
	}
	if env.Kind == a2a.KindMessage {
		a.a2aRow(components.A2AMessageIn, text, env, extra)
		if withheld {
			if r, err := node.Reply(env, a2alink.WithheldReply); err == nil {
				a.a2aRow(components.A2AReplyOut, a2alink.WithheldReply, r, map[string]any{"harness": true})
			}
			return nil
		}
		return a.a2aSideTurn(env, m.body)
	}
	// A reply to this session's question.
	a.a2aRow(components.A2AReplyIn, text, env, extra)
	if withheld {
		return nil
	}
	note := a2alink.ReplyNote(env, m.ev.Question, m.body)
	a.a2aTell(note, env)
	if !a.working() && !a.preSend {
		return a.flushPendingActivitySends()
	}
	return nil
}

// a2aTell hands a note to the running loop, or keeps it for the next turn:
// a directive joins the next prompt's, a reply starts a turn like a
// finished activity's output.
func (a *App) a2aTell(n agent.PeerNote, env a2a.Envelope) {
	a.a2a.ref.Store(&repomemory.Stamp{MessageID: env.MessageID, ContextID: env.ContextID})
	if a.agent != nil && (a.working() || a.preSend) && a.agent.Peer(n) {
		return
	}
	a.a2aQueue(n)
}

func (a *App) a2aQueue(n agent.PeerNote) {
	if len(n.Attachments) == 0 {
		a.a2a.pending = append(a.a2a.pending, n.Directive)
		return
	}
	a.pendingActivitySends = append(a.pendingActivitySends, activitySend{label: n.Attachments[0].Label, atts: n.Attachments, directive: n.Directive})
}

// a2aReclaim takes back the notes a finished loop never delivered.
func (a *App) a2aReclaim() {
	if a.a2a == nil || a.agent == nil {
		return
	}
	for _, n := range a.agent.TakePeerNotes() {
		a.a2aQueue(n)
	}
}

// takeA2ADirectives returns the broadcast directives waiting for the next
// turn, joined, and clears them.
func (a *App) takeA2ADirectives() string {
	if a.a2a == nil || len(a.a2a.pending) == 0 {
		return ""
	}
	d := strings.Join(a.a2a.pending, "\n\n")
	a.a2a.pending = nil
	return d
}

// joinDirective appends extra to a directive.
func joinDirective(d, extra string) string {
	switch {
	case strings.TrimSpace(extra) == "":
		return d
	case strings.TrimSpace(d) == "":
		return extra
	}
	return d + "\n\n" + extra
}

// a2aSideTurn answers a message in a side-turn on this session's model, off
// the UI loop; the main conversation is untouched.
func (a *App) a2aSideTurn(q a2a.Envelope, body string) tea.Cmd {
	sess := a.agent
	params := a.sessionBuildParams()
	in := agent.SideReplyInput{
		From: q.Agent.Display(), Work: a2alink.WorkLine(q.Work), Question: body,
		MessageID: q.MessageID, ContextID: q.ContextID, Context: a.a2aContextSummary(),
	}
	return func() tea.Msg {
		s := sess
		if s == nil {
			built, err := buildAgentSession(params)
			if err != nil {
				return a2aSideMsg{q: q, err: err}
			}
			s = built
		}
		ctx, cancel := context.WithTimeout(context.Background(), a2alink.SideTurnTimeout)
		defer cancel()
		res, err := s.SideReply(ctx, in)
		return a2aSideMsg{q: q, res: res, err: err}
	}
}

// handleA2ASide sends the side-turn's reply and records it with the tool
// calls the side-turn made.
func (a *App) handleA2ASide(m a2aSideMsg) tea.Cmd {
	if a.a2a == nil {
		return nil
	}
	text, extra := m.res.Reply, map[string]any{}
	if m.err != nil || strings.TrimSpace(text) == "" {
		text, extra["harness"] = a2alink.FailedReply, true
	}
	if len(m.res.Calls) > 0 {
		extra["calls"] = m.res.Calls
	}
	r, err := a.a2a.node.Reply(m.q, text)
	if err != nil {
		a.addSystem("A2A: the reply could not be sent: " + err.Error())
		return nil
	}
	a.a2aRow(components.A2AReplyOut, text, r, extra)
	return nil
}

// a2aContextSummary is the harness's summary of this session for a
// side-turn: the repository, the last prompt and reply, the todos and the
// files changed.
func (a *App) a2aContextSummary() string {
	var b strings.Builder
	if a.a2a != nil {
		w := a.a2a.node.Broadcaster().Work()
		if l := a2alink.WorkLine(w); l != "" {
			b.WriteString("Working on " + l + ".\n")
		}
		if len(w.Files) > 0 {
			b.WriteString("Files this session changed: " + strings.Join(w.Files, ", ") + ".\n")
		}
	}
	var lastUser, lastReply string
	for i := len(a.messages) - 1; i >= 0 && (lastUser == "" || lastReply == ""); i-- {
		m := a.messages[i]
		switch m.Role {
		case "user":
			if lastUser == "" {
				lastUser = m.Text()
			}
		case "assistant":
			if lastReply == "" && strings.TrimSpace(m.Text()) != "" {
				lastReply = m.Text()
			}
		}
	}
	if lastUser != "" {
		b.WriteString("Latest task from the user: " + sanitize.TextN(lastUser, 800) + "\n")
	}
	if a.todos != nil && len(a.todos.Items) > 0 {
		b.WriteString("Todos:\n")
		for i, it := range a.todos.Items {
			if i == 20 {
				break
			}
			b.WriteString(fmt.Sprintf("- [%s] %s\n", it.Status, sanitize.Line(it.Text, 200)))
		}
	}
	if lastReply != "" {
		b.WriteString("Your latest reply: " + sanitize.TextN(lastReply, 1500) + "\n")
	}
	return b.String()
}

// a2aNoteDiff feeds the files a tool changed to the broadcaster.
func (a *App) a2aNoteDiff(ch *filediff.Change) {
	if a.a2a == nil || ch == nil {
		return
	}
	var paths []string
	for _, f := range ch.Files {
		paths = append(paths, f.Path)
	}
	a.a2a.node.Broadcaster().AddFiles(paths...)
}
