package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/voicecmd"
)

// voiceWakeWindow is how long a bare "Hey, Belay" keeps listening for the
// instruction that follows it.
const voiceWakeWindow = 8 * time.Second

// voiceGate decides what a final transcript may do, before anything is shown or
// sent. It returns the text to dictate; done is true when the transcript was
// consumed (a keyword acted, the wake word was missing, or only commands were
// wanted) and cmd is what the consumption started.
//
//  1. A bare keyword that applies to an open ask or a running turn acts.
//  2. With the wake word on, speech that does not start with "Hey, Belay" is
//     dropped here: never queued, shown, tidied by a model or recorded. A match
//     is stripped, so later steps see only what followed it.
//  3. Commands-only (an ask is open, the composer is not): nothing else passes.
func (a *App) voiceGate(raw string) (text string, cmd tea.Cmd, done bool) {
	v := &a.voice
	s := a.settings.Voice
	commands := s.VoiceCommandsEnabled()
	if commands {
		if k, n, ok := voicecmd.MatchKeyword(raw); ok {
			if applied, cmd := a.voiceKeyword(k, n); applied {
				a.voiceDropLive()
				return "", cmd, true
			}
		}
	}
	text = raw
	if s.VoiceWakeWordEnabled() {
		rest, ok := voicecmd.MatchWake(raw)
		armed := time.Now().Before(v.wakeUntil)
		switch {
		case ok && rest == "":
			v.wakeUntil = time.Now().Add(voiceWakeWindow)
			a.voiceNote("voice: listening")
			return "", nil, true
		case ok:
			text = rest
		case armed:
			// The instruction after a bare wake word, said in its own breath.
		default:
			return "", nil, true
		}
		v.wakeUntil = time.Time{}
		if commands {
			if k, n, ok := voicecmd.MatchKeyword(text); ok {
				if applied, cmd := a.voiceKeyword(k, n); applied {
					return "", cmd, true
				}
			}
		}
	}
	if v.ready && a.voiceMatchable(text) {
		if targets := a.voiceTargets(); len(targets) > 0 {
			return "", a.voiceMatchCmd(strings.TrimSpace(text), targets), true
		}
	}
	if !v.ready && a.voiceCommandReady() {
		// Only keywords are heard while an ask is open.
		return "", nil, true
	}
	return strings.TrimSpace(text), nil, false
}

// voiceDropLive removes the running guess from the composer when the phrase
// turned out to be a spoken command, so a keyword is never left behind as text.
// It leaves the span alone if the user has edited it.
func (a *App) voiceDropLive() {
	v := &a.voice
	if !v.live.active {
		return
	}
	runes := []rune(a.editor.Value())
	end := v.live.start + len([]rune(v.live.text))
	if end <= len(runes) && string(runes[v.live.start:end]) == v.live.text {
		a.editor.ReplaceRange(v.live.start, end, "")
		a.refreshAutocomplete()
		a.relayout()
	}
	v.live = liveSpan{}
}
