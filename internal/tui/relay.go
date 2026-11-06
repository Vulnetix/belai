package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/filediff"
	"github.com/vulnetix/belai/internal/hooks"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/transcript"
)

// maxRelayPaths caps the session-wide changed-path set kept for /relay. The
// drafter is shown fewer still (rolemanager caps the facts block).
const maxRelayPaths = 500

// relayState is the /relay flow: a draft being written, then under review.
// Nothing here reaches disk. The draft is model text, and it re-enters a
// session only through applyRelay after the user accepts it.
type relayState struct {
	// seq numbers the flow; a message for an older number is dropped, so a
	// discarded or redrafted flow cannot be revived by a late reply.
	seq int
	// drafting is true while a draft or an admission is in flight.
	drafting  bool
	direction string
	// draft is the text under review; admitted is the last text the security
	// classifier admitted. A draft that differs from it is admitted again on
	// accept.
	draft    string
	admitted string
	edited   bool
	// err is the last refusal to show in the review.
	err string
	// editDir is the scratch directory handed to $EDITOR, removed on return.
	editDir string
	// sourceID and sourceTurns pin the transcript a draft was written from.
	sourceID    string
	sourceTurns int
	vp          viewport.Model
}

// relayDraftMsg carries a finished draft back to the UI loop.
type relayDraftMsg struct {
	seq int
	// text is the validated, admitted handoff; empty when err is set.
	text        string
	err         error
	sourceID    string
	sourceTurns int
}

// relayAcceptedMsg carries the admission of a user-edited handoff.
type relayAcceptedMsg struct {
	seq  int
	text string
	err  error
}

// relayEditedMsg is the return from the $EDITOR round trip.
type relayEditedMsg struct {
	seq int
	err error
}

// observeRelayPaths adds one tool's observed file changes to the session-wide
// set /relay reports as a fact. Paths are deduplicated and capped.
func (a *App) observeRelayPaths(ch *filediff.Change) {
	if ch == nil {
		return
	}
	if a.relayPathSet == nil {
		a.relayPathSet = map[string]bool{}
	}
	for _, f := range ch.Files {
		p := filepath.ToSlash(f.Path)
		if p == "" || a.relayPathSet[p] {
			continue
		}
		if len(a.relayPaths) >= maxRelayPaths {
			return
		}
		a.relayPathSet[p] = true
		a.relayPaths = append(a.relayPaths, p)
	}
}

// resetRelayPaths drops the changed-path set: it belongs to one session.
func (a *App) resetRelayPaths() {
	a.relayPaths = nil
	a.relayPathSet = nil
}

// relayRefusal says why /relay cannot start now, or "" when it can. It fails
// closed, as /compact does: a draft needs the classifier that admits it, and
// the session must be idle so the transcript it summarises holds still.
func (a *App) relayRefusal() string {
	switch {
	case !a.settings.HandoffEnabled():
		return "off (handoff.enabled is false)"
	case a.working() || a.preSend:
		return "wait for the running turn to finish"
	case a.relay.drafting || a.viewOpen(viewRelay):
		return "a handoff is already being drafted"
	case a.classifier == nil:
		return "no classifier configured"
	case !a.hasRelayContent():
		return "nothing to hand off yet"
	}
	return ""
}

// hasRelayContent reports whether the transcript holds a user or assistant
// message worth summarising.
func (a *App) hasRelayContent() bool {
	for _, m := range a.transcriptMessages() {
		if m.Role == "user" || m.Role == "assistant" {
			return true
		}
	}
	return false
}

// relayTurns counts the conversation messages a draft is pinned to; harness
// notices (system rows) are not part of it.
func (a *App) relayTurns() int {
	n := 0
	for _, m := range a.transcriptMessages() {
		if m.Role != "system" {
			n++
		}
	}
	return n
}

// relayCommand handles /relay [direction]: it drafts a handoff note for a
// fresh session and opens the review. The command is the user's alone; no
// model can start it.
func (a *App) relayCommand(arg string) tea.Cmd {
	if why := a.relayRefusal(); why != "" {
		a.addSystem("relay: " + why)
		return nil
	}
	seq := a.relay.seq + 1
	a.relay = relayState{seq: seq, direction: strings.TrimSpace(arg)}
	return a.startRelayDraft()
}

// startRelayDraft snapshots every input on the UI loop and drafts off it.
func (a *App) startRelayDraft() tea.Cmd {
	msgs := a.transcriptMessages()
	nonce := nonceHex()
	doc := transcript.Serialize(msgs, transcript.SerializeOptions{Nonce: nonce})
	facts := a.relayFacts(nonce)
	direction := a.relay.direction
	caveman := a.settings.ClassifierCavemanEnabled()
	max := a.settings.HandoffMaxBytes()
	facts.MaxBytes = max
	c := a.classifier
	hookSet, sid, wd := a.sessionHooks(), a.sessionID, a.workdir
	admit := a.summaryAdmitter()
	gs := a.gitSync
	seq := a.relay.seq
	turns := a.relayTurns()
	a.relay.drafting = true
	a.relay.err = ""
	a.addSystem("relay: drafting a handoff note")
	return func() tea.Msg {
		// pre_compact runs off the UI goroutine; it cannot stop the draft.
		if hookSet.Has(hooks.EventPreCompact) {
			hookSet.Dispatch(a.ctx, hooks.Input{Event: hooks.EventPreCompact, SessionID: sid, Cwd: wd})
		}
		done := relayDraftMsg{seq: seq, sourceID: sid, sourceTurns: turns}
		if gs != nil {
			if in := gs.Info(a.ctx, false); in != nil {
				if facts.Branch == "" {
					facts.Branch = in.Branch
				}
				facts.Dirty = in.Dirty
			}
		}
		payload := rolemanager.BuildHandoffDraftPayload(doc, facts, direction, caveman)
		ctx, served := rolemanager.TrackServedModel(a.ctx)
		raw, err := c.Classify(ctx, payload)
		if err != nil {
			done.err = err
			return done
		}
		text, err := rolemanager.ValidateHandoff(raw, served(), max)
		if err != nil {
			done.err = err
			return done
		}
		done.text, done.err = admitRelayText(a.ctx, admit, text)
		return done
	}
}

// admitRelayText runs the security classifier over a handoff before it can be
// shown or stored, with the same wording as a /compact refusal.
func admitRelayText(ctx context.Context, admit func(context.Context, string) (rolemanager.Decision, error), text string) (string, error) {
	dec, err := admit(ctx, text)
	var refusal *rolemanager.RefusalError
	switch {
	case errors.As(err, &refusal):
		return "", fmt.Errorf("the handoff was refused by the security classifier (%s)", refusal.Sentinel.Label())
	case err != nil:
		return "", fmt.Errorf("the handoff could not be checked: %w", err)
	case dec.Action != rolemanager.ActionProceed:
		return "", fmt.Errorf("the handoff was not admitted (%s)", dec.Sentinel.Label())
	}
	return dec.Content, nil
}

// relayFacts collects the harness facts that ride beside the transcript. The
// builder cleans every value; this only reads state.
func (a *App) relayFacts(nonce string) rolemanager.RelayFacts {
	f := rolemanager.RelayFacts{Nonce: nonce, Branch: a.gitInfo.Branch}
	if a.settings.HandoffIncludeGoal() {
		if a.lastGoal != nil {
			f.Objective, f.GoalStatus = a.lastGoal.Objective, a.lastGoal.Status
		}
		if a.todos != nil && !a.todos.Cleared {
			for _, it := range a.todos.Items {
				f.Todos = append(f.Todos, rolemanager.RelayTodo{Text: it.Text, Status: string(it.Status)})
			}
		}
	}
	f.Paths = append(f.Paths, a.relayPaths...)
	return f
}

// handleRelayDraft takes a finished draft. A draft for a flow that was
// discarded, or written from a transcript that has since moved, is dropped.
func (a *App) handleRelayDraft(m relayDraftMsg) tea.Cmd {
	if m.seq != a.relay.seq {
		return nil
	}
	a.relay.drafting = false
	switch {
	case m.err != nil:
		a.relayFail("relay failed: " + m.err.Error())
		return nil
	case a.working() || a.sessionID != m.sourceID || a.relayTurns() != m.sourceTurns:
		a.relayFail("relay: the session moved on while the note was drafted; run /relay again")
		return nil
	}
	a.relay.draft, a.relay.admitted = m.text, m.text
	a.relay.sourceID, a.relay.sourceTurns = m.sourceID, m.sourceTurns
	a.relay.edited = false
	a.relay.err = ""
	if a.viewOpen(viewRelay) {
		a.relay.setContent(a.relayWidth(), a.relayHeight())
		return nil
	}
	return a.push(viewRelay)
}

// relayFail ends a flow that produced nothing usable. An open review keeps its
// previous draft and shows the reason; with no draft the flow ends.
func (a *App) relayFail(msg string) {
	a.addSystem(msg)
	if a.viewOpen(viewRelay) && a.relay.draft != "" {
		a.relay.err = strings.TrimPrefix(msg, "relay failed: ")
		return
	}
	a.endRelay()
}

// endRelay discards the flow: the scratch directory goes, the sequence moves on
// so a late reply is ignored, and the review closes.
func (a *App) endRelay() {
	a.removeRelayScratch()
	open := a.viewOpen(viewRelay)
	a.relay = relayState{seq: a.relay.seq + 1}
	if open {
		a.dropView(viewRelay)
	}
}

// discardRelay is Esc in the review.
func (a *App) discardRelay() tea.Cmd {
	a.endRelay()
	a.addSystem("relay: discarded, the session is unchanged")
	return nil
}

// redraftRelay is r in the review: another draft from the same transcript and
// direction.
func (a *App) redraftRelay() tea.Cmd {
	if a.relay.drafting {
		return nil
	}
	if why := a.relayRedraftRefusal(); why != "" {
		a.relay.err = why
		return nil
	}
	a.relay.seq++
	return a.startRelayDraft()
}

func (a *App) relayRedraftRefusal() string {
	switch {
	case !a.settings.HandoffEnabled():
		return "off (handoff.enabled is false)"
	case a.working() || a.preSend:
		return "wait for the running turn to finish"
	case a.classifier == nil:
		return "no classifier configured"
	}
	return ""
}

// openRelayEditor writes the draft to a private scratch file and opens it in
// $VISUAL or $EDITOR. The file is removed when the editor returns.
func (a *App) openRelayEditor() tea.Cmd {
	if a.relay.drafting {
		return nil
	}
	bin, args, ok := resolveEditorCommand()
	if !ok {
		a.relay.err = "no $VISUAL or $EDITOR found"
		return nil
	}
	a.removeRelayScratch()
	dir, err := os.MkdirTemp("", "belai-relay-")
	if err != nil {
		a.relay.err = "edit failed: " + err.Error()
		return nil
	}
	path := filepath.Join(dir, "handoff.md")
	if err := os.WriteFile(path, []byte(a.relay.draft), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		a.relay.err = "edit failed: " + err.Error()
		return nil
	}
	a.relay.editDir = dir
	seq := a.relay.seq
	cmd := exec.Command(bin, append(args, path)...)
	return a.execEditor(cmd, func(err error) tea.Msg { return relayEditedMsg{seq: seq, err: err} })
}

// removeRelayScratch deletes the editor's scratch directory, if any.
func (a *App) removeRelayScratch() {
	if a.relay.editDir != "" {
		_ = os.RemoveAll(a.relay.editDir)
		a.relay.editDir = ""
	}
}

// handleRelayEdited reads the edited text back. An edit the validator refuses
// leaves the previous draft in place and says why.
func (a *App) handleRelayEdited(m relayEditedMsg) tea.Cmd {
	var cmds []tea.Cmd
	if mouseEnabled(&a.settings) {
		cmds = append(cmds, func() tea.Msg { return tea.EnableMouseCellMotion() })
	}
	if m.seq != a.relay.seq || a.relay.editDir == "" {
		a.removeRelayScratch()
		return tea.Batch(cmds...)
	}
	path := filepath.Join(a.relay.editDir, "handoff.md")
	data, err := os.ReadFile(path)
	a.removeRelayScratch()
	switch {
	case m.err != nil:
		a.relay.err = "edit failed: " + m.err.Error()
	case err != nil:
		a.relay.err = "edit failed: " + err.Error()
	default:
		text, verr := rolemanager.ValidateHandoff(string(data), "", a.settings.HandoffMaxBytes())
		if verr != nil {
			a.relay.err = "the edited note was refused (" + verr.Error() + "); keeping the previous draft"
			break
		}
		if text != a.relay.draft {
			a.relay.draft, a.relay.edited = text, true
		}
		a.relay.err = ""
		a.relay.setContent(a.relayWidth(), a.relayHeight())
	}
	return tea.Batch(cmds...)
}

// acceptRelay is Enter in the review. The text is validated and sanitised
// again; a draft the user edited is admitted again before it is stored.
func (a *App) acceptRelay() tea.Cmd {
	if a.relay.drafting {
		return nil
	}
	text, err := rolemanager.ValidateHandoff(a.relay.draft, "", a.settings.HandoffMaxBytes())
	if err != nil {
		a.relay.err = "the note is not usable (" + err.Error() + ")"
		return nil
	}
	text = sanitize.Sanitize(text)
	if text == a.relay.admitted {
		return a.finishRelay(text)
	}
	admit := a.summaryAdmitter()
	seq := a.relay.seq
	a.relay.drafting = true
	a.relay.err = ""
	return func() tea.Msg {
		out, err := admitRelayText(a.ctx, admit, text)
		return relayAcceptedMsg{seq: seq, text: out, err: err}
	}
}

// handleRelayAccepted finishes an accept that had to be admitted first.
func (a *App) handleRelayAccepted(m relayAcceptedMsg) tea.Cmd {
	if m.seq != a.relay.seq {
		return nil
	}
	a.relay.drafting = false
	if m.err != nil {
		a.relay.err = m.err.Error()
		return nil
	}
	return a.finishRelay(m.text)
}

// finishRelay switches to the fresh session. The session must still be the one
// the draft came from and idle; otherwise nothing changes.
func (a *App) finishRelay(text string) tea.Cmd {
	if a.working() || a.sessionID != a.relay.sourceID || a.relayTurns() != a.relay.sourceTurns {
		a.relay.err = "the session moved on; discard this note and run /relay again"
		return nil
	}
	edited, dirLen := a.relay.edited, len([]rune(sanitize.Line(a.relay.direction, rolemanager.MaxRelayDirectionRunes)))
	a.endRelay()
	return a.applyRelay(text, edited, dirLen)
}

// applyRelay starts the new session from the note. It mirrors applyCompaction:
// a new id, parent_session, a summary entry (kind "handoff"), and the todo list
// and name carried. The note re-enters only through buildTurns, wrapped as an
// untrusted user turn.
func (a *App) applyRelay(text string, edited bool, directionLen int) tea.Cmd {
	msgs := a.transcriptMessages()
	est := transcript.EstimateContext(msgs)
	old := a.sessionID
	oldName := a.sessionName

	a.flushTurnEnd()
	a.openAsks = nil
	a.sessionID = session.MustID()
	a.publishSessionID()
	a.lastEntryID = ""
	a.persistedUpTo = 0
	a.parentSession = old

	a.appendEntry(session.Entry{
		Type:    "summary",
		Role:    "",
		Content: text,
		Meta: map[string]any{
			"parent_session":  old,
			"kind":            "handoff",
			"model":           a.cfg.Model,
			"source_messages": len(msgs),
			"source_tokens":   est.Tokens,
			"edited":          edited,
			"direction_len":   directionLen,
		},
	})
	// The goal, the plan run and the test pass belong to the session that
	// ran them; the note carries what the next session needs of them.
	a.planExecuting = false
	a.lastGoal = nil
	a.resetTaskPaths()
	a.resetCommitLedger()
	a.resetRelayPaths()
	a.resetTestPass()
	a.setCompactState(session.CompactionState{})
	a.appendEntry(a.compactState.ToEntry(a.lastEntryID))
	if oldName != "" {
		a.appendEntry(session.Entry{Type: session.EntryTypeSessionName, Role: "", Content: oldName, Meta: map[string]any{"source": "inherited"}})
	}
	if a.todos != nil && len(a.todos.Items) > 0 {
		a.appendEntry(a.todos.ToEntry(""))
	}

	a.sessionName = oldName
	a.nameRequested = true
	a.summary = text
	a.messages = nil
	a.usage = nil
	a.usageStale = true
	a.subagents = nil
	a.subagentIdx = map[string]int{}
	a.threadFilter = ""
	a.runsOpen = false
	a.runsFocus = false
	a.runsTab = tabActivity
	a.runsSel = 0
	a.runsScroll = 0
	a.invalidateAgentSession()

	a.addSystem(fmt.Sprintf("relay: continuing %s in %s", shortID(old), shortID(a.sessionID)))
	a.saveSession()
	a.refreshFooter()
	return nil
}
