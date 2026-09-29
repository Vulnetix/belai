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
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voice"
	"github.com/vulnetix/belai/internal/voice/asr"
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

	mode  string // the engine's mode; delivery, cleanup and key are read live
	muted bool
	ready bool // the readiness last told to the engine

	phase  pttPhase
	seq    int
	held   string   // dictation waiting for a ready composer
	queue  []string // raw transcripts waiting for cleanup
	busy   bool     // one cleanup call in flight
	wantOn bool     // /voice on was asked while the model was missing

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
	return voice.ModelPath()
}

func (v *voiceState) recognizer(ctx context.Context) (voice.Recognizer, error) {
	if v.load != nil {
		return v.load(ctx)
	}
	path := voice.ModelPath()
	if path == "" {
		return nil, fmt.Errorf("the speech model is not downloaded")
	}
	if err := voice.Verify(path); err != nil {
		return nil, err
	}
	return asr.Load(path)
}

// voiceInit is called from Init. It brings voice up only for a real run
// whose settings turn it on.
func (a *App) voiceInit() tea.Cmd {
	if !a.voice.autostart || !a.settings.Voice.VoiceEnabled() {
		return nil
	}
	return a.voiceStart()
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
func (a *App) syncVoice() tea.Cmd {
	v := &a.voice
	if v.eng == nil {
		return nil
	}
	ready := a.voiceComposerReady()
	if ready == v.ready {
		return nil
	}
	v.ready = ready
	v.eng.SetReady(ready)
	if !ready {
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
	v := &a.voice
	switch m := msg.(type) {
	case voiceEventMsg:
		if m.eng != v.eng {
			return nil, true // an engine that was turned off
		}
		cmd := a.watchVoice(m.eng)
		switch m.ev.Kind {
		case voice.EventState:
			a.refreshFooter()
		case voice.EventError:
			a.addSystem("voice: " + sanitize.Line(m.ev.Err.Error(), 240))
			a.refreshFooter()
		case voice.EventTranscript:
			cmd = tea.Batch(cmd, a.voiceTranscript(m.ev.Text))
		}
		return cmd, true
	case voiceCleanedMsg:
		return a.voiceCleaned(m), true
	case voiceHoldMsg:
		a.voiceHold(m.seq)
		return nil, true
	case voiceModelMsg:
		return a.voiceModelDone(m), true
	case tea.KeyMsg:
		if v.eng != nil && a.view == viewChat && voiceKeyMatches(m.String(), a.settings.Voice.VoiceKeyOr()) {
			return a.voiceKey(), true
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
	v := &a.voice
	if len(v.queue) >= voiceQueueMax {
		v.queue = v.queue[1:]
	}
	v.queue = append(v.queue, raw)
	return a.voiceNext()
}

// voiceNext starts the cleanup of the next queued transcript, one at a time so
// phrases keep their order.
func (a *App) voiceNext() tea.Cmd {
	v := &a.voice
	if v.busy || len(v.queue) == 0 {
		return nil
	}
	raw := v.queue[0]
	v.queue = v.queue[1:]
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
	return tea.Batch(a.voiceDeliver(m.text), a.voiceNext())
}

// voiceDeliver puts text in the composer if it is ready, else holds it.
func (a *App) voiceDeliver(text string) tea.Cmd {
	text = strings.TrimSpace(sanitize.Text(text))
	if text == "" {
		return nil
	}
	v := &a.voice
	if !v.ready {
		v.held = strings.TrimSpace(v.held + " " + text)
		if len(v.held) > voiceHeldMax {
			v.held = v.held[len(v.held)-voiceHeldMax:]
		}
		return nil
	}
	return a.voiceInsert(text)
}

func (a *App) voiceDeliverHeld() tea.Cmd {
	v := &a.voice
	text := v.held
	v.held = ""
	if text == "" {
		return nil
	}
	return a.voiceInsert(text)
}

// voiceInsert places text at the cursor. With submit delivery it then sends
// the composer through the same path as pressing Enter, unless what would be
// sent could run something: a line starting with / or ! is a local command, and
// an @path attaches a file, and dictation must never reach either by itself.
func (a *App) voiceInsert(text string) tea.Cmd {
	val := []rune(a.editor.Value())
	off := a.editor.CursorOffset()
	if off > len(val) {
		off = len(val)
	}
	if off > 0 && !unicode.IsSpace(val[off-1]) {
		text = " " + text
	}
	a.editor.ReplaceRange(off, off, text)
	a.refreshAutocomplete()
	a.relayout()
	if a.settings.Voice.VoiceDeliveryOr() != config.VoiceDeliverySubmit {
		return nil
	}
	if reason := voiceSubmitBlock(a.editor.Value()); reason != "" {
		a.addSystem("voice: inserted but not sent, because it " + reason + "; press enter to send it")
		return nil
	}
	return a.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter})
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
func (a *App) voiceStart() tea.Cmd {
	v := &a.voice
	if v.eng != nil {
		return nil
	}
	s := a.settings.Voice
	v.mode = s.VoiceModeOr()
	if v.model() == "" {
		a.addSystem(voiceModelOffer())
		return nil
	}
	src, err := v.source(s.VoiceDevice())
	if err != nil {
		a.addSystem("voice: " + sanitize.Line(err.Error(), 300))
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.stop = cancel
	eng := voice.New(ctx, voice.Config{Source: src, Load: v.recognizer, Mode: voice.Mode(v.mode)})
	v.eng, v.muted, v.phase = eng, false, pttIdle
	v.ready = a.voiceComposerReady()
	eng.SetReady(v.ready)
	eng.SetMode(voice.Mode(v.mode))
	eng.SetEnabled(true)
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
	v.eng.Close()
	if v.stop != nil {
		v.stop()
	}
	v.eng, v.stop = nil, nil
	v.phase, v.held, v.queue, v.busy, v.ready, v.muted = pttIdle, "", nil, false, false, false
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
		a.addSystem("voice: the speech model is already on disk")
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
		return a.voiceStart()
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

// voiceTitleChip is the composer title's chip, or "".
func (a *App) voiceTitleChip() string {
	label, on, ok := a.voiceIndicator()
	if !ok {
		return ""
	}
	glyph, color := "○ ", components.ColorTealSoft
	if on {
		glyph, color = "● ", components.ColorTeal
	}
	return components.Chip(glyph+label, color)
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
	default:
		a.addSystem("usage: /voice [status|on|off|download|push|listen|insert|submit|cleanup on|off]")
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
	if a.settings.Voice.VoiceEnabled() {
		if v.eng != nil {
			return nil
		}
		if v.model() == "" {
			v.wantOn = true
			a.addSystem(voiceModelOffer())
			return nil
		}
		return a.voiceStart()
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
	switch key {
	case "voice.enabled":
		return a.voiceApplyEnabled()
	case "voice.mode":
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
	var b strings.Builder
	state := "off"
	if label, _, ok := a.voiceIndicator(); ok {
		state = label
	}
	fmt.Fprintf(&b, "voice: %s\n", state)
	fmt.Fprintf(&b, "  mode %s, delivery %s, cleanup %s, key %s\n", s.VoiceModeOr(), s.VoiceDeliveryOr(), onOffLabel(s.VoiceCleanupEnabled()), s.VoiceKeyOr())
	if src, err := v.source(s.VoiceDevice()); err != nil {
		fmt.Fprintf(&b, "  capture: %s\n", sanitize.Line(err.Error(), 300))
	} else if n, ok := src.(interface{ Name() string }); ok {
		fmt.Fprintf(&b, "  capture: %s\n", n.Name())
	} else {
		b.WriteString("  capture: ready\n")
	}
	if p := v.model(); p != "" {
		fmt.Fprintf(&b, "  model: %s on disk", voice.ModelFile)
	} else {
		fmt.Fprintf(&b, "  model: not downloaded (%s); run /voice download", mib(voice.ModelSize))
	}
	return b.String()
}

// voiceToggle flips a voice.* toggle row in the global settings. Voice is a
// per-user preference, so /settings never writes it to a project file.
func (a *App) voiceToggle(key string) error {
	return a.mutateGlobalSetting(func(s *config.Settings) {
		v := voiceSettings(s)
		switch key {
		case "voice.enabled":
			v.Enabled = voiceBool(!v.VoiceEnabled())
		case "voice.cleanup":
			v.Cleanup = voiceBool(!v.VoiceCleanupEnabled())
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
		case "voice.cleanup":
			s.Voice.Cleanup = nil
		}
	})
}
