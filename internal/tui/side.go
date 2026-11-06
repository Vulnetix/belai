package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/a2alink"
	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
)

// /btw (docs/side-question.md): a side question answered by a separate,
// tool-less model call on the session's model. Neither the question nor the
// answer joins the conversation, the session record, a compaction or a
// handoff: the answer is an Ephemeral system row that only the screen holds.

// sideState is the side question in flight. The UI loop owns it.
type sideState struct {
	busy bool
	// dismissed counts the /btw rows hidden from the thread, so the thread
	// filter only runs when there is one.
	dismissed int
}

// sideAnswerMsg is a finished side question. sessionID is the session it was
// asked in; an answer for a session the user has left is dropped.
type sideAnswerMsg struct {
	sessionID string
	question  string
	reply     string
	err       error
}

// sideTailBytes caps the recent conversation a side question reads.
const sideTailBytes = 8 << 10

// sideMessageRunes caps one message in that tail.
const sideMessageRunes = 1500

// sideFailed is shown for any failure. It is a fixed harness line: provider
// text never reaches the thread, and nothing is retried or sent to the main
// conversation.
const sideFailed = "btw: the side question could not be answered, nothing was added to the conversation"

// sideUsage is what /btw with no argument prints.
const sideUsage = "usage: /btw <question> asks a side question that stays out of the conversation; /btw clear removes the answers from the thread"

// sideRefusal says why /btw cannot start now, or "" when it can.
func (a *App) sideRefusal() string {
	switch {
	case !a.settings.SideEnabled():
		return "off (side.enabled is false)"
	case a.side.busy:
		return "a side question is already running"
	}
	return ""
}

// btwCommand handles /btw <question> and /btw clear. It is the user's alone:
// no model can start it.
func (a *App) btwCommand(arg string) tea.Cmd {
	q := strings.TrimSpace(arg)
	switch q {
	case "":
		a.addEphemeralSystem(sideUsage)
		return nil
	case "clear":
		a.clearSideRows()
		return nil
	}
	if why := a.sideRefusal(); why != "" {
		a.addEphemeralSystem("btw: " + why)
		return nil
	}
	sess := a.agent
	params := a.sessionBuildParams()
	in := agent.SideAskInput{
		Question:      q,
		Context:       a.sideContext(a.settings.SideHistoryTurns()),
		MaxReplyBytes: a.settings.SideMaxReplyBytes(),
	}
	sid := a.sessionID
	a.side.busy = true
	a.addEphemeralSystem("btw: asking a side question (not added to the conversation)")
	return func() tea.Msg {
		done := sideAnswerMsg{sessionID: sid, question: q}
		s := sess
		if s == nil {
			built, err := buildAgentSession(params)
			if err != nil {
				done.err = err
				return done
			}
			s = built
		}
		ctx, cancel := context.WithTimeout(context.Background(), a2alink.SideTurnTimeout)
		defer cancel()
		res, err := s.AskSide(ctx, in)
		// A side question that ran out of time or was cancelled shows nothing,
		// even if text arrived.
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		done.reply, done.err = res.Reply, err
		return done
	}
}

// handleSideAnswer shows a finished side question. The reply was sanitised and
// capped by the agent; it is written to the screen only.
func (a *App) handleSideAnswer(m sideAnswerMsg) tea.Cmd {
	a.side.busy = false
	if m.sessionID != a.sessionID {
		return nil
	}
	if m.err != nil || strings.TrimSpace(m.reply) == "" {
		a.addSideRow(sideFailed)
		return nil
	}
	a.addSideRow(fmt.Sprintf("side answer, not in history (/btw clear removes it)\nQ: %s\n%s",
		sanitize.Line(m.question, 200), m.reply))
	return nil
}

// addSideRow appends a render-only row marked as a /btw row.
func (a *App) addSideRow(text string) {
	a.messages = append(a.messages, components.Message{Role: "system", Content: text, Ephemeral: true, Side: true})
}

// clearSideRows hides every /btw row from the thread and says how many. The
// rows stay in memory, so no index into the thread moves.
func (a *App) clearSideRows() {
	hidden := 0
	for i := range a.messages {
		if a.messages[i].Side && !a.messages[i].Dismissed {
			a.messages[i].Dismissed = true
			hidden++
		}
	}
	if hidden == 0 {
		a.addEphemeralSystem("btw: no side answers in the thread")
		return
	}
	a.side.dismissed += hidden
	a.addEphemeralSystem(fmt.Sprintf("btw: removed %d side row(s) from the thread", hidden))
}

// sideContext is what a side question reads besides its question: the harness
// summary of the session, then the last turns user and assistant messages,
// newest kept first when the byte cap bites. Only text already in the
// conversation is used. Tool results, subagent rows, a failed attempt's
// messages and harness rows are never included, and the whole block is
// classified with the question when the prompt is admitted.
func (a *App) sideContext(turns int) string {
	var b strings.Builder
	b.WriteString(a.sessionContextSummary(turns <= 0))
	if tail := a.sideTail(turns); tail != "" {
		b.WriteString("Recent conversation, oldest first:\n")
		b.WriteString(tail)
	}
	return b.String()
}

// sideTail renders up to turns of the latest user and assistant messages.
func (a *App) sideTail(turns int) string {
	if turns <= 0 {
		return ""
	}
	var parts []string
	used := 0
	for i := len(a.messages) - 1; i >= 0 && len(parts) < turns; i-- {
		m := a.messages[i]
		if m.SubagentID != "" || m.Partial || m.Ephemeral || m.IsAttachment || (m.Role != "user" && m.Role != "assistant") {
			continue
		}
		text := strings.TrimSpace(sanitize.TextN(m.Text(), sideMessageRunes))
		if text == "" {
			continue
		}
		part := "[" + m.Role + "]\n" + text + "\n"
		if used+len(part) > sideTailBytes {
			break
		}
		used += len(part)
		parts = append(parts, part)
	}
	for l, r := 0, len(parts)-1; l < r; l, r = l+1, r-1 {
		parts[l], parts[r] = parts[r], parts[l]
	}
	return strings.Join(parts, "\n")
}
