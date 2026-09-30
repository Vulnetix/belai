package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
	"github.com/vulnetix/belai/internal/voice"
)

// tuiMic is a microphone the test never feeds: the TUI tests are about when
// the engine is asked to listen, not about audio.
type tuiMic struct {
	starts atomic.Int32
	stops  atomic.Int32
}

func (m *tuiMic) Start(ctx context.Context) (voice.Stream, error) {
	m.starts.Add(1)
	ch := make(chan []int16, 8)
	go func() {
		<-ctx.Done()
		m.stops.Add(1)
		close(ch)
	}()
	return voice.Stream{C: ch, Err: func() error { return nil }}, nil
}

type tuiRec struct {
	mu   sync.Mutex
	text string
}

func (r *tuiRec) Transcribe(context.Context, []float32) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text, nil
}

// voiceApp builds an App with voice seams that touch no microphone, no model
// file and no network, and with an isolated settings home.
func voiceApp(t *testing.T, v config.VoiceSettings) (*App, *tuiMic) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	a := New(Options{Workdir: t.TempDir()})
	a.classifier = nil
	mic := &tuiMic{}
	a.settings.Voice = &v
	a.voice.newSource = func(string) (voice.Source, error) { return mic, nil }
	a.voice.load = func(context.Context) (voice.Recognizer, error) { return &tuiRec{text: "x"}, nil }
	a.voice.modelPath = func() string { return "/models/ggml-tiny.en-q5_1.bin" }
	t.Cleanup(a.voiceStop)
	return a, mic
}

func on() *bool { b := true; return &b }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func started(t *testing.T, v config.VoiceSettings) (*App, *tuiMic) {
	t.Helper()
	a, mic := voiceApp(t, v)
	if cmd := a.voiceStart(false); cmd == nil {
		t.Fatalf("voiceStart returned no watcher: %v", a.messages)
	}
	a.syncVoice()
	waitFor(t, "model loaded", func() bool { s := a.voice.eng.State(); return s != voice.StateLoading && s != voice.StateOff })
	return a, mic
}

func lastSystem(a *App) string {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == "system" {
			return a.messages[i].Content
		}
	}
	return ""
}

func TestNewAndInitNeverStartVoice(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{Enabled: on()})
	a.Init()
	if a.voice.eng != nil || mic.starts.Load() != 0 {
		t.Fatal("New and Init started voice without a real run")
	}
	if cmd := a.voiceInit(); cmd != nil {
		t.Fatal("voiceInit started voice with autostart off")
	}
	a.voice.autostart = true
	if cmd := a.voiceInit(); cmd == nil || a.voice.eng == nil {
		t.Fatal("a real run with voice.enabled did not start voice")
	}
	a2, _ := voiceApp(t, config.VoiceSettings{})
	a2.voice.autostart = true
	if cmd := a2.voiceInit(); cmd != nil || a2.voice.eng != nil {
		t.Fatal("voice started although it is not enabled")
	}
}

func TestComposerReadyPredicate(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	if !a.voiceComposerReady() {
		t.Fatal("a plain chat composer is not ready")
	}
	cases := map[string]func(){
		"another screen":     func() { a.view = viewSettings },
		"action bar":         func() { a.promptAction = true },
		"save file":          func() { a.saveFileMode = true },
		"save prompt":        func() { a.savePromptMode = true },
		"history":            func() { a.historyActive = true },
		"forge confirm":      func() { a.forgeConfirm.kind = forgeConfirmRemove },
		"runs panel focus":   func() { a.runsFocus = true },
		"directory picker":   func() { a.dirPickState.open = true },
		"agent picker":       func() { a.agentPickerOpen = true },
		"slash popup":        func() { a.autocomplete = []string{"/help"} },
		"classifying prompt": func() { a.preSend = true },
		"pending attachment": func() { a.pendingInput = "hello" },
		"secret field":       func() { a.editor.Masked = true },
	}
	for name, set := range cases {
		a2, _ := voiceApp(t, config.VoiceSettings{})
		a = a2
		set()
		if a.voiceComposerReady() {
			t.Errorf("%s: the composer counted as ready", name)
		}
	}
}

func TestATurnInFlightKeepsTheComposerReady(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.cancel = func() {}
	if !a.working() {
		t.Fatal("test setup: no turn in flight")
	}
	if !a.voiceComposerReady() {
		t.Fatal("typing steers a running turn, so dictation must stay allowed")
	}
}

func TestVoiceStartExplainsWhyItCannot(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{Enabled: on()})
	a.voice.modelPath = func() string { return "" }
	if cmd := a.voiceStart(false); cmd != nil || a.voice.eng != nil {
		t.Fatal("started without the model")
	}
	msg := lastSystem(a)
	for _, want := range []string{"ggml-tiny.en-q5_1.bin", "32.2 MB", "/voice download", "SHA-256"} {
		if !strings.Contains(msg, want) {
			t.Errorf("model offer %q lacks %q", msg, want)
		}
	}
	a.voice.modelPath = func() string { return "/m" }
	a.voice.newSource = func(string) (voice.Source, error) { return nil, voice.ErrNoCapture }
	if cmd := a.voiceStart(false); cmd != nil || a.voice.eng != nil {
		t.Fatal("started without a capture helper")
	}
	if !strings.Contains(lastSystem(a), "install one of") {
		t.Fatalf("message = %q", lastSystem(a))
	}
	if mic.starts.Load() != 0 {
		t.Fatal("the microphone opened")
	}
}

func TestPauseWhenTheComposerIsUnavailable(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	waitFor(t, "microphone open", func() bool { return mic.starts.Load() == 1 })
	if label, _, _ := a.voiceIndicator(); label != "listening" {
		t.Fatalf("indicator = %q", label)
	}
	a.view = viewSettings
	a.syncVoice()
	waitFor(t, "paused", func() bool { return a.voice.eng.State() == voice.StatePaused })
	waitFor(t, "microphone closed", func() bool { return mic.stops.Load() == 1 })
	if label, on, _ := a.voiceIndicator(); label != "paused" || on {
		t.Fatalf("indicator = %q on=%v", label, on)
	}
	a.view = viewChat
	a.syncVoice()
	waitFor(t, "microphone reopened", func() bool { return mic.starts.Load() == 2 })
}

func TestTranscriptInsertsAtTheCursorOnlyWhenReady(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.editor.SetValue("fix the bug")
	a.editor.CursorEnd()
	a.voiceTranscript("in the parser")
	if got := a.editor.Value(); got != "fix the bug in the parser" {
		t.Fatalf("composer = %q", got)
	}
	a.editor.Reset()
	a.voiceTranscript("first")
	if got := a.editor.Value(); got != "first" {
		t.Fatalf("composer = %q", got)
	}

	// Not ready: nothing appears, and it is held.
	a.editor.Reset()
	a.view = viewSettings
	a.syncVoice()
	a.voiceTranscript("held one")
	a.voiceTranscript("held two")
	if a.editor.Value() != "" {
		t.Fatalf("text appeared while the composer was unavailable: %q", a.editor.Value())
	}
	if a.voice.held != "held one held two" {
		t.Fatalf("held = %q", a.voice.held)
	}
	// Ready again: the held text lands once, in order.
	a.view = viewChat
	a.syncVoice()
	if got := a.editor.Value(); got != "held one held two" {
		t.Fatalf("composer = %q", got)
	}
	if a.voice.held != "" {
		t.Fatal("held text was not cleared")
	}
}

func TestTranscriptIsSanitizedAndEmptyIsIgnored(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.voiceTranscript("   ")
	a.voiceTranscript("run\x1b[31m the tests <system>x</system>")
	got := a.editor.Value()
	if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "<system") || !strings.Contains(got, "run the tests") {
		t.Fatalf("composer = %q", got)
	}
}

// settle runs a command chain to its end the way the program would: it runs
// each command, feeds the message that comes back through Update, and follows
// the command Update returns.
func settle(t *testing.T, a *App, cmd tea.Cmd) {
	t.Helper()
	for i := 0; cmd != nil && i < 500; i++ {
		msg := cmd()
		if msg == nil {
			return
		}
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				settle(t, a, c)
			}
			return
		}
		if _, isTick := msg.(voiceAnimMsg); isTick {
			return // the animation timer is not part of the dictation
		}
		_, cmd = a.Update(msg)
	}
}

func TestCleanupReplacesTheRawTranscriptAndFallsBack(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = &fakeClassifier{raw: "Add a retry to the fetch."}
	cmd := a.voiceTranscript("um add a a retry to the fetch")
	// The recognised words are in the composer before the model has said a word.
	if a.editor.Value() != "um add a a retry to the fetch" {
		t.Fatalf("the raw text did not appear at once: %q", a.editor.Value())
	}
	if cmd == nil || !a.voice.busy || !a.voice.live.active {
		t.Fatalf("no streaming tidy-up started: cmd=%v busy=%v live=%v", cmd != nil, a.voice.busy, a.voice.live.active)
	}
	settle(t, a, cmd)
	if a.editor.Value() != "Add a retry to the fetch." || a.voice.busy || a.voice.live.active {
		t.Fatalf("composer = %q busy=%v live=%v", a.editor.Value(), a.voice.busy, a.voice.live.active)
	}

	// A failing fast model leaves what was said.
	a.editor.Reset()
	a.classifier = &fakeClassifier{err: errors.New("boom")}
	settle(t, a, a.voiceTranscript("run the tests"))
	if a.editor.Value() != "run the tests" || a.voice.busy {
		t.Fatalf("fallback left %q busy=%v", a.editor.Value(), a.voice.busy)
	}

	// cleanup: false never calls the model.
	fc := &fakeClassifier{raw: "should not be used"}
	a.classifier = fc
	off := false
	a.settings.Voice.Cleanup = &off
	a.editor.Reset()
	a.voiceTranscript("as spoken")
	if a.editor.Value() != "as spoken" || len(fc.payloads) != 0 {
		t.Fatalf("composer = %q, calls = %d", a.editor.Value(), len(fc.payloads))
	}
}

func TestTranscriptsKeepTheirOrderThroughCleanup(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = &fakeClassifier{raw: "ok"}
	first := a.voiceTranscript("one")
	if second := a.voiceTranscript("two"); second != nil {
		t.Fatal("a second phrase started while one was being tidied")
	}
	if len(a.voice.queue) != 1 {
		t.Fatalf("queue = %v", a.voice.queue)
	}
	settle(t, a, first)
	// Both phrases came through, in order, and nothing is left waiting.
	if len(a.voice.queue) != 0 || a.voice.busy {
		t.Fatalf("queue = %v busy = %v", a.voice.queue, a.voice.busy)
	}
	if got := a.editor.Value(); got != "ok ok" {
		t.Fatalf("composer = %q, want both phrases tidied in order", got)
	}
}
func TestSubmitDeliveryBlocksCommandsAndAttachments(t *testing.T) {
	cases := map[string]string{
		"/help":            "starts with /",
		"  !rm -rf x":      "starts with !",
		"look at @main.go": "@path",
		"fix the bug":      "",
		"email me at x":    "",
		"a@ b":             "",
	}
	for in, want := range cases {
		got := voiceSubmitBlock(in)
		if (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("voiceSubmitBlock(%q) = %q, want %q", in, got, want)
		}
	}
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Delivery: config.VoiceDeliverySubmit})
	a.voiceTranscript("/help")
	if a.editor.Value() != "/help" {
		t.Fatalf("a dictated slash command was not left in the composer: %q", a.editor.Value())
	}
	if !strings.Contains(lastSystem(a), "not sent") {
		t.Fatalf("no explanation: %q", lastSystem(a))
	}
}

func TestSubmitDeliverySendsThroughEnter(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Delivery: config.VoiceDeliverySubmit})
	a.voiceTranscript("say hello")
	// With no agent engaged, Enter opens the agent picker and defers the
	// send: that is the ordinary Enter path, so dictation took it too.
	if !a.agentPickerOpen || !a.agentPickerSubmit {
		t.Fatalf("submit delivery did not go through Enter: picker=%v submit=%v", a.agentPickerOpen, a.agentPickerSubmit)
	}
	// Insert delivery never presses Enter.
	b, _ := started(t, config.VoiceSettings{Enabled: on()})
	b.voiceTranscript("say hello")
	if b.agentPickerOpen {
		t.Fatal("insert delivery pressed Enter")
	}
}

func TestHoldToTalkFollowsKeyRepeat(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	press := func() { a.Update(tea.KeyMsg{Type: tea.KeyF11}) }

	press()
	if a.voice.phase != pttPressing {
		t.Fatalf("phase = %v after the first press", a.voice.phase)
	}
	waitFor(t, "microphone opens", func() bool { return mic.starts.Load() == 1 })
	press() // key repeat: this is a hold
	if a.voice.phase != pttHeld {
		t.Fatalf("phase = %v after a repeat", a.voice.phase)
	}
	// A stale tick from the first press must not end the hold.
	a.Update(voiceHoldMsg{seq: a.voice.seq - 1})
	if a.voice.phase != pttHeld {
		t.Fatal("a stale hold tick ended the recording")
	}
	press()
	a.Update(voiceHoldMsg{seq: a.voice.seq}) // repeats stopped: released
	if a.voice.phase != pttIdle {
		t.Fatalf("phase = %v after the repeats stopped", a.voice.phase)
	}
}

func TestTapToggles(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	waitFor(t, "microphone opens", func() bool { return mic.starts.Load() == 1 })
	a.Update(voiceHoldMsg{seq: a.voice.seq}) // no repeat arrived: a tap
	if a.voice.phase != pttLatched {
		t.Fatalf("phase = %v, want latched", a.voice.phase)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF11}) // the second tap stops it
	if a.voice.phase != pttIdle {
		t.Fatalf("phase = %v after the second tap", a.voice.phase)
	}
}

func TestVoiceKeyDoesNothingWhenNotReady(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	a.view = viewSettings
	a.syncVoice()
	waitFor(t, "paused", func() bool { return a.voice.eng.State() == voice.StatePaused })
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	time.Sleep(20 * time.Millisecond)
	if mic.starts.Load() != 0 || a.voice.phase != pttIdle {
		t.Fatal("the key recorded while the composer was unavailable")
	}
}

func TestLosingReadinessCancelsAHold(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	if a.voice.phase == pttIdle {
		t.Fatal("test setup: not recording")
	}
	a.promptAction = true
	a.syncVoice()
	if a.voice.phase != pttIdle {
		t.Fatalf("phase = %v after the composer became unavailable", a.voice.phase)
	}
}

func TestListenModeKeyMutes(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	waitFor(t, "microphone open", func() bool { return mic.starts.Load() == 1 })
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	waitFor(t, "microphone closed", func() bool { return mic.stops.Load() == 1 })
	if label, _ := a.voiceFooter(); label != "voice: muted" {
		t.Fatalf("footer = %q", label)
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	waitFor(t, "microphone reopened", func() bool { return mic.starts.Load() == 2 })
}

func TestConfiguredKeyIsHonoured(t *testing.T) {
	if !voiceKeyMatches("f11", "f11") || voiceKeyMatches("f10", "f11") {
		t.Fatal("plain key match")
	}
	if !voiceKeyMatches("ctrl+@", "ctrl+space") || !voiceKeyMatches("ctrl+space", "ctrl+space") || voiceKeyMatches("f11", "ctrl+space") {
		t.Fatal("ctrl+space match")
	}
	a, mic := started(t, config.VoiceSettings{Enabled: on(), Key: "f13"})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	time.Sleep(20 * time.Millisecond)
	if mic.starts.Load() != 0 {
		t.Fatal("f11 recorded although voice.key is f13")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyF13})
	waitFor(t, "microphone opens on the configured key", func() bool { return mic.starts.Load() == 1 })
}

func TestFooterAndComposerIcon(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if label, _ := a.voiceFooter(); label != "" {
		t.Fatal("voice shows a footer switch while it is off")
	}
	if strings.ContainsAny(ansi.Strip(a.renderComposer()), "◉◎●◌⊘") {
		t.Fatal("the composer shows a voice icon while voice is off")
	}
	a, _ = started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	a.refreshFooter()
	if a.footer.Voice != "voice: listening" || !a.footer.VoiceOn {
		t.Fatalf("footer = %q on=%v", a.footer.Voice, a.footer.VoiceOn)
	}
	if !strings.ContainsAny(ansi.Strip(a.renderComposer()), "◉◎█▒░▀") {
		t.Fatal("the composer does not show the listening icon")
	}
	a.view = viewSettings
	if strings.ContainsAny(ansi.Strip(a.renderComposer()), "◉◎●◌⊘") {
		t.Fatal("the icon is drawn on another view's field")
	}
	a.view = viewChat

	a, _ = started(t, config.VoiceSettings{Enabled: on()})
	if label, on, _ := a.voiceIndicator(); label != "push to talk · f11" || on {
		t.Fatalf("armed indicator = %q on=%v", label, on)
	}
}

func TestVoiceCommand(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.voiceCommand("")
	for _, want := range []string{"voice: off", "mode push_to_talk", "delivery insert", "cleanup on", "key f11", "capture: ", "model: ggml-tiny.en-q5_1.bin on disk"} {
		if !strings.Contains(lastSystem(a), want) {
			t.Errorf("status %q lacks %q", lastSystem(a), want)
		}
	}
	a.voiceCommand("bogus")
	if !strings.Contains(lastSystem(a), "usage: /voice") {
		t.Fatalf("no usage: %q", lastSystem(a))
	}
	a.voiceCommand("cleanup maybe")
	if !strings.Contains(lastSystem(a), "usage: /voice cleanup") {
		t.Fatalf("no cleanup usage: %q", lastSystem(a))
	}

	if cmd := a.voiceCommand("on"); cmd == nil || a.voice.eng == nil {
		t.Fatalf("/voice on did not start voice: %q", lastSystem(a))
	}
	if !a.settings.Voice.VoiceEnabled() {
		t.Fatal("/voice on did not persist the setting")
	}
	a.voiceCommand("listen")
	if a.voice.mode != config.VoiceModeListen || a.settings.Voice.VoiceModeOr() != config.VoiceModeListen {
		t.Fatalf("mode = %q", a.voice.mode)
	}
	a.voiceCommand("submit")
	if a.settings.Voice.VoiceDeliveryOr() != config.VoiceDeliverySubmit {
		t.Fatal("/voice submit did not persist")
	}
	a.voiceCommand("cleanup off")
	if a.settings.Voice.VoiceCleanupEnabled() {
		t.Fatal("/voice cleanup off did not persist")
	}
	a.voiceCommand("push")
	a.voiceCommand("insert")
	a.voiceCommand("cleanup on")
	if a.settings.Voice.VoiceModeOr() != config.VoiceModePushToTalk || a.settings.Voice.VoiceDeliveryOr() != config.VoiceDeliveryInsert || !a.settings.Voice.VoiceCleanupEnabled() {
		t.Fatalf("settings = %+v", a.settings.Voice)
	}
	a.voiceCommand("off")
	if a.voice.eng != nil || a.settings.Voice.VoiceEnabled() {
		t.Fatal("/voice off left voice running or enabled")
	}
	// The settings landed in the global file, never a project file.
	g, err := config.LoadGlobal()
	if err != nil || g.Voice == nil {
		t.Fatalf("global settings = %+v, %v", g.Voice, err)
	}
}

func TestVoiceOnWithoutTheModelOffersItAndWaits(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{})
	a.voice.modelPath = func() string { return "" }
	if cmd := a.voiceCommand("on"); cmd != nil || a.voice.eng != nil || !a.voice.wantOn {
		t.Fatalf("cmd=%v eng=%v wantOn=%v", cmd != nil, a.voice.eng != nil, a.voice.wantOn)
	}
	if !strings.Contains(lastSystem(a), "/voice download") {
		t.Fatalf("no offer: %q", lastSystem(a))
	}
	// The download finished: voice starts because it was asked for.
	a.voice.modelPath = func() string { return "/m" }
	if cmd := a.voiceModelDone(voiceModelMsg{path: "/m"}); cmd == nil || a.voice.eng == nil {
		t.Fatal("voice did not start after the model arrived")
	}
	if mic.starts.Load() != 0 {
		t.Fatal("the microphone opened before it was needed")
	}
}

func TestModelDownloadFailureIsReported(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.voice.downloading, a.voice.wantOn = true, true
	a.voiceModelDone(voiceModelMsg{err: errors.New("hf said no")})
	if a.voice.downloading || a.voice.wantOn || !strings.Contains(lastSystem(a), "hf said no") {
		t.Fatalf("state after failure: %+v, message %q", a.voice, lastSystem(a))
	}
	a.voice.modelPath = func() string { return "/m" }
	a.voiceDownload()
	if !strings.Contains(lastSystem(a), "already on disk") {
		t.Fatalf("message = %q", lastSystem(a))
	}
	a.voice.modelPath = func() string { return "" }
	a.voice.downloading = true
	a.voiceDownload()
	if !strings.Contains(lastSystem(a), "already running") {
		t.Fatalf("message = %q", lastSystem(a))
	}
	if label, _, ok := a.voiceIndicator(); !ok || label != "downloading" {
		t.Fatalf("indicator = %q", label)
	}
}

func TestVoiceErrorsAreShownSanitized(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventError, Err: errors.New("parecord stopped\x1b[31m: no device")}})
	got := lastSystem(a)
	if !strings.HasPrefix(got, "voice: parecord stopped") || strings.ContainsRune(got, 0x1b) {
		t.Fatalf("message = %q", got)
	}
}

func TestEventsFromAStoppedEngineAreIgnored(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	old := a.voice.eng
	a.voiceStop()
	before := len(a.messages)
	cmd, handled := a.handleVoiceMsg(voiceEventMsg{eng: old, ev: voice.Event{Kind: voice.EventTranscript, Text: "late"}})
	if !handled || cmd != nil || a.editor.Value() != "" || len(a.messages) != before {
		t.Fatal("a stopped engine's transcript was delivered")
	}
}

func TestVoiceStopClearsEverything(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	waitFor(t, "microphone open", func() bool { return mic.starts.Load() == 1 })
	a.voice.held, a.voice.queue = "waiting", []string{"queued"}
	a.voiceStop()
	if a.voice.eng != nil || a.voice.held != "" || len(a.voice.queue) != 0 || mic.stops.Load() != 1 {
		t.Fatalf("state after stop: %+v (stops %d)", a.voice, mic.stops.Load())
	}
	a.voiceStop() // idempotent
}

func TestSettingsRowsForVoice(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{})
	keys := map[string]bool{}
	for _, r := range a.settingsRows() {
		keys[r.key] = true
	}
	for _, k := range []string{"voice.enabled", "voice.mode", "voice.delivery", "voice.cleanup"} {
		if !keys[k] {
			t.Errorf("/settings has no %s row", k)
		}
	}
	if err := a.cycleToggle("voice.enabled"); err != nil {
		t.Fatal(err)
	}
	if cmd := a.voiceAfterSetting("voice.enabled"); cmd == nil || a.voice.eng == nil {
		t.Fatal("turning the voice row on did not start voice")
	}
	if err := a.cycleChoice("voice.mode", []string{config.VoiceModePushToTalk, config.VoiceModeListen}); err != nil {
		t.Fatal(err)
	}
	a.voiceAfterSetting("voice.mode")
	if a.voice.mode != config.VoiceModeListen {
		t.Fatalf("mode = %q", a.voice.mode)
	}
	waitFor(t, "microphone open in listen mode", func() bool { return mic.starts.Load() == 1 })
	if err := a.cycleChoice("voice.delivery", []string{config.VoiceDeliveryInsert, config.VoiceDeliverySubmit}); err != nil || a.settings.Voice.VoiceDeliveryOr() != config.VoiceDeliverySubmit {
		t.Fatalf("delivery = %v, %v", a.settings.Voice.VoiceDeliveryOr(), err)
	}
	if err := a.cycleToggle("voice.cleanup"); err != nil || a.settings.Voice.VoiceCleanupEnabled() {
		t.Fatalf("cleanup toggle: %v", err)
	}
	for _, k := range []string{"voice.mode", "voice.delivery", "voice.cleanup", "voice.enabled"} {
		if err := a.unsetSetting(k); err != nil {
			t.Fatal(err)
		}
		a.voiceAfterSetting(k)
	}
	if a.settings.Voice.VoiceEnabled() || a.voice.eng != nil {
		t.Fatal("resetting the rows left voice on")
	}
	if a.voiceAfterSetting("provider") != nil {
		t.Fatal("a non-voice row produced a voice command")
	}
}

// docs/voice.md names every subcommand and every settings row.
func TestVoicePageMatchesTheTUI(t *testing.T) {
	doc := docparity.Read(t, "docs/voice.md")
	for _, sub := range []string{"/voice status", "/voice on", "/voice off", "/voice download", "/voice push", "/voice listen", "/voice insert", "/voice submit", "/voice cleanup on"} {
		if !strings.Contains(doc, sub) {
			t.Errorf("docs/voice.md does not document %s", sub)
		}
	}
	for _, w := range []string{"listening", "hearing", "transcribing", "paused", "push to talk", "loading", "muted", "downloading"} {
		if !strings.Contains(doc, w) {
			t.Errorf("docs/voice.md does not describe the %q indicator", w)
		}
	}
	site := docparity.Read(t, "site/src/components/sections/Qol.astro")
	if !strings.Contains(site, "/voice") {
		t.Error("the site does not mention /voice")
	}
}

func TestVoiceKeyWhileOffExplainsItselfOnce(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{})
	before := len(a.messages)
	cmd, handled := a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if !handled || cmd != nil {
		t.Fatalf("handled=%v cmd=%v: the key must be answered", handled, cmd != nil)
	}
	got := lastSystem(a)
	for _, want := range []string{"voice is off", "/voice on", "hold f11", "voice.key"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q lacks %q", got, want)
		}
	}
	// Key repeat while held must not repeat the message.
	a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if len(a.messages) != before+1 {
		t.Fatalf("%d messages after three presses, want one", len(a.messages)-before)
	}
	if mic.starts.Load() != 0 {
		t.Fatal("the microphone opened while voice was off")
	}
	// Another view's text field keeps its keys.
	b, _ := voiceApp(t, config.VoiceSettings{})
	b.view = viewSettings
	if _, handled := b.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11}); handled {
		t.Fatal("the hint stole a key from another view")
	}
	// The configured key is the one that answers.
	c, _ := voiceApp(t, config.VoiceSettings{Key: "f13"})
	if _, handled := c.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11}); handled {
		t.Fatal("f11 answered although voice.key is f13")
	}
	c.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF13})
	if !strings.Contains(lastSystem(c), "hold f13") {
		t.Fatalf("hint = %q", lastSystem(c))
	}
}

func TestVoiceKeyHintPointsAtTheModelWhenMissing(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.voice.modelPath = func() string { return "" }
	a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if got := lastSystem(a); !strings.Contains(got, "/voice download") || !strings.Contains(got, "not downloaded") {
		t.Fatalf("hint = %q", got)
	}
	b, _ := voiceApp(t, config.VoiceSettings{})
	b.voice.downloading = true
	b.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if !strings.Contains(lastSystem(b), "still downloading") {
		t.Fatalf("hint = %q", lastSystem(b))
	}
}

func TestBuiltInModelIsReportedAndNeverDownloaded(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.voice.modelPath = func() string { return "built in" }
	a.voiceCommand("status")
	if !strings.Contains(lastSystem(a), "model: built in") || strings.Count(lastSystem(a), "\n") != 1 {
		t.Fatalf("status = %q", lastSystem(a))
	}
	if cmd := a.voiceDownload(); cmd != nil {
		t.Fatal("a build with the model built in started a download")
	}
	if !strings.Contains(lastSystem(a), "built into this binary") {
		t.Fatalf("message = %q", lastSystem(a))
	}
	if a.voice.downloading {
		t.Fatal("downloading flag set")
	}
	// /voice on starts without any download offer.
	if cmd := a.voiceCommand("on"); cmd == nil || a.voice.eng == nil {
		t.Fatalf("/voice on did not start with the model built in: %q", lastSystem(a))
	}
}
