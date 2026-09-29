package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/voice"
)

func TestMicVerdict(t *testing.T) {
	cases := []struct {
		elapsed time.Duration
		peak    float64
		samples int64
		want    string
	}{
		{time.Second, -100, 0, "checking the microphone"},
		{time.Second, -100, 48000, "checking the microphone"},
		{4 * time.Second, -100, 0, "No audio data arrived from the capture helper (parecord)"},
		{4 * time.Second, -100, 64000, "Perhaps the microphone is on mute"},
		{4 * time.Second, -70, 64000, "Perhaps the microphone is on mute"},
		{4 * time.Second, -55, 64000, "below the speech level"},
		{4 * time.Second, -12, 64000, "The microphone is working"},
	}
	for _, c := range cases {
		if got := micVerdict(c.elapsed, c.peak, c.samples, "parecord"); !strings.Contains(got, c.want) {
			t.Errorf("micVerdict(%v, %v, %d) = %q, want it to say %q", c.elapsed, c.peak, c.samples, got, c.want)
		}
	}
	if !strings.Contains(micVerdict(5*time.Second, -100, 1, "parecord"), "voice.device") {
		t.Error("the silent verdict does not point at voice.device")
	}
	if !strings.Contains(micVerdict(5*time.Second, -100, 0, "parecord"), "arecord, ffmpeg or sox") {
		t.Error("the no-data verdict does not suggest another helper")
	}
	if helperName("parecord --raw -") != "parecord" || helperName("") != "none" {
		t.Error("helperName")
	}
}

func TestMeterBar(t *testing.T) {
	silent := meterBar(-100, -100, 40)
	if len([]rune(silent)) != 40 || strings.Contains(silent, "█") {
		t.Fatalf("a silent meter = %q", silent)
	}
	if !strings.Contains(silent, "┊") {
		t.Fatal("the speech-level marker is missing")
	}
	loud := meterBar(-10, -3, 40)
	quiet := meterBar(-60, -60, 40)
	if strings.Count(loud, "█") <= strings.Count(quiet, "█") {
		t.Fatalf("a louder level does not fill more: %q vs %q", loud, quiet)
	}
	if full := meterBar(0, 0, 40); strings.Count(full, "█") != 40 {
		t.Fatalf("a full-scale meter = %q", full)
	}
	if over := meterBar(20, 20, 40); len([]rune(over)) != 40 {
		t.Fatal("a level above full scale changed the width")
	}
	held := meterBar(-60, -20, 40)
	if !strings.Contains(held, "▏") {
		t.Fatalf("a held peak above the level shows no marker: %q", held)
	}
}

func TestSparklineAndLastRunes(t *testing.T) {
	s := sparkline([]float64{-100, -40, 0})
	r := []rune(s)
	if len(r) != 3 || r[0] != '▁' || r[2] != '█' || r[1] == r[0] {
		t.Fatalf("sparkline = %q", s)
	}
	if lastRunes("héllo wörld", 5) != "wörld" || lastRunes("abc", 10) != "abc" {
		t.Fatal("lastRunes is not rune-safe")
	}
}

func TestKeyAnalysisReadsAHold(t *testing.T) {
	d := &voiceDebugState{started: time.Now()}
	t0 := time.Now()
	d.noteKey("f11", "f11", t0)
	d.noteKey("f11", "f11", t0.Add(500*time.Millisecond)) // the terminal repeats: a hold
	for i := 2; i < 10; i++ {
		d.noteKey("f11", "f11", t0.Add(time.Duration(500+30*i)*time.Millisecond))
	}
	log := strings.Join(d.log, "\n")
	if !strings.Contains(log, "key f11 press") || !strings.Contains(log, "hold works") {
		t.Fatalf("log = %q", log)
	}
	if strings.Count(log, "repeat after") != 1 {
		t.Fatal("every repeat was logged: only the first tells anything")
	}
	d.resolveKey(t0.Add(600 * time.Millisecond)) // still repeating
	if d.keyResolved {
		t.Fatal("a hold ended while repeats were still arriving")
	}
	d.resolveKey(d.keyLast.Add(300 * time.Millisecond))
	if !d.keyResolved || !strings.Contains(strings.Join(d.log, "\n"), "release inferred") {
		t.Fatalf("release not inferred: %v", d.log)
	}
	before := len(d.log)
	d.resolveKey(d.keyLast.Add(time.Second))
	if len(d.log) != before {
		t.Fatal("a resolved press was resolved twice")
	}
}

func TestKeyAnalysisReadsATap(t *testing.T) {
	d := &voiceDebugState{started: time.Now()}
	t0 := time.Now()
	d.noteKey("ctrl+@", "ctrl+space", t0)
	d.resolveKey(t0.Add(300 * time.Millisecond))
	if d.keyResolved {
		t.Fatal("a tap was decided before the grace period")
	}
	d.resolveKey(t0.Add(800 * time.Millisecond))
	if !d.keyResolved || !strings.Contains(strings.Join(d.log, "\n"), "that was a tap") {
		t.Fatalf("tap not recognised: %v", d.log)
	}
	// A new press after a long gap starts a new analysis.
	d.noteKey("ctrl+@", "ctrl+space", t0.Add(3*time.Second))
	if d.keyCount != 1 || d.keyResolved {
		t.Fatal("a later press did not restart the analysis")
	}
}

func TestKeyAnalysisLogsOtherKeysWithoutAnalysingThem(t *testing.T) {
	d := &voiceDebugState{started: time.Now()}
	d.noteKey("f9", "f11", time.Now())
	if len(d.log) != 1 || !strings.Contains(d.log[0], "not the voice key f11") || d.keyCount != 0 {
		t.Fatalf("log = %v count = %d", d.log, d.keyCount)
	}
}

func TestLogIsCappedAndStamped(t *testing.T) {
	d := &voiceDebugState{started: time.Now().Add(-65*time.Second - 250*time.Millisecond)}
	d.logf("first")
	if !strings.HasPrefix(d.log[0], "01:05.") {
		t.Fatalf("stamp = %q", d.log[0])
	}
	for i := 0; i < dbgLogMax+50; i++ {
		d.logf("line %d", i)
	}
	if len(d.log) != dbgLogMax || !strings.HasSuffix(d.log[len(d.log)-1], "line 249") {
		t.Fatalf("%d lines, last %q", len(d.log), d.log[len(d.log)-1])
	}
}

func startDebug(t *testing.T, v config.VoiceSettings) (*App, *tuiMic) {
	t.Helper()
	a, mic := voiceApp(t, v)
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	t.Cleanup(func() { a.vdebug.resume = false; a.voiceDebugStop() })
	if cmd := a.voiceDebugStart(); cmd == nil {
		t.Fatal("voiceDebugStart returned nothing")
	}
	return a, mic
}

func TestDebugOpensItsOwnListeningEngine(t *testing.T) {
	a, mic := startDebug(t, config.VoiceSettings{})
	if a.view != viewVoiceDebug || a.vdebug.eng == nil {
		t.Fatalf("view = %v, engine = %v", a.view, a.vdebug.eng)
	}
	waitFor(t, "the debug microphone", func() bool { return mic.starts.Load() == 1 })
	if a.voice.eng != nil {
		t.Fatal("the normal engine started as well")
	}
	// The screen is not the chat, so the composer is not ready and nothing
	// would be typed: the debug engine listens regardless.
	if a.voiceComposerReady() {
		t.Fatal("the debug view counted as a ready composer")
	}
}

func TestDebugPausesAndRestoresTheNormalEngine(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	waitFor(t, "normal engine idle", func() bool { return a.voice.eng.State() == voice.StateIdle })
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	a.voiceDebugStart()
	if a.voice.eng != nil || !a.vdebug.resume {
		t.Fatal("the normal engine kept running under the debug screen")
	}
	defer func() { a.vdebug.resume = false; a.voiceDebugStop() }()
	waitFor(t, "the debug microphone", func() bool { return mic.starts.Load() >= 1 })

	a.handleVoiceDebugKey(tea.KeyMsg{Type: tea.KeyEsc})
	if a.view != viewChat {
		t.Fatalf("esc left the view at %v", a.view)
	}
	if a.voice.eng == nil {
		t.Fatal("the normal engine was not brought back")
	}
	if a.vdebug.eng != nil {
		t.Fatal("the debug engine kept running")
	}
	a.voiceStop()
}

func TestDebugDoesNotResurrectVoiceThatWasOff(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{Enabled: func() *bool { b := false; return &b }()})
	a.handleVoiceDebugKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if a.voice.eng != nil {
		t.Fatal("closing the debug screen turned voice on")
	}
}

func TestDebugExplainsWhyItCannotListen(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	a.voice.newSource = func(string) (voice.Source, error) { return nil, voice.ErrNoCapture }
	a.voiceDebugStart()
	defer func() { a.vdebug.resume = false; a.voiceDebugStop() }()
	view := ansi.Strip(a.voiceDebugView())
	if a.vdebug.eng != nil || !strings.Contains(view, "install one of") {
		t.Fatalf("no explanation of the missing helper:\n%s", view)
	}
	a.handleVoiceDebugKey(tea.KeyMsg{Type: tea.KeyEsc})

	b, _ := voiceApp(t, config.VoiceSettings{})
	b.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	b.voice.modelPath = func() string { return "" }
	b.voiceDebugStart()
	if !strings.Contains(ansi.Strip(b.voiceDebugView()), "/voice download") {
		t.Fatal("no explanation of the missing model")
	}
	b.handleVoiceDebugKey(tea.KeyMsg{Type: tea.KeyEsc})
}

func TestDebugShowsHardwareAndSilenceVerdict(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.handleVoiceDebugMsg(voiceDebugHardwareMsg{lines: []string{"default input: alsa_input.usb-Yeti", "input: alsa_input.usb-Yeti (running)"}})
	a.vdebug.started = time.Now().Add(-5 * time.Second)
	a.vdebug.tick(time.Now())
	view := ansi.Strip(a.voiceDebugView())
	for _, want := range []string{"Voice debug", "Hardware", "default input: alsa_input.usb-Yeti", "capture", "model", "Microphone level", "rms", "speech gate", "No audio data arrived from the capture helper", "Raw transcript", "Fast-model cleanup", "Events"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q", want)
		}
	}
	if !strings.Contains(strings.Join(a.vdebug.log, "\n"), "found 2 input line(s)") {
		t.Fatalf("log = %v", a.vdebug.log)
	}
	a.handleVoiceDebugMsg(voiceDebugHardwareMsg{})
	if !strings.Contains(strings.Join(a.vdebug.log, "\n"), "no probe program") {
		t.Fatal("an empty probe was not reported")
	}
}

func TestDebugTranscriptThenLiveCleanup(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.classifier = &fakeClassifier{raw: "Add a retry to the fetch."}
	cmd, handled := a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: a.vdebug.eng, ev: voice.Event{Kind: voice.EventTranscript, Text: "um add a a retry to the fetch"}})
	if !handled || cmd == nil || !a.vdebug.cleanBusy {
		t.Fatalf("handled=%v cmd=%v busy=%v", handled, cmd != nil, a.vdebug.cleanBusy)
	}
	if a.vdebug.raw != "um add a a retry to the fetch" {
		t.Fatalf("raw = %q", a.vdebug.raw)
	}
	// A second transcript while the fast model is busy is queued, not lost.
	a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: a.vdebug.eng, ev: voice.Event{Kind: voice.EventTranscript, Text: "and run the tests"}})
	if !a.vdebug.cleanDirty || !strings.Contains(a.vdebug.raw, "and run the tests") {
		t.Fatalf("dirty=%v raw=%q", a.vdebug.cleanDirty, a.vdebug.raw)
	}
	// Run the cleanup the first transcript started.
	var clean voiceDebugCleanMsg
	found := false
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if m, ok := c().(voiceDebugCleanMsg); ok {
				clean, found = m, true
			}
		}
	} else if m, ok := cmd().(voiceDebugCleanMsg); ok {
		clean, found = m, true
	}
	if !found {
		t.Fatal("the transcript did not start a cleanup")
	}
	next := a.voiceDebugCleaned(clean)
	if next == nil || !a.vdebug.cleanBusy {
		t.Fatal("the transcript that arrived meanwhile did not start a second cleanup")
	}
	if a.vdebug.cleaned != "Add a retry to the fetch." || !strings.Contains(a.vdebug.cleanNote, "tidied by the fast model") {
		t.Fatalf("cleaned = %q, note = %q", a.vdebug.cleaned, a.vdebug.cleanNote)
	}
	view := ansi.Strip(a.voiceDebugView())
	if !strings.Contains(view, "um add a a retry to the fetch") || !strings.Contains(view, "Add a retry to the fetch.") {
		t.Fatalf("the view does not show both texts:\n%s", view)
	}
}

func TestDebugCleanupFailureAndNoFastModel(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.classifier = nil
	if cmd := a.voiceDebugHeard("hello there"); cmd != nil {
		t.Fatal("cleanup ran with no fast model")
	}
	if !strings.Contains(a.vdebug.cleanNote, "no fast model is configured") {
		t.Fatalf("note = %q", a.vdebug.cleanNote)
	}
	a.voiceDebugCleaned(voiceDebugCleanMsg{err: errors.New("provider down"), took: time.Millisecond})
	if a.vdebug.cleaned != "" || !strings.Contains(a.vdebug.cleanNote, "provider down") || !strings.Contains(a.vdebug.cleanNote, "raw text") {
		t.Fatalf("note = %q", a.vdebug.cleanNote)
	}
	if cmd := a.voiceDebugHeard("   "); cmd != nil {
		t.Fatal("an empty transcript started work")
	}
}

func TestDebugRawTranscriptKeepsTheLast400Characters(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.classifier = nil
	for i := 0; i < 30; i++ {
		a.voiceDebugHeard(strings.Repeat("word ", 5) + "end")
	}
	if n := len([]rune(a.vdebug.raw)); n > dbgRawMax || n < dbgRawMax-10 {
		t.Fatalf("raw is %d characters, want about %d", n, dbgRawMax)
	}
}

func TestDebugLogsEngineStatesAndErrors(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: a.vdebug.eng, ev: voice.Event{Kind: voice.EventState, State: voice.StateListening}})
	a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: a.vdebug.eng, ev: voice.Event{Kind: voice.EventState, State: voice.StateListening}})
	a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: a.vdebug.eng, ev: voice.Event{Kind: voice.EventError, Err: errors.New("parecord stopped: no device")}})
	log := strings.Join(a.vdebug.log, "\n")
	if strings.Count(log, "engine listening") != 1 {
		t.Fatalf("a repeated state was logged twice: %s", log)
	}
	if !strings.Contains(log, "error: parecord stopped: no device") || !strings.Contains(ansi.Strip(a.voiceDebugView()), "parecord stopped") {
		t.Fatalf("the error is not shown: %s", log)
	}
	// An event from an engine that was closed is ignored.
	cmd, handled := a.handleVoiceDebugMsg(voiceDebugEventMsg{eng: nil, ev: voice.Event{Kind: voice.EventTranscript, Text: "late"}})
	if !handled || cmd != nil || a.vdebug.raw != "" {
		t.Fatal("a stale engine's event was delivered")
	}
}

func TestDebugKeysGoToTheLog(t *testing.T) {
	a, _ := startDebug(t, config.VoiceSettings{})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	log := strings.Join(a.vdebug.log, "\n")
	if !strings.Contains(log, "key f11 press") || !strings.Contains(log, "key x (not the voice key f11)") {
		t.Fatalf("log = %s", log)
	}
	a.handleVoiceDebugKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if len(a.vdebug.log) != 1 || !strings.Contains(a.vdebug.log[0], "cleared") || a.vdebug.raw != "" {
		t.Fatalf("clear left %v", a.vdebug.log)
	}
}

func TestDebugTickTracksTheLevelAndStopsWhenTheScreenIsLeft(t *testing.T) {
	a, mic := startDebug(t, config.VoiceSettings{})
	waitFor(t, "microphone", func() bool { return mic.starts.Load() == 1 })
	cmd, handled := a.handleVoiceDebugMsg(voiceDebugTickMsg{})
	if !handled || cmd == nil {
		t.Fatal("the tick did not reschedule")
	}
	if len(a.vdebug.hist) != 1 || a.vdebug.level != voice.Silent {
		t.Fatalf("hist=%d level=%+v", len(a.vdebug.hist), a.vdebug.level)
	}
	// Something else takes the screen: the microphone must not stay open.
	a.view = viewChat
	a.handleVoiceDebugMsg(voiceDebugTickMsg{})
	if a.vdebug.eng != nil {
		t.Fatal("the debug engine kept running after the screen was left")
	}
	waitFor(t, "microphone closed", func() bool { return mic.stops.Load() >= 1 })
}

func TestDebugStopWithNothingOpenIsANoOp(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	if cmd := a.voiceDebugStop(); cmd != nil {
		t.Fatal("stopping a screen that is not open returned a command")
	}
	a.voiceDebugStart()
	defer func() { a.vdebug.resume = false; a.voiceDebugStop() }()
	if cmd := a.voiceDebugStart(); cmd != nil {
		t.Fatal("starting twice opened a second engine")
	}
}

func TestVoiceCommandDebugOpensTheScreen(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	if cmd := a.voiceCommand("debug"); cmd == nil || a.view != viewVoiceDebug {
		t.Fatalf("/voice debug: cmd=%v view=%v", cmd != nil, a.view)
	}
	defer func() { a.vdebug.resume = false; a.voiceDebugStop() }()
	if !strings.Contains(strings.Join(NewRegistry(t.TempDir()).Complete("/voice d"), " "), "debug") &&
		!strings.Contains(strings.Join(NewRegistry(t.TempDir()).Complete("/voice "), " "), "debug") {
		t.Log("completion list checked by TestRegistryNames")
	}
}
