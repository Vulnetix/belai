package tui

import (
	"context"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/voice"
)

// Dictation reaches the composer in three steps, so the words appear as they
// are said rather than after the last of them: a running guess while you are
// still speaking, the recognised text the moment you stop, and then the fast
// model's tidy-up streamed over it, replacing the raw words as it writes.
// All three are the same stretch of the composer, the live span. Typing
// anything cancels the lot.

// liveSpan is the stretch of the composer that dictation is currently writing.
type liveSpan struct {
	active bool
	start  int    // rune offset of its first rune in the composer
	text   string // what the composer holds there now
	raw    string // the recognised text behind it, kept for tagging the turn
}

// voiceCleanMsg is one event from a streaming tidy-up: a piece of the cleaned
// text so far, or the end. gen names the tidy-up it belongs to, so events from
// one that was cancelled are ignored. ch is carried so the handler can wait for
// the next event.
type voiceCleanMsg struct {
	gen   int
	ch    <-chan voiceCleanMsg
	delta string // the cleaned text so far
	done  bool
	raw   string
	text  string
	err   error
}

// voiceLiveSet makes the composer hold text as the live dictation. The first
// call inserts it at the cursor; later calls replace what the last one put
// there. It returns false, and forgets the span, if you have changed that
// stretch of the composer in the meantime, since it is then yours.
func (a *App) voiceLiveSet(raw, text string) bool {
	v := &a.voice
	if !v.live.active {
		val := []rune(a.editor.Value())
		off := a.editor.CursorOffset()
		if off > len(val) {
			off = len(val)
		}
		prefix := ""
		if off > 0 && !unicode.IsSpace(val[off-1]) {
			prefix = " "
		}
		a.editor.ReplaceRange(off, off, prefix+text)
		v.live = liveSpan{active: true, start: off + len([]rune(prefix)), text: text, raw: raw}
	} else {
		runes := []rune(a.editor.Value())
		end := v.live.start + len([]rune(v.live.text))
		if end > len(runes) || string(runes[v.live.start:end]) != v.live.text {
			v.live = liveSpan{}
			return false
		}
		a.editor.ReplaceRange(v.live.start, end, text)
		v.live.text = text
		if raw != "" {
			v.live.raw = raw
		}
	}
	a.refreshAutocomplete()
	a.relayout()
	return true
}

// voiceLiveEnd settles the span: what it holds stays in the composer, and is
// remembered as dictated so the turn it ends up in is tagged.
func (a *App) voiceLiveEnd() {
	v := &a.voice
	if v.live.active {
		v.noteDictated(v.live.raw, strings.TrimSpace(v.live.text))
	}
	v.live = liveSpan{}
}

// voicePartial shows a running guess at what is being said. It only ever
// replaces its own earlier guess, and it stays out of the way while the fast
// model is working on the previous phrase.
func (a *App) voicePartial(text string) {
	v := &a.voice
	text = strings.TrimSpace(sanitize.Text(text))
	if text == "" || !v.ready || v.busy {
		return
	}
	if !a.voiceLiveSet(text, text) {
		// The stretch was edited: the words are the user's now. Stop guessing
		// for this phrase; the final text is inserted normally.
		return
	}
}

// voiceBegin shows a final transcript in the composer at once, then starts the
// fast model's tidy-up of it. The cleaned text streams over the raw text as it
// is written; if there is no fast model, or it fails, the raw text stays.
func (a *App) voiceBegin(raw string) tea.Cmd {
	v := &a.voice
	if !a.voiceLiveSet(raw, raw) {
		// The guess was edited away, or there was none: insert afresh.
		v.live = liveSpan{}
		a.voiceLiveSet(raw, raw)
	}
	if !a.settings.Voice.VoiceCleanupEnabled() || a.classifier == nil {
		return a.voiceFinish(raw, raw)
	}
	return a.voiceCleanStream(raw)
}

// voiceCleanStream runs the tidy-up in the background and reports each piece
// of the cleaned text through voiceCleanMsg.
func (a *App) voiceCleanStream(raw string) tea.Cmd {
	v := &a.voice
	v.busy = true
	v.cleanGen++
	gen := v.cleanGen
	ctx, cancel := context.WithTimeout(context.Background(), voiceCleanupTimeout)
	v.cleanCancel = cancel
	c := a.classifier
	ch := make(chan voiceCleanMsg, 64)
	go func() {
		defer close(ch)
		defer cancel()
		text, err := rolemanager.CleanVoiceStream(ctx, c, raw, func(so string) {
			select {
			case ch <- voiceCleanMsg{gen: gen, ch: ch, delta: so}:
			case <-ctx.Done():
			}
		})
		ch <- voiceCleanMsg{gen: gen, ch: ch, done: true, raw: raw, text: text, err: err}
	}()
	return voiceCleanWait(ch)
}

func voiceCleanWait(ch <-chan voiceCleanMsg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			return nil
		}
		return m
	}
}

// handleVoiceClean applies one piece of a streaming tidy-up.
func (a *App) handleVoiceClean(m voiceCleanMsg) tea.Cmd {
	v := &a.voice
	if m.gen != v.cleanGen {
		// A tidy-up that was cancelled: drain it so its goroutine can finish.
		if m.done {
			return nil
		}
		return voiceCleanWait(m.ch)
	}
	if !m.done {
		if v.live.active {
			a.voiceLiveSet("", m.delta)
		}
		return voiceCleanWait(m.ch)
	}
	v.busy = false
	v.cleanCancel = nil
	text := m.text
	if m.err != nil || strings.TrimSpace(text) == "" {
		// A failed tidy-up never loses what was said: put the raw text back.
		text = m.raw
	}
	if v.live.active {
		a.voiceLiveSet(m.raw, text)
	}
	return a.voiceFinish(m.raw, text)
}

// voiceFinish settles the live span and, with submit delivery, sends the
// composer the way Enter would. Then the next queued phrase starts.
func (a *App) voiceFinish(raw, text string) tea.Cmd {
	a.voiceLiveEnd()
	return tea.Batch(a.voiceMaybeSubmit(), a.voiceNext())
}

// voiceMaybeSubmit sends the composer through the Enter path when delivery is
// submit, unless what would be sent could run something: a line starting with
// / or ! is a local command, and an @path attaches a file, and dictation must
// never reach either by itself.
func (a *App) voiceMaybeSubmit() tea.Cmd {
	if a.settings.Voice.VoiceDeliveryOr() != config.VoiceDeliverySubmit {
		return nil
	}
	if reason := voiceSubmitBlock(a.editor.Value()); reason != "" {
		a.voiceNote("voice: inserted but not sent, because it " + reason + "; press enter to send it")
		return nil
	}
	return a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
}

// voiceActivity reports whether voice is doing anything a keystroke should
// stop: text being written, a tidy-up running, phrases waiting, a recording in
// progress or speech being heard.
func (a *App) voiceActivity() bool {
	v := &a.voice
	if v.eng == nil {
		return false
	}
	if v.live.active || v.busy || len(v.queue) > 0 || v.held != "" || v.phase != pttIdle {
		return true
	}
	switch v.eng.State() {
	case voice.StateHearing, voice.StateTranscribing:
		return true
	case voice.StateListening:
		return v.mode == config.VoiceModePushToTalk // a push-to-talk recording
	}
	return false
}

// voiceCancelFlow cancels all voice activity at once, and reports whether there
// was any. It is what typing does. The recording ends and the microphone
// closes (always-listening stays open for the next phrase), a tidy-up in
// flight is cut off and its result ignored, nothing waiting is kept, and the
// Enter that submit delivery would press never comes. Text already in the
// composer stays: it is yours to keep or delete.
func (a *App) voiceCancelFlow() bool {
	v := &a.voice
	if !a.voiceActivity() {
		return false
	}
	a.voiceDetach()
	v.queue, v.held, v.heldRaw = nil, "", ""
	v.phase = pttIdle
	v.seq++
	v.eng.Cancel()
	a.refreshFooter()
	return true
}

// voiceDetach stops a tidy-up in flight and lets go of the live span. What the
// composer already holds stays; it is remembered as dictated.
func (a *App) voiceDetach() {
	v := &a.voice
	if v.cleanCancel != nil {
		v.cleanCancel()
		v.cleanCancel = nil
	}
	v.cleanGen++
	v.busy = false
	a.voiceLiveEnd()
}

// voiceNote adds an automatic voice notice to the transcript, unless the voice
// log is off. Answers to a command you typed are always shown.
func (a *App) voiceNote(text string) {
	if a.settings.Voice.VoiceLogEnabled() {
		a.addSystem(text)
	}
}
