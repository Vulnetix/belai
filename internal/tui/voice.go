package tui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voice"
)

// Voice input (docs/voice.md). The engine in internal/voice owns the
// microphone and the model; this file owns when its text may appear.

const (
	// voiceTapGrace is how long after a first press the TUI waits for key
	// repeat before it decides the press was a tap. It sits above the usual
	// X11 repeat delay (660 ms), since terminals report no key release.
	voiceTapGrace = 700 * time.Millisecond
	// voiceRepeatGap is the silence after the last repeat that ends a hold.
	voiceRepeatGap = 250 * time.Millisecond
	// voiceHeldMax bounds dictation waiting for a ready composer.
	voiceHeldMax = 4000
	// voiceQueueMax bounds transcripts waiting for cleanup.
	voiceQueueMax = 8
	// voiceCleanupTimeout bounds one fast-model cleanup call.
	voiceCleanupTimeout = 20 * time.Second
)

type pttPhase int

const (
	pttIdle     pttPhase = iota
	pttPressing          // first press seen, waiting to learn tap or hold
	pttHeld              // key repeat seen: recording until the repeats stop
	pttLatched           // a tap: recording until the next tap
)

// voiceState is the TUI's side of voice input.
type voiceState struct {
	eng  *voice.Engine
	stop context.CancelFunc

	// autostart is set by Start only, so New (every test) never opens a
	// microphone.
	autostart bool

	mode      string // the engine's mode; delivery, cleanup and key are read live
	muted     bool
	ready     bool      // the composer can take dictation
	engReady  bool      // the readiness last told to the engine: the composer, or an ask waiting for a keyword
	wakeUntil time.Time // a bare "Hey, Belay" counts the next utterance until then
	jobs      *jev.Jobs // Jev job runner for spoken instructions, see voice_command.go
	jobsKey   string
	cmdGen    int // which spoken-instruction match is current; typing bumps it

	phase       pttPhase
	seq         int
	held        string             // dictation waiting for a ready composer
	heldRaw     string             // the recognised text behind held
	dictated    []dictation        // what was inserted since the last send, for tagging the turn
	queue       []string           // raw transcripts waiting for cleanup
	busy        bool               // one cleanup call in flight
	wantOn      bool               // /voice on was asked while the model was missing
	hinted      bool               // the off-state key hint was shown this session
	frame       int                // animation frame, advanced while something on screen moves
	animating   bool               // a frame timer is running
	live        liveSpan           // the stretch of the composer dictation is writing
	cleanGen    int                // which streaming tidy-up is current
	cleanCancel context.CancelFunc // stops the streaming tidy-up

	downloading bool

	// Seams for tests; nil means the real implementation.
	newSource func(device string) (voice.Source, error)
	load      func(ctx context.Context) (voice.Recognizer, error)
	modelPath func() string
}

type (
	voiceEventMsg struct {
		eng *voice.Engine
		ev  voice.Event
	}
	voiceCleanedMsg struct{ raw, text string }
	voiceHoldMsg    struct{ seq int }
	voiceModelMsg   struct {
		path string
		err  error
	}
)

func (v *voiceState) source(device string) (voice.Source, error) {
	if v.newSource != nil {
		return v.newSource(device)
	}
	return voice.NewExecSource(device)
}

func (v *voiceState) model() string {
	if v.modelPath != nil {
		return v.modelPath()
	}
	return voice.ModelSource()
}

func (v *voiceState) recognizer(ctx context.Context) (voice.Recognizer, error) {
	if v.load != nil {
		return v.load(ctx)
	}
	m, err := voice.LoadModel()
	if err != nil {
		return nil, err
	}
	return m, nil
}

// voiceInit is called from Init. It brings voice up only for a real run
// whose settings turn it on.
func (a *App) voiceInit() tea.Cmd {
	if !a.voice.autostart || !a.voiceWanted() {
		return nil
	}
	// A start nobody asked for (voice is on because the model is built in) is
	// quiet: a machine with no microphone helper is not told so on every launch.
	// The voice key says why when it is pressed.
	return a.voiceStart(a.settings.Voice == nil || a.settings.Voice.Enabled == nil)
}

// voiceComposerReady reports whether dictated text could appear in the chat
// composer right now. Any screen or overlay that owns the keyboard, or a
// prompt still being classified, makes it false. A running turn does not:
// typing during a turn steers it, so dictation may too.
func (a *App) voiceComposerReady() bool {
	return a.view == viewChat &&
		!a.promptAction && !a.saveFileMode && !a.savePromptMode && !a.historyActive &&
		a.forgeConfirm.kind == forgeConfirmNone && a.forgeInput.kind == forgeInputNone &&
		!a.kanbanInputActive() && !a.runsFocus && !(a.kb != nil && a.kb.pane.focus) &&
		!a.dirPickState.open && !a.rootConfirmVisible() && !a.filePickerVisible() &&
		!a.agentPickerOpen && !a.agentPickerVisible() && len(a.autocomplete) == 0 &&
		!a.preSend && a.pendingInput == "" && !a.editor.Masked
}

// syncVoice tells the engine whether the composer can take text and delivers
// dictation that was waiting. Update runs it after every message.
func (a *App) syncVoiceReady() tea.Cmd {
	v := &a.voice
	if v.eng == nil {
		return nil
	}
	ready := a.voiceComposerReady()
	// The microphone also stays open while an ask waits for a spoken keyword.
	// That is commands-only: v.ready stays false, so nothing is dictated.
	engine := ready || a.voiceCommandReady()
	if engine != v.engReady {
		v.engReady = engine
		v.eng.SetReady(engine)
	}
	if ready == v.ready {
		return nil
	}
	v.ready = ready
	if !ready {
		// The composer cannot take text: a tidy-up in flight is cut off and the
		// live span let go. Phrases already queued wait for the composer.
		a.voiceDetach()
		v.phase = pttIdle
		v.seq++
		return nil
	}
	return a.voiceDeliverHeld()
}

func (a *App) watchVoice(e *voice.Engine) tea.Cmd {
	return func() tea.Msg {
		select {
		case ev := <-e.Events():
			return voiceEventMsg{eng: e, ev: ev}
		case <-e.Done():
			return nil
		}
	}
}

// handleVoiceMsg handles the voice messages and the voice key.
func (a *App) handleVoiceMsg(msg tea.Msg) (tea.Cmd, bool) {
	if cmd, ok := a.handleVoiceDebugMsg(msg); ok {
		return cmd, true
	}
	v := &a.voice
	switch m := msg.(type) {
	case voiceEventMsg:
		if m.eng != v.eng {
			return nil, true // an engine that was turned off
		}
		cmd := a.watchVoice(m.eng)
		switch m.ev.Kind {
		case voice.EventState:
			if v.phase == pttLatched && m.ev.State != voice.StateListening && m.ev.State != voice.StateHearing {
				v.phase = pttIdle // the tap ended itself
			}
			a.refreshFooter()
		case voice.EventError:
			a.voiceNote("voice: " + sanitize.Line(m.ev.Err.Error(), 240))
			a.refreshFooter()
		case voice.EventPartial:
			// A running guess is raw text, so it never shows while the wake
			// word gates dictation: only a matched utterance may appear.
			if !a.settings.Voice.VoiceWakeWordEnabled() {
				a.voicePartial(m.ev.Text)
			}
		case voice.EventTranscript:
			cmd = tea.Batch(cmd, a.voiceTranscript(m.ev.Text))
		}
		return cmd, true
	case voiceCommandMsg:
		return a.handleVoiceCommand(m), true
	case voiceCleanMsg:
		return a.handleVoiceClean(m), true
	case voiceCleanedMsg:
		return a.voiceCleaned(m), true
	case voiceAnimMsg:
		v.frame++
		if a.voiceAnimating() {
			return voiceAnimTick(), true
		}
		v.animating = false
		return nil, true
	case voiceHoldMsg:
		a.voiceHold(m.seq)
		return nil, true
	case voiceModelMsg:
		return a.voiceModelDone(m), true
	case tea.KeyMsg:
		if v.eng != nil && a.view == viewChat && voiceKeyMatches(m.String(), a.settings.Voice.VoiceKeyOr()) {
			return a.voiceKey(), true
		}
		if v.eng == nil && a.view == viewChat && voiceKeyMatches(m.String(), a.settings.Voice.VoiceKeyOr()) {
			return a.voiceOffHint(), true
		}
		// Anything else typed in the composer takes over from voice at once:
		// what it was recording, writing or about to send is cancelled. The key
		// then goes on to the composer as usual.
		if a.view == viewChat {
			a.voiceCancelFlow()
		}
	}
	return nil, false
}

func voiceKeyMatches(pressed, want string) bool {
	if want == "ctrl+space" {
		return pressed == "ctrl+@" || pressed == "ctrl+space"
	}
	return pressed == want
}

// voiceKey handles a press of the voice key. Terminals report no key release,
// so a hold is read from key repeat: the first press starts recording; repeats
// mean a hold, which ends when they stop; no repeat within voiceTapGrace means
// a tap, which records until the next tap.
func (a *App) voiceKey() tea.Cmd {
	v := &a.voice
	if !v.ready {
		return nil // the composer cannot take text, so nothing is recorded
	}
	if v.mode == config.VoiceModeListen {
		v.muted = !v.muted
		v.eng.SetEnabled(!v.muted)
		a.refreshFooter()
		return nil
	}
	switch v.phase {
	case pttIdle:
		v.eng.PTTDown()
		v.phase = pttPressing
		v.seq++
		a.refreshFooter()
		return voiceHoldTick(v.seq, voiceTapGrace)
	case pttPressing, pttHeld:
		v.phase = pttHeld
		v.seq++
		return voiceHoldTick(v.seq, voiceRepeatGap)
	case pttLatched:
		v.eng.PTTUp()
		v.phase = pttIdle
		a.refreshFooter()
	}
	return nil
}

func voiceHoldTick(seq int, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return voiceHoldMsg{seq: seq} })
}

func (a *App) voiceHold(seq int) {
	v := &a.voice
	if seq != v.seq || v.eng == nil {
		return
	}
	switch v.phase {
	case pttPressing:
		v.phase = pttLatched
		v.eng.PTTLatch() // a tap ends by itself when the speaker stops
	case pttHeld:
		v.eng.PTTUp()
		v.phase = pttIdle
		a.refreshFooter()
	}
}

// voiceTranscript takes one raw transcript from the engine.
func (a *App) voiceTranscript(raw string) tea.Cmd {
	raw = strings.TrimSpace(sanitize.Text(raw))
	if raw == "" {
		return nil
	}
	text, cmd, done := a.voiceGate(raw)
	if done {
		return cmd
	}
	return a.voiceEnqueue(text)
}

// voiceEnqueue queues text for cleanup and insertion.
func (a *App) voiceEnqueue(text string) tea.Cmd {
	v := &a.voice
	if len(v.queue) >= voiceQueueMax {
		v.queue = v.queue[1:]
	}
	v.queue = append(v.queue, text)
	return a.voiceNext()
}

// voiceNext starts the cleanup of the next queued transcript, one at a time so
// phrases keep their order. With the composer ready the phrase is shown at once
// and tidied in place; otherwise it is tidied out of sight and held.
func (a *App) voiceNext() tea.Cmd {
	v := &a.voice
	if v.busy || len(v.queue) == 0 {
		return nil
	}
	raw := v.queue[0]
	v.queue = v.queue[1:]
	if v.ready {
		// The words go into the composer at once and the tidy-up streams over
		// them (voice_live.go).
		return a.voiceBegin(raw)
	}
	// The composer cannot take text right now: tidy the phrase out of sight and
	// hold the result until it can.
	if !a.settings.Voice.VoiceCleanupEnabled() || a.classifier == nil {
		return a.voiceCleaned(voiceCleanedMsg{raw: raw, text: raw})
	}
	v.busy = true
	c := a.classifier
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), voiceCleanupTimeout)
		defer cancel()
		text, err := rolemanager.CleanVoice(ctx, c, raw)
		if err != nil {
			text = raw // a failed cleanup never loses what was said
		}
		return voiceCleanedMsg{raw: raw, text: text}
	}
}
func (a *App) voiceCleaned(m voiceCleanedMsg) tea.Cmd {
	a.voice.busy = false
	return tea.Batch(a.voiceDeliver(m.raw, m.text), a.voiceNext())
}

// voiceDeliver puts text in the composer if it is ready, else holds it.
func (a *App) voiceDeliver(raw, text string) tea.Cmd {
	text = strings.TrimSpace(sanitize.Text(text))
	if text == "" {
		return nil
	}
	v := &a.voice
	if !v.ready {
		v.held = strings.TrimSpace(v.held + " " + text)
		v.heldRaw = strings.TrimSpace(v.heldRaw + " " + raw)
		if len(v.held) > voiceHeldMax {
			v.held = v.held[len(v.held)-voiceHeldMax:]
		}
		return nil
	}
	return a.voiceInsert(raw, text)
}

func (a *App) voiceDeliverHeld() tea.Cmd {
	v := &a.voice
	text := v.held
	raw := v.heldRaw
	v.heldRaw = ""
	v.held = ""
	if text == "" {
		return nil
	}
	return a.voiceInsert(raw, text)
}

// voiceInsert places text at the cursor. With submit delivery it then sends
// the composer through the same path as pressing Enter, unless what would be
// sent could run something: a line starting with / or ! is a local command, and
// an @path attaches a file, and dictation must never reach either by itself.
func (a *App) voiceInsert(raw, text string) tea.Cmd {
	val := []rune(a.editor.Value())
	off := a.editor.CursorOffset()
	if off > len(val) {
		off = len(val)
	}
	if off > 0 && !unicode.IsSpace(val[off-1]) {
		text = " " + text
	}
	a.editor.ReplaceRange(off, off, text)
	a.voice.noteDictated(raw, strings.TrimSpace(text))
	a.refreshAutocomplete()
	a.relayout()
	return a.voiceMaybeSubmit()
}

// voiceSubmitBlock says why composer text must not be sent automatically, or
// "" when it may.
func voiceSubmitBlock(s string) string {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "/"):
		return "starts with /, which runs a command"
	case strings.HasPrefix(t, "!"):
		return "starts with !, which runs a shell command"
	}
	for i, r := range t {
		if r == '@' && i+1 < len(t) && !unicode.IsSpace(rune(t[i+1])) {
			return "contains an @path, which attaches a file"
		}
	}
	return ""
}

// voiceStart brings the engine up. It reports why it cannot in the chat, and
// starts nothing (no microphone, no download) in that case.
func (a *App) voiceStart(quiet bool) tea.Cmd {
	v := &a.voice
	if v.eng != nil {
		return nil
	}
	s := a.settings.Voice
	v.mode = s.VoiceModeOr()
	if v.model() == "" {
		if !quiet {
			a.addSystem(voiceModelOffer())
		}
		return nil
	}
	src, err := v.source(s.VoiceDevice())
	if err != nil {
		if !quiet {
			a.addSystem("voice: " + sanitize.Line(err.Error(), 300))
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.stop = cancel
	eng := voice.New(ctx, voice.Config{Source: src, Load: v.recognizer, Mode: voice.Mode(v.mode)})
	v.eng, v.muted, v.phase = eng, false, pttIdle
	v.ready = a.voiceComposerReady()
	v.engReady = v.ready || a.voiceCommandReady()
	eng.SetReady(v.engReady)
	eng.SetMode(voice.Mode(v.mode))
	eng.SetEnabled(true)
	a.relayout() // the text gives up room for the icon
	a.refreshFooter()
	return a.watchVoice(eng)
}

// voiceStop turns voice off: it closes the microphone and drops everything
// waiting. Safe to call when voice is not running.
func (a *App) voiceStop() {
	v := &a.voice
	if v.eng == nil {
		return
	}
	a.voiceDetach()
	v.eng.Close()
	if v.stop != nil {
		v.stop()
	}
	v.cmdGen++
	v.eng, v.stop = nil, nil
	v.phase, v.held, v.queue, v.busy, v.ready, v.engReady, v.muted = pttIdle, "", nil, false, false, false, false
	a.relayout()
	a.refreshFooter()
}

func voiceModelOffer() string {
	dest, _ := localinfer.ModelsDir()
	return fmt.Sprintf("voice needs its speech model: %s (%s), from huggingface.co/%s, saved to %s. Run /voice download to fetch it; the file is checked against its SHA-256 before it is used.",
		voice.ModelFile, mib(voice.ModelSize), voice.ModelRepo, dest)
}

func mib(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/1e6) }

// voiceDownload fetches the model after the explicit /voice download.
func (a *App) voiceDownload() tea.Cmd {
	v := &a.voice
	if v.model() != "" {
		if v.model() == "built in" {
			a.addSystem("voice: the speech model is built into this binary, so there is nothing to download")
		} else {
			a.addSystem("voice: the speech model is already on disk")
		}
		return nil
	}
	if v.downloading {
		a.addSystem("voice: the download is already running")
		return nil
	}
	v.downloading = true
	a.addSystem("voice: downloading " + voice.ModelFile + " (" + mib(voice.ModelSize) + ")")
	return func() tea.Msg {
		path, err := voice.Ensure(context.Background(), &http.Client{}, "", func(voice.Offer) bool { return true }, nil)
		return voiceModelMsg{path: path, err: err}
	}
}

func (a *App) voiceModelDone(m voiceModelMsg) tea.Cmd {
	v := &a.voice
	v.downloading = false
	if m.err != nil {
		v.wantOn = false
		a.addSystem("voice: the model download failed: " + sanitize.Line(m.err.Error(), 240))
		return nil
	}
	a.addSystem("voice: the speech model is ready")
	if v.wantOn {
		v.wantOn = false
		return a.voiceStart(false)
	}
	return nil
}

// voiceIndicator describes what voice is doing for the footer and the
// composer chip. ok is false when nothing should be shown.
func (a *App) voiceIndicator() (label string, on, ok bool) {
	v := &a.voice
	if v.eng == nil {
		if v.downloading {
			return "downloading", false, true
		}
		return "", false, false
	}
	if v.muted {
		return "muted", false, true
	}
	if v.busy {
		return "transcribing", true, true
	}
	switch v.eng.State() {
	case voice.StateLoading:
		return "loading", false, true
	case voice.StatePaused:
		return "paused", false, true
	case voice.StateIdle:
		return "push to talk · " + a.settings.Voice.VoiceKeyOr(), false, true
	case voice.StateListening:
		return "listening", true, true
	case voice.StateHearing:
		return "hearing", true, true
	case voice.StateTranscribing:
		return "transcribing", true, true
	}
	return "", false, false // off, or an unfinished start
}

// voiceFooter is the footer's voice switch.
func (a *App) voiceFooter() (string, bool) {
	label, on, ok := a.voiceIndicator()
	if !ok {
		return "", false
	}
	return "voice: " + label, on
}

// voiceCommand is /voice.
func (a *App) voiceCommand(arg string) tea.Cmd {
	fields := strings.Fields(strings.ToLower(arg))
	sub := ""
	if len(fields) > 0 {
		sub = fields[0]
	}
	switch sub {
	case "", "status":
		a.addSystem(a.voiceStatus())
	case "on", "off":
		on := sub == "on"
		if !a.voiceSave(func(v *config.VoiceSettings) { v.Enabled = voiceBool(on) }) {
			return nil
		}
		cmd := a.voiceApplyEnabled()
		if !on {
			a.addSystem("voice: off")
		}
		return cmd
	case "debug":
		return a.voiceDebugStart()
	case "download":
		return a.voiceDownload()
	case "push", "listen":
		mode := config.VoiceModePushToTalk
		if sub == "listen" {
			mode = config.VoiceModeListen
		}
		if a.voiceSave(func(v *config.VoiceSettings) { v.Mode = mode }) {
			a.voiceApplyMode()
			a.addSystem("voice mode: " + mode)
		}
	case "insert", "submit":
		if a.voiceSave(func(v *config.VoiceSettings) { v.Delivery = sub }) {
			a.addSystem("voice delivery: " + sub)
		}
	case "cleanup":
		if len(fields) < 2 || (fields[1] != "on" && fields[1] != "off") {
			a.addSystem("usage: /voice cleanup on|off")
			return nil
		}
		on := fields[1] == "on"
		if a.voiceSave(func(v *config.VoiceSettings) { v.Cleanup = voiceBool(on) }) {
			a.addSystem("voice cleanup: " + fields[1])
		}
	case "wake", "commands":
		if len(fields) < 2 || (fields[1] != "on" && fields[1] != "off") {
			a.addSystem("usage: /voice " + sub + " on|off")
			return nil
		}
		on := fields[1] == "on"
		if a.voiceSave(func(v *config.VoiceSettings) {
			if sub == "wake" {
				v.WakeWord = voiceBool(on)
				if on {
					// Only listen mode hears the wake word; the notice below
					// says so rather than leave a setting that cannot work.
					v.Mode = config.VoiceModeListen
				}
				return
			}
			v.Commands = voiceBool(on)
		}) {
			a.voiceApplyMode()
			msg := "voice " + sub + ": " + fields[1]
			if sub == "wake" && on {
				msg += " (mode listen: the wake word needs the microphone open)"
			}
			a.addSystem(msg)
		}
	default:
		a.addSystem("usage: /voice [status|debug|on|off|download|push|listen|insert|submit|cleanup on|off|wake on|off|commands on|off]")
	}
	return nil
}

// voiceSave writes one voice setting to the global settings file and reloads.
func (a *App) voiceSave(write func(*config.VoiceSettings)) bool {
	if err := a.mutateGlobalSetting(func(s *config.Settings) { write(voiceSettings(s)) }); err != nil {
		a.addSystem("voice: could not save the setting: " + sanitize.Line(err.Error(), 200))
		return false
	}
	a.refreshFooter()
	return true
}

// voiceApplyEnabled makes the running engine match voice.enabled: it starts
// voice (or offers the model download) when on, and stops it when off.
func (a *App) voiceApplyEnabled() tea.Cmd {
	v := &a.voice
	if a.voiceWanted() {
		if v.eng != nil {
			return nil
		}
		if v.model() == "" {
			v.wantOn = true
			a.addSystem(voiceModelOffer())
			return nil
		}
		return a.voiceStart(false)
	}
	v.wantOn = false
	a.voiceStop()
	return nil
}

// voiceApplyMode makes the running engine match voice.mode.
func (a *App) voiceApplyMode() {
	v := &a.voice
	mode := a.settings.Voice.VoiceModeOr()
	if v.eng == nil || mode == v.mode {
		return
	}
	v.mode, v.muted, v.phase = mode, false, pttIdle
	v.seq++
	v.eng.SetMode(voice.Mode(mode))
	v.eng.SetEnabled(true)
	a.refreshFooter()
}

// voiceAfterSetting applies a voice.* row edited in /settings.
func (a *App) voiceAfterSetting(key string) tea.Cmd {
	if strings.HasPrefix(key, "tts.") {
		return a.ttsAfterSetting(key)
	}
	switch key {
	case "voice.enabled":
		return a.voiceApplyEnabled()
	case "voice.mode", "voice.wake_word":
		a.voiceApplyMode()
	}
	return nil
}

func voiceSettings(s *config.Settings) *config.VoiceSettings {
	if s.Voice == nil {
		s.Voice = &config.VoiceSettings{}
	}
	return s.Voice
}

func voiceBool(b bool) *bool { return &b }

// voiceStatus is the /voice status text: facts only.
func (a *App) voiceStatus() string {
	v := &a.voice
	s := a.settings.Voice
	state := "off"
	if label, _, ok := a.voiceIndicator(); ok {
		state = label
	}
	// Two lines: a note longer than that is collapsed in the transcript, and
	// the capture helper and the model are what the user runs this to see.
	capture := "ready"
	if src, err := v.source(s.VoiceDevice()); err != nil {
		capture = sanitize.Line(err.Error(), 300)
	} else if n, ok := src.(interface{ Name() string }); ok {
		capture = n.Name()
	}
	var model string
	switch p := v.model(); p {
	case "":
		model = fmt.Sprintf("not downloaded (%s), run /voice download", mib(voice.ModelSize))
	case "built in":
		model = "built in"
	default:
		model = voice.ModelFile + " on disk"
	}
	return fmt.Sprintf("voice: %s · mode %s · delivery %s · cleanup %s · key %s · wake %s · commands %s\ncapture: %s · model: %s",
		state, s.VoiceModeOr(), s.VoiceDeliveryOr(), onOffLabel(s.VoiceCleanupEnabled()), s.VoiceKeyOr(),
		onOffLabel(s.VoiceWakeWordEnabled()), onOffLabel(s.VoiceCommandsEnabled()), capture, model)
}

// voiceToggle flips a voice.* toggle row in the global settings. Voice is a
// per-user preference, so /settings never writes it to a project file.
func (a *App) voiceToggle(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		v := voiceSettings(s)
		switch key {
		case "voice.enabled":
			v.Enabled = voiceBool(!v.VoiceEnabledOr(voice.Embedded()))
		case "voice.cleanup":
			v.Cleanup = voiceBool(!v.VoiceCleanupEnabled())
		case "voice.log":
			v.Log = voiceBool(!v.VoiceLogEnabled())
		case "voice.commands":
			v.Commands = voiceBool(!v.VoiceCommandsEnabled())
		case "voice.wake_word":
			on := !v.VoiceWakeWordEnabled()
			v.WakeWord = voiceBool(on)
			if on {
				// Only listen mode hears the wake word (config.ValidateVoice).
				v.Mode = config.VoiceModeListen
			}
		}
	})
}

// voiceChoose steps a voice.* choose row to its next option.
func (a *App) voiceChoose(key string, opts []string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		v := voiceSettings(s)
		switch key {
		case "voice.mode":
			v.Mode = opts[(indexOfString(opts, v.VoiceModeOr())+1)%len(opts)]
		case "voice.delivery":
			v.Delivery = opts[(indexOfString(opts, v.VoiceDeliveryOr())+1)%len(opts)]
		case "voice.key":
			v.Key = opts[(indexOfString(opts, v.VoiceKeyOr())+1)%len(opts)]
		}
	})
}

// voiceUnset returns a voice.* row to its default.
func (a *App) voiceUnset(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		if s.Voice == nil {
			return
		}
		switch key {
		case "voice.enabled":
			s.Voice.Enabled = nil
		case "voice.mode":
			s.Voice.Mode = ""
		case "voice.delivery":
			s.Voice.Delivery = ""
		case "voice.key":
			s.Voice.Key = ""
		case "voice.cleanup":
			s.Voice.Cleanup = nil
		case "voice.log":
			s.Voice.Log = nil
		case "voice.commands":
			s.Voice.Commands = nil
		case "voice.wake_word":
			s.Voice.WakeWord = nil
		}
	})
}

// voiceOffHint answers the voice key while voice is off, once a session, so
// the key never does nothing without saying why. A terminal that keeps the key
// for itself (some use f11 for fullscreen) never sends it, and voice.key names
// another.
func (a *App) voiceOffHint() tea.Cmd {
	v := &a.voice
	if v.hinted {
		return nil
	}
	v.hinted = true
	key := a.settings.Voice.VoiceKeyOr()
	switch {
	case v.downloading:
		a.addSystem("voice: the speech model is still downloading; voice turns on when it finishes")
	case v.model() == "":
		a.addSystem("voice is off, and the speech model is not downloaded yet: /voice on shows what it needs, /voice download fetches it, then hold " + key + " to dictate")
	case a.voiceWanted():
		// Voice is meant to be running (the model is built in) but did not
		// start: say why instead of leaving the key silent.
		if _, err := v.source(a.settings.Voice.VoiceDevice()); err != nil {
			a.addSystem("voice could not start: " + sanitize.Line(err.Error(), 300))
		} else {
			a.addSystem("voice is not running: /voice on starts it, /voice debug checks the microphone")
		}
	default:
		a.addSystem("voice is off: /voice on turns it on, then hold " + key + " to dictate. If " + key + " does nothing your terminal may keep it; set voice.key to ctrl+space (in /settings or settings.json)")
	}
	return nil
}

// voiceWanted reports whether voice should be running: what voice.enabled
// says, or on when the speech model is built into this binary and the setting
// is unset.
func (a *App) voiceWanted() bool {
	return a.settings.Voice.VoiceEnabledOr(a.voice.model() == "built in")
}

// dictation is one inserted stretch of dictated text and what the model
// recognised behind it.
type dictation struct{ text, raw string }

const dictatedMax = 16

func (v *voiceState) noteDictated(raw, text string) {
	if text == "" {
		return
	}
	if len(v.dictated) >= dictatedMax {
		v.dictated = v.dictated[1:]
	}
	v.dictated = append(v.dictated, dictation{text: text, raw: raw})
}

// tagDictated marks a prompt about to be echoed as dictated when it still
// holds text that was dictated since the last send, and keeps what was
// recognised so ctrl+o can show it. A prompt you typed is left alone, and the
// record is cleared either way.
func (a *App) tagDictated(m *components.Message) {
	v := &a.voice
	var raws []string
	for _, d := range v.dictated {
		if strings.Contains(m.Content, d.text) {
			raws = append(raws, d.raw)
		}
	}
	v.dictated = nil
	if len(raws) > 0 {
		m.Voice, m.VoiceRaw = true, strings.Join(raws, " ")
	}
}

// voiceEnabledNow is voice.enabled as the user experiences it: the setting,
// or on when the speech model is built into this binary.
func voiceEnabledNow(s config.Settings) bool {
	return s.Voice.VoiceEnabledOr(voice.Embedded())
}

// syncVoice runs after every message: it tells the engine whether the
// composer can take text, delivers dictation that was waiting, and starts the
// animation timer when something on the composer moves.
func (a *App) syncVoice() tea.Cmd {
	cmd := a.syncVoiceReady()
	v := &a.voice
	if v.eng != nil && !v.animating && a.voiceAnimating() {
		v.animating = true
		return tea.Batch(cmd, voiceAnimTick())
	}
	return cmd
}
