package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/voicecmd"
)

// answerFromVoice marks an ask answered by a spoken keyword.
const answerFromVoice = "voice"

// voiceCommandReady reports whether the microphone should stay open for spoken
// keywords although the composer cannot take text: a permission ask, a
// clarification or a plan review is waiting for an answer. Needs listen mode,
// because push to talk is not listening. What is heard here only ever reaches
// voiceKeyword; nothing is written to the composer or sent to a model.
func (a *App) voiceCommandReady() bool {
	if !a.settings.Voice.VoiceCommandsEnabled() || a.voice.mode != config.VoiceModeListen {
		return false
	}
	return a.permAskOpen() || a.clarifyOpen() || a.planReviewOpen()
}

func (a *App) permAskOpen() bool {
	return a.view == viewPermissionAsk && a.permAskState.reply != nil
}

func (a *App) clarifyOpen() bool {
	return a.view == viewClarify && a.clarifyState.reply != nil && !a.clarifyState.noteMode
}

// planReviewOpen is true while the plan review waits for a choice. The same
// conditions the web's plan answer needs: no turn, no pre-send, no note being
// typed.
func (a *App) planReviewOpen() bool {
	return a.view == viewPlanReview && a.planReview.askID != "" && !a.planReview.noteMode &&
		!a.working() && !a.preSend
}

// voiceKeyword applies a spoken keyword to the ask or turn it names. It reports
// whether it did anything: a keyword with nothing to apply to is ordinary
// speech. The keyword is a fixed vocabulary match (internal/voicecmd), and it
// goes through the same functions the keys do, so every check they make holds.
func (a *App) voiceKeyword(k voicecmd.Keyword, n int) (bool, tea.Cmd) {
	switch {
	case a.permAskOpen():
		decision := ""
		switch k {
		case voicecmd.Approve:
			decision = askAllowOnce
		case voicecmd.ApproveAlways:
			// Writes a persistent allow rule, so only the exact phrase.
			decision = askAllowAlways
		case voicecmd.Deny:
			decision = askDeny
		default:
			return false, nil
		}
		a.settlePermissionAsk(decision, answerFromVoice, "")
		a.voiceAck(string(k))
		return true, a.nextAgent()
	case a.clarifyOpen():
		return a.voiceClarify(k, n)
	case a.planReviewOpen():
		switch k {
		case voicecmd.Approve:
			a.recordPlanChoice(planChoiceApproveHere, answerFromVoice, "", "")
			a.voiceAck("approve")
			return true, a.submitPlanApprove()
		case voicecmd.Deny:
			a.recordPlanChoice(planChoiceStay, answerFromVoice, "", "")
			a.voiceAck("deny")
			return true, a.submitPlanStay()
		}
		return false, nil
	case k == voicecmd.Stop && a.view == viewChat && (a.preSend || a.working()):
		if a.cancelPreSend() || a.cancelTurn() {
			a.voiceAck("stop")
			return true, nil
		}
	}
	return false, nil
}

// voiceClarify applies option N, submit and skip to the open questionnaire.
// An option number goes to the first group that has no answer yet, and is
// refused when that group has no such option. A single-choice group that is
// answered last sends the answers, so "option 2" alone is select and proceed.
func (a *App) voiceClarify(k voicecmd.Keyword, n int) (bool, tea.Cmd) {
	st := &a.clarifyState
	switch k {
	case voicecmd.Option:
		g := st.firstOpenGroup()
		if g < 0 || n < 1 || n > len(st.q.Groups[g].Options) {
			return false, nil
		}
		for i, r := range st.rows {
			if r.kind == clarifyRowOption && r.groupIdx == g && r.optionIdx == n-1 {
				st.selected = i
				st.toggleChoice(i)
				break
			}
		}
		a.voiceAck("option " + string(rune('0'+n)))
		if !st.q.Groups[g].Multi && st.firstOpenGroup() < 0 {
			return true, a.voiceSubmitClarify()
		}
		return true, nil
	case voicecmd.Skip:
		g := st.firstOpenGroup()
		if g < 0 {
			return false, nil
		}
		st.skipGroup(g)
		a.voiceAck("skip")
		if st.firstOpenGroup() < 0 {
			return true, a.voiceSubmitClarify()
		}
		return true, nil
	case voicecmd.Submit:
		a.voiceAck("submit")
		return true, a.voiceSubmitClarify()
	}
	return false, nil
}

// voiceSubmitClarify sends the answers shown, as Enter does.
func (a *App) voiceSubmitClarify() tea.Cmd {
	a.clarifyState.chooseHighlighted()
	a.settleClarify(a.clarifyState.buildAnswers(), answerFromVoice, "", false)
	return a.nextAgent()
}

// firstOpenGroup is the first group with no chosen option and not skipped, or
// -1 when every group has an answer.
func (s *clarifyViewState) firstOpenGroup() int {
	for g := range s.q.Groups {
		if !s.skipped[g] && len(s.chosen[g]) == 0 {
			return g
		}
	}
	return -1
}

// voiceAck shows what a spoken keyword did. Always shown: an action taken by
// voice must never be silent.
func (a *App) voiceAck(what string) {
	a.addSystem("voice: " + what)
}
