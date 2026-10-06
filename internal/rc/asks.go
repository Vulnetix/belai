package rc

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/clarify"
	"github.com/vulnetix/belai/internal/session"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/turnlog"
	"github.com/vulnetix/belai/internal/webask"
)

// DefaultAskWait is how long a remote session waits for a web answer before
// it denies the call (or skips the questions) and moves on.
const DefaultAskWait = 10 * time.Minute

// AnswerMirror is the part of sessionsync.Syncer the ask bridge drives.
type AnswerMirror interface {
	Answers() <-chan sessionsync.RemoteAnswer
	AckAnswer(id, status, reason, entryID string)
	Nudge()
}

// askBridge answers a remote session's asks from the web when ask is on
// (`belai rc --web-controls`). Each ask is written to the transcript as the
// TUI writes it (internal/webask), so the website shows and answers it the
// same way; a web answer is applied only to the ask open now and only after it
// validates. Anything but an explicit allow denies, an unanswered ask denies
// after Wait, and allow-always is remembered in memory for this session only:
// a web answer never writes a permission rule to a settings file.
type askBridge struct {
	log    *turnlog.Log
	mirror AnswerMirror
	wait   time.Duration

	mu     sync.Mutex
	always map[string]bool
	// off is closed when ask is turned off, to release the ask open now.
	off chan struct{}
}

func newAskBridge(log *turnlog.Log, m AnswerMirror, wait time.Duration) *askBridge {
	if wait <= 0 {
		wait = DefaultAskWait
	}
	return &askBridge{log: log, mirror: m, wait: wait, always: map[string]bool{}, off: make(chan struct{})}
}

// releaseOpen settles the ask open now, if any, because ask was turned off.
// Turning ask off also turns web answers off, so an ask raised while it was on
// could never be answered and would hold the turn for the whole wait. Belai's
// rule for ask off is that every ask resolves to allow (AskDisabled), so the
// open permission is allowed and a questionnaire is dismissed, each recorded in
// the transcript so the website closes it.
func (b *askBridge) releaseOpen() {
	b.mu.Lock()
	close(b.off)
	b.off = make(chan struct{})
	b.mu.Unlock()
}

// released is the channel releaseOpen will close for an ask that starts waiting now.
func (b *askBridge) released() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.off
}

// awaited is how a wait for a web answer ended.
type awaited int

const (
	awaitedAnswer   awaited = iota // an accepted web answer
	awaitedEnded                   // the session stopped or the wait ran out; the ask is settled
	awaitedReleased                // ask was turned off; the caller settles it
)

// record writes an ask entry and returns its id ("" when nothing was written).
func (b *askBridge) record(kind, content string, meta map[string]any) string {
	w := b.log.Writer()
	if w == nil {
		return ""
	}
	id, err := session.NewID()
	if err != nil {
		return ""
	}
	meta["ask_id"], meta["kind"] = id, kind
	if w.Entry(session.Entry{ID: id, Type: "ask", Role: "system", Content: content, Meta: meta}) != id {
		return ""
	}
	b.mirror.Nudge()
	return id
}

// settle writes the ask_answer entry for askID and returns its id.
func (b *askBridge) settle(askID, kind, source, remoteID, content string, meta map[string]any) string {
	w := b.log.Writer()
	if w == nil {
		return ""
	}
	meta["ask_id"], meta["kind"], meta["source"] = askID, kind, source
	if remoteID != "" {
		meta["remote_answer_id"] = remoteID
	}
	id := w.Entry(session.Entry{ID: webask.AnswerEntryID(askID), Type: "ask_answer", Role: "system", Content: content, Meta: meta})
	b.mirror.Nudge()
	return id
}

// await waits for the web answer to askID. accept validates and applies one
// answer, returning the ask_answer entry id or a refusal reason. off is the
// release channel read before the ask was recorded, so an ask turned off in
// between is not missed. It says how the wait ended.
func (b *askBridge) await(ctx context.Context, off <-chan struct{}, askID, kind string, accept func(sessionsync.RemoteAnswer) (string, string)) awaited {
	timer := time.NewTimer(b.wait)
	defer timer.Stop()
	answers := b.mirror.Answers()
	for {
		select {
		case <-ctx.Done():
			b.settle(askID, kind, webask.FromHost, "", "", map[string]any{"closed": true, "reason": "the session stopped"})
			return awaitedEnded
		case <-timer.C:
			b.settle(askID, kind, webask.FromHost, "", "", map[string]any{"closed": true, "reason": "no answer arrived in time"})
			return awaitedEnded
		case <-off:
			return awaitedReleased
		case ans, ok := <-answers:
			if !ok {
				answers = nil
				continue
			}
			if ans.AskID != askID {
				b.mirror.AckAnswer(ans.ID, sessionsync.AckRefused, "this question is no longer open on the host: it was answered there, or the turn moved on", "")
				continue
			}
			if ans.Kind != kind {
				b.mirror.AckAnswer(ans.ID, sessionsync.AckRefused, "the answer does not fit this question", "")
				continue
			}
			entryID, why := accept(ans)
			if why != "" {
				b.mirror.AckAnswer(ans.ID, sessionsync.AckRefused, why, "")
				continue
			}
			b.mirror.AckAnswer(ans.ID, sessionsync.AckAccepted, "", entryID)
			return awaitedAnswer
		}
	}
}

// permission asks the web whether a tool call may run.
func (b *askBridge) permission(ctx context.Context, ask *agent.AskRequest) bool {
	if ask == nil {
		return false
	}
	rule := webask.Rule(ask)
	b.mu.Lock()
	always := b.always[rule]
	b.mu.Unlock()
	if always {
		return true
	}
	content, meta := webask.PermissionAsk(ask)
	off := b.released()
	askID := b.record(webask.KindPermission, content, meta)
	if askID == "" {
		return false
	}
	allow := false
	got := b.await(ctx, off, askID, webask.KindPermission, func(ans sessionsync.RemoteAnswer) (string, string) {
		decision, err := webask.ParseDecision(ans.Payload)
		if err != nil {
			return "", err.Error()
		}
		allow = decision != webask.Deny
		if decision == webask.AllowAlways {
			b.mu.Lock()
			b.always[rule] = true
			b.mu.Unlock()
		}
		text := "permission for " + ask.Name + ": " + strings.ReplaceAll(decision, "_", " ") + " (answered on the web)"
		return b.settle(askID, webask.KindPermission, webask.FromWeb, ans.ID, text, map[string]any{"decision": decision}), ""
	})
	if got == awaitedReleased {
		b.settle(askID, webask.KindPermission, webask.FromHost, "", "permission for "+ask.Name+": allow once (ask was turned off)", map[string]any{"decision": webask.AllowOnce, "ask_off": true})
		return true
	}
	return allow
}

// clarify puts a questionnaire (or the mode choice) to the web. An unanswered
// or declined one answers nothing, as a host that dismisses the questions.
func (b *askBridge) clarify(ctx context.Context, q *clarify.Questionnaire, modeChoice bool) clarify.Answers {
	if q == nil {
		return clarify.Answers{}
	}
	kind := webask.KindClarify
	if modeChoice {
		kind = webask.KindModeChoice
	}
	content := "questions from the agent"
	if modeChoice && len(q.Groups) > 0 {
		content = "Mode choice: " + q.Groups[0].Context
	}
	off := b.released()
	askID := b.record(kind, content, map[string]any{"questionnaire": q})
	if askID == "" {
		return clarify.Answers{}
	}
	var out clarify.Answers
	got := b.await(ctx, off, askID, kind, func(ans sessionsync.RemoteAnswer) (string, string) {
		answers, declined, err := webask.ParseClarify(*q, ans.Payload)
		if err != nil {
			return "", err.Error()
		}
		text := answers.Render(*q)
		if declined {
			text = "clarification declined"
		}
		out = answers
		return b.settle(askID, kind, webask.FromWeb, ans.ID, text+"\n(answered on the web)", map[string]any{
			"answers": webask.WireAnswers(answers), "declined": declined,
		}), ""
	})
	if got == awaitedReleased {
		b.settle(askID, kind, webask.FromHost, "", "clarification dismissed (ask was turned off)", map[string]any{"closed": true, "reason": "ask was turned off", "ask_off": true})
		return clarify.Answers{}
	}
	return out
}
