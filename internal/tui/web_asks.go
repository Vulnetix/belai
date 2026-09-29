package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/tui/components"
)

// Asks, turns and decisions as durable session records (docs/session-sync.md).
//
// Every question the host stops on is written to the session JSONL as an `ask`
// entry whose id is the ask id, and every resolution — typed on the host or
// sent from the website — as an `ask_answer` entry with id "answer-" + ask id.
// Because the syncer mirrors the file, that one write is both the record a
// resumed session reads and what the website shows and answers. Turn
// boundaries (`turn_state`), running tools (`tool_start`) and the role
// manager's verdicts ride the same path.
//
// A web answer is untrusted input. It is applied only to the ask that is open
// right now, only after it validates against that ask, and only through the
// code the host's own keys use, so it passes every gate a key press passes
// (clarify answers still go through admission on the agent side). The first
// answer wins: a host key press and a web answer racing for one ask resolve it
// once, and the loser is refused.

// Ask kinds, shared with the website.
const (
	askPermission = "permission"
	askClarify    = "clarify"
	askModeChoice = "mode_choice"
	askPlanReview = "plan_review"
)

// Permission decisions.
const (
	askAllowOnce   = "allow_once"
	askAllowAlways = "allow_always"
	askDeny        = "deny"
)

// Plan-review choices.
const (
	planChoiceApproveHere = "approve_here"
	planChoiceApproveNew  = "approve_new"
	planChoiceRefine      = "refine"
	planChoiceStay        = "stay"
)

// Answer sources.
const (
	answerFromHost = "host"
	answerFromWeb  = "web"
)

const (
	// maxAskArgsBytes caps the tool arguments an ask records.
	maxAskArgsBytes = 32 << 10
	// maxAskPlanBytes caps the plan text a plan-review ask carries.
	maxAskPlanBytes = 64 << 10
	// maxAnswerNoteBytes caps a web answer's per-group note.
	maxAnswerNoteBytes = 2 << 10
)

// remoteAnswerMsg carries one web answer claimed from the inbox.
type remoteAnswerMsg sessionsync.RemoteAnswer

// answerEntryID is the ask_answer entry id for an ask.
func answerEntryID(askID string) string { return "answer-" + askID }

// addEphemeralSystem shows a notice whose durable record is an ask entry.
func (a *App) addEphemeralSystem(text string) {
	a.messages = append(a.messages, components.Message{Role: "system", Content: text, Ephemeral: true})
}

// recordAsk writes an ask entry and returns its id, or "" when the session is
// not being recorded (the ask then stays host-only).
func (a *App) recordAsk(kind, content string, meta map[string]any) string {
	if a.store == nil || a.storeDisabled {
		return ""
	}
	id, err := session.NewID()
	if err != nil {
		return ""
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["ask_id"] = id
	meta["kind"] = kind
	a.appendEntry(session.Entry{ID: id, Type: "ask", Role: "system", Content: content, Meta: meta})
	if a.lastEntryID != id {
		return ""
	}
	if a.openAsks == nil {
		a.openAsks = map[string]string{}
	}
	a.openAsks[id] = kind
	return id
}

// recordAskAnswer writes the ask_answer entry for askID and returns its id
// ("" when nothing was written). It is idempotent per ask.
func (a *App) recordAskAnswer(askID, kind, source, remoteID, content string, meta map[string]any) string {
	if askID == "" {
		return ""
	}
	if _, open := a.openAsks[askID]; !open {
		return ""
	}
	delete(a.openAsks, askID)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["ask_id"] = askID
	meta["kind"] = kind
	meta["source"] = source
	if remoteID != "" {
		meta["remote_answer_id"] = remoteID
	}
	id := answerEntryID(askID)
	a.appendEntry(session.Entry{ID: id, Type: "ask_answer", Role: "system", Content: content, Meta: meta})
	if a.lastEntryID != id {
		return ""
	}
	return id
}

// closeOpenAsks records every blocking ask still open as closed: the turn
// that asked it is over, so nothing can answer it any more. A plan review is
// not blocking and outlives its turn.
func (a *App) closeOpenAsks(reason string) {
	for id, kind := range a.openAsks {
		if kind == askPlanReview {
			continue
		}
		a.recordAskAnswer(id, kind, answerFromHost, "", "", map[string]any{"closed": true, "reason": reason})
	}
}

// ── Turn boundaries and running tools ────────────────────────────────────

// openTurn records that a turn started.
func (a *App) openTurn() {
	if a.turnID != "" {
		a.closeTurn("superseded")
		a.flushTurnEnd()
	}
	id, err := session.NewID()
	if err != nil {
		return
	}
	a.turnID = id
	a.turnStarted = time.Now()
	meta := a.footerFacts()
	meta["turn_id"], meta["state"], meta["started_at"] = id, "started", a.turnStarted.UnixMilli()
	a.appendEntry(session.Entry{Type: "turn_state", Role: "system", Meta: meta})
}

// footerFacts is what the TUI footer shows, for the website's footer: mode,
// model, effort, branch, the switches and the context estimate. Harness
// facts only.
func (a *App) footerFacts() map[string]any {
	a.refreshFooter()
	return map[string]any{
		"mode": a.mode, "provider": a.cfg.Provider, "model": a.cfg.Model, "effort": a.cfg.Effort,
		"branch": a.footer.Branch, "guardrails": a.guardrailsEnabled(), "ask": a.askEnabled(),
		"context_tokens": a.footer.Tokens, "context_limit": a.footer.ContextLimit,
	}
}

// closeTurn marks the open turn as over ("ended", "error", "interrupted").
// The first state given wins, so a specific one set before the generic
// endPhase call stands. The line itself is written by flushTurnEnd, after the
// turn's last rows are persisted, so a reader never sees the turn end before
// its reply.
func (a *App) closeTurn(state string) {
	if a.turnID == "" || a.turnEnd != "" {
		return
	}
	a.turnEnd = state
	a.turnEndAt = time.Now()
}

// flushTurnEnd writes a pending turn end. persistTail and the tick call it.
func (a *App) flushTurnEnd() {
	if a.turnID == "" || a.turnEnd == "" || a.cancel != nil {
		return
	}
	id, state := a.turnID, a.turnEnd
	a.turnID, a.turnEnd = "", ""
	a.closeOpenAsks("the turn " + state)
	meta := a.footerFacts()
	meta["turn_id"], meta["state"], meta["started_at"] = id, state, a.turnStarted.UnixMilli()
	meta["duration_ms"] = a.turnEndAt.Sub(a.turnStarted).Milliseconds()
	a.appendEntry(session.Entry{Type: "turn_state", Role: "system", Timestamp: a.turnEndAt.UnixMilli(), Meta: meta})
}

// recordToolStart records a main-thread tool call the moment it starts, so a
// reader sees it running before its result lands. Resume pairs calls with
// results only, so this entry never re-enters a conversation.
func (a *App) recordToolStart(id, name, args string) {
	if len(args) > maxAskArgsBytes {
		args = truncateUTF8(args, maxAskArgsBytes)
	}
	a.appendEntry(session.Entry{Type: "tool_start", Role: "tool", Meta: map[string]any{
		"tool_call_id": id, "tool_name": name, "tool_args": args, "started_at": time.Now().UnixMilli(),
	}})
}

// ── Role-manager decisions ───────────────────────────────────────────────

// decisionFacts is the bounded metadata a decision row persists. Detail is
// never carried: only the verdict token, its label, the subject tool, the
// pass and the model reach the record, exactly as the feed's own rule.
func decisionFacts(act rolemanager.Activity) map[string]any { return rolemanager.Facts(act) }

// recordActivity writes a role-manager decision to the session record. It runs
// for every activity, shown or not, at the moment the render loop takes it
// from the queue, so the transcript is a complete, ordered account of what the
// harness decided whatever the display level. A decision the feed does not
// print (mode selection, goal and plan evaluation already have their own
// lines) is marked hidden so a resumed TUI does not render it twice.
func (a *App) recordActivity(act rolemanager.Activity) {
	rec := act.Record()
	if _, hidden := rec.Meta["hidden"]; !hidden {
		// A row the feed shows is labelled with the model that answered it;
		// only an activity that names none falls back to the agent model.
		if _, ok := rec.Meta["model"]; !ok {
			if a.cfg.Provider != "" {
				rec.Meta["provider"] = a.cfg.Provider
			}
			if a.cfg.Model != "" {
				rec.Meta["model"] = a.cfg.Model
			}
		}
	}
	a.appendEntry(session.Entry{Type: rolemanager.RecordType, Role: rolemanager.RecordType, Content: rec.Content, Meta: rec.Meta, Timestamp: rec.Timestamp})
}

// ── Recording the host's asks ────────────────────────────────────────────

// recordPermissionAsk writes a permission ask: the tool, its subject and
// arguments, the rule allow-always would add, and the diff it would make.
func (a *App) recordPermissionAsk(ask *agent.AskRequest) string {
	args := toolArgsString(ask.Args)
	if len(args) > maxAskArgsBytes {
		args = truncateUTF8(args, maxAskArgsBytes)
	}
	rule := ask.Name
	if ask.Subject != "" {
		rule = ask.Name + "(" + ask.Subject + ")"
	}
	meta := map[string]any{
		"tool":    ask.Name,
		"subject": ask.Subject,
		"args":    args,
		"rule":    rule,
		"options": []string{askAllowOnce, askAllowAlways, askDeny},
	}
	if ask.Preview != nil && !ask.Preview.Empty() {
		meta["diff"] = ask.Preview.Wire(maxDiffWireBytes)
	}
	content := "permission: " + ask.Name
	if ask.Subject != "" {
		content += " " + ask.Subject
	}
	return a.recordAsk(askPermission, content, meta)
}

// recordPlanReviewAsk writes a plan review, carrying the plan text so the
// website can show what it is approving.
func (a *App) recordPlanReviewAsk(name, path string) string {
	meta := map[string]any{
		"plan_name": name,
		"plan_path": path,
		"options":   []string{planChoiceApproveHere, planChoiceApproveNew, planChoiceRefine, planChoiceStay},
	}
	if body, err := os.ReadFile(path); err == nil {
		text := string(body)
		if len(text) > maxAskPlanBytes {
			text = truncateUTF8(text, maxAskPlanBytes)
			meta["plan_truncated"] = true
		}
		meta["plan"] = text
	}
	return a.recordAsk(askPlanReview, "plan written: "+path, meta)
}

// ── Resolving asks (host keys and web answers share these) ───────────────

// settlePermissionAsk answers the open permission ask once: it writes the
// allow rule for allow-always, releases the agent, records the answer and
// dismisses the view. It returns the ask_answer entry id.
func (a *App) settlePermissionAsk(decision, source, remoteID string) string {
	st := a.permAskState
	if st.reply == nil {
		return ""
	}
	if decision == askAllowAlways && st.ask != nil {
		a.allowAlwaysRule(st.ask.Name, st.ask.Subject)
	}
	a.answerPermissionAsk(decision != askDeny)
	text := "permission: " + strings.ReplaceAll(decision, "_", " ")
	if source == answerFromWeb {
		text += " (answered on the web)"
	}
	if st.ask != nil {
		text = strings.Replace(text, "permission:", "permission for "+st.ask.Name+":", 1)
	}
	entryID := a.recordAskAnswer(st.askID, askPermission, source, remoteID, text, map[string]any{"decision": decision})
	a.permAskState = permissionAskViewState{}
	a.dropView(viewPermissionAsk)
	return entryID
}

// settleClarify sends answers for the open clarify (or mode-choice) ask,
// shows them in the transcript and records them. It returns the ask_answer
// entry id.
func (a *App) settleClarify(answers clarify.Answers, source, remoteID string, declined bool) string {
	st := a.clarifyState
	if st.reply == nil {
		return ""
	}
	reply := st.reply
	go func() { reply <- answers }()
	kind := askClarify
	if st.modeChoice {
		kind = askModeChoice
	}
	text := answers.Render(st.q)
	if declined {
		text = "clarification declined"
	}
	if source == answerFromWeb {
		text += "\n(answered on the web)"
	}
	// The questionnaire is already in the transcript; the answers belong
	// beside it so the session record shows what was chosen.
	a.addEphemeralSystem(text)
	entryID := a.recordAskAnswer(st.askID, kind, source, remoteID, text, map[string]any{
		"answers": wireAnswers(answers), "declined": declined,
	})
	a.clarifyState.reply = nil
	a.clarifyState.askID = ""
	a.dropView(viewClarify)
	return entryID
}

// recordPlanChoice records the host's or the web's plan-review choice.
func (a *App) recordPlanChoice(choice, source, remoteID, notes string) string {
	text := "plan review: " + strings.ReplaceAll(choice, "_", " ")
	if source == answerFromWeb {
		text += " (answered on the web)"
	}
	meta := map[string]any{"plan_choice": choice}
	if notes != "" {
		meta["notes"] = notes
	}
	id := a.recordAskAnswer(a.planReview.askID, askPlanReview, source, remoteID, text, meta)
	a.planReview.askID = ""
	return id
}

// dropView removes v from the view stack wherever it is. A web answer can
// arrive while the host user has another screen open above the ask.
func (a *App) dropView(v viewState) {
	if a.view == v {
		a.pop()
		return
	}
	for i := len(a.viewStack) - 1; i >= 0; i-- {
		if a.viewStack[i] == v {
			a.viewStack = append(a.viewStack[:i], a.viewStack[i+1:]...)
			return
		}
	}
}

func (a *App) viewOpen(v viewState) bool {
	for _, s := range a.viewStack {
		if s == v {
			return true
		}
	}
	return a.view == v
}

// ── Web answers ──────────────────────────────────────────────────────────

// watchRemoteAnswers waits for the next web answer.
func (a *App) watchRemoteAnswers() tea.Cmd {
	if a.syncer == nil || !a.syncer.RemoteAnswersEnabled() {
		return nil
	}
	ch := a.syncer.Answers()
	return func() tea.Msg {
		ans, ok := <-ch
		if !ok {
			return nil
		}
		return remoteAnswerMsg(ans)
	}
}

// watchRemote arms the inbox watchers: prompts, answers and agent drafts.
func (a *App) watchRemote() tea.Cmd {
	return tea.Batch(a.watchRemotePrompts(), a.watchRemoteAnswers(), a.watchRemoteDrafts())
}

// handleRemoteAnswer applies one web answer to the ask it names, if that ask
// is the one open now, or refuses it.
func (a *App) handleRemoteAnswer(ans sessionsync.RemoteAnswer) tea.Cmd {
	if a.syncer == nil {
		return nil
	}
	refuse := func(reason string) tea.Cmd {
		a.syncer.AckAnswer(ans.ID, sessionsync.AckRefused, reason, "")
		return nil
	}
	accept := func(entryID string) {
		a.syncer.AckAnswer(ans.ID, sessionsync.AckAccepted, "", entryID)
	}
	if ans.SessionID != a.sessionID {
		return refuse("the session is no longer active on the host")
	}
	if ans.AskID == "" {
		return refuse("the answer names no question")
	}
	switch {
	case ans.AskID == a.permAskState.askID && a.permAskState.reply != nil:
		if ans.Kind != askPermission {
			return refuse("the answer does not fit a permission ask")
		}
		var p struct {
			Decision string `json:"decision"`
		}
		if err := json.Unmarshal(ans.Payload, &p); err != nil {
			return refuse("the answer could not be read")
		}
		switch p.Decision {
		case askAllowOnce, askAllowAlways, askDeny:
		default:
			return refuse("unknown decision; expected allow_once, allow_always or deny")
		}
		accept(a.settlePermissionAsk(p.Decision, answerFromWeb, ans.ID))
		return a.nextAgent()

	case ans.AskID == a.clarifyState.askID && a.clarifyState.reply != nil:
		want := askClarify
		if a.clarifyState.modeChoice {
			want = askModeChoice
		}
		if ans.Kind != want {
			return refuse("the answer does not fit this question")
		}
		answers, declined, err := parseWebAnswers(a.clarifyState.q, ans.Payload)
		if err != nil {
			return refuse(err.Error())
		}
		accept(a.settleClarify(answers, answerFromWeb, ans.ID, declined))
		return a.nextAgent()

	case ans.AskID == a.planReview.askID && a.planReview.askID != "" && a.viewOpen(viewPlanReview):
		if ans.Kind != askPlanReview {
			return refuse("the answer does not fit a plan review")
		}
		if a.working() || a.preSend {
			return refuse("the plan turn is still finishing on the host; try again in a moment")
		}
		var p struct {
			Choice string `json:"choice"`
			Notes  string `json:"notes"`
		}
		if err := json.Unmarshal(ans.Payload, &p); err != nil {
			return refuse("the answer could not be read")
		}
		return a.applyWebPlanChoice(ans, p.Choice, p.Notes)
	}
	return refuse("this question is no longer open on the host: it was answered there, or the turn moved on")
}

// applyWebPlanChoice runs a web plan-review choice through the same arms the
// host's keys use. Refinement notes are a prompt from the web, so they are
// cleaned and take the web-prompt path (never a slash or shell command).
func (a *App) applyWebPlanChoice(ans sessionsync.RemoteAnswer, choice, notes string) tea.Cmd {
	switch choice {
	case planChoiceApproveHere, planChoiceApproveNew, planChoiceStay:
		entryID := a.recordPlanChoice(choice, answerFromWeb, ans.ID, "")
		a.syncer.AckAnswer(ans.ID, sessionsync.AckAccepted, "", entryID)
		a.dropView(viewPlanReview)
		// The arms pop the review themselves; put it back on top first so
		// they pop exactly it.
		a.viewStack = append(a.viewStack, viewPlanReview)
		a.view = viewPlanReview
		switch choice {
		case planChoiceApproveHere:
			return a.submitPlanApprove()
		case planChoiceApproveNew:
			return a.submitPlanApproveNew()
		default:
			return a.submitPlanStay()
		}
	case planChoiceRefine:
		text := sessionsync.CleanPrompt(notes)
		if text == "" {
			a.syncer.AckAnswer(ans.ID, sessionsync.AckRefused, "refinement notes were empty after cleaning", "")
			return nil
		}
		entryID := a.recordPlanChoice(choice, answerFromWeb, ans.ID, text)
		a.syncer.AckAnswer(ans.ID, sessionsync.AckAccepted, "", entryID)
		a.dropView(viewPlanReview)
		a.pendingDirective = activePlanReviewDirective
		a.pendingPlanRevision = nextPlanRevision(a.workdir, a.planReview.name)
		firstUser := !a.hasUserMessage()
		a.echoUserMessage(components.Message{Role: "user", Content: text})
		return a.dispatchPrompt(text, nil, "", firstUser)
	}
	a.syncer.AckAnswer(ans.ID, sessionsync.AckRefused,
		fmt.Sprintf("unknown plan choice %q", choice), "")
	return nil
}

// ── Web answer payloads ──────────────────────────────────────────────────

// wireAnswer is one group's answer as the website sends it and the record
// stores it.
type wireAnswer struct {
	Group   int    `json:"group"`
	Chosen  []int  `json:"chosen,omitempty"`
	Note    string `json:"note,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

func wireAnswers(a clarify.Answers) []wireAnswer {
	out := make([]wireAnswer, 0, len(a.Items))
	for _, it := range a.Items {
		out = append(out, wireAnswer{Group: it.GroupIndex, Chosen: it.Chosen, Note: it.Note, Skipped: it.Skipped})
	}
	return out
}

// parseWebAnswers validates a web clarify payload against the open
// questionnaire: every group index in range and answered at most once, every
// chosen option in range and unique, a single-choice group given at most one,
// notes cleaned and capped. A group the web left out is skipped. A decline
// answers nothing, exactly as a host that dismisses the questions.
func parseWebAnswers(q clarify.Questionnaire, raw json.RawMessage) (clarify.Answers, bool, error) {
	var p struct {
		Answers []wireAnswer `json:"answers"`
		Decline bool         `json:"decline"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return clarify.Answers{}, false, fmt.Errorf("the answer could not be read")
	}
	if p.Decline {
		return clarify.Answers{}, true, nil
	}
	seen := map[int]bool{}
	byGroup := map[int]clarify.Answer{}
	for _, w := range p.Answers {
		if w.Group < 0 || w.Group >= len(q.Groups) {
			return clarify.Answers{}, false, fmt.Errorf("answer for question %d, which was not asked", w.Group+1)
		}
		if seen[w.Group] {
			return clarify.Answers{}, false, fmt.Errorf("question %d answered twice", w.Group+1)
		}
		seen[w.Group] = true
		g := q.Groups[w.Group]
		ans := clarify.Answer{GroupIndex: w.Group, Skipped: w.Skipped}
		if !w.Skipped {
			picked := map[int]bool{}
			for _, c := range w.Chosen {
				if c < 0 || c >= len(g.Options) {
					return clarify.Answers{}, false, fmt.Errorf("question %d has no option %d", w.Group+1, c+1)
				}
				if picked[c] {
					continue
				}
				picked[c] = true
				ans.Chosen = append(ans.Chosen, c)
			}
			if !g.Multi && len(ans.Chosen) > 1 {
				return clarify.Answers{}, false, fmt.Errorf("question %d takes one choice", w.Group+1)
			}
		}
		if note := sessionsync.CleanPrompt(w.Note); note != "" {
			if len(note) > maxAnswerNoteBytes {
				note = truncateUTF8(note, maxAnswerNoteBytes)
			}
			ans.Note = note
		}
		byGroup[w.Group] = ans
	}
	out := clarify.Answers{}
	for gi := range q.Groups {
		if ans, ok := byGroup[gi]; ok {
			out.Items = append(out.Items, ans)
		} else {
			out.Items = append(out.Items, clarify.Answer{GroupIndex: gi, Skipped: true})
		}
	}
	return out, false, nil
}
