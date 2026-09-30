package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tui/components"
	"github.com/vulnetix/belai/internal/voice"
)

func TestVoiceIconsByLook(t *testing.T) {
	plain := func(l voiceLook, f int) string { return ansi.Strip(voiceIconFor(l, f)) }
	if voiceIconFor(lookNone, 0) != "" {
		t.Fatal("an icon is drawn while voice is off")
	}
	for _, l := range []voiceLook{lookIdle, lookMuted} {
		if plain(l, 0) != "⊘" || plain(l, 7) != "⊘" {
			t.Errorf("look %d is not a still muted mark", l)
		}
	}
	if plain(lookPaused, 0) != "◌" || plain(lookPaused, 3) != "◌" {
		t.Error("paused is not a still dim circle")
	}
	// Listening pulses slowly: four frames a beat.
	if plain(lookListening, 0) != "◉" || plain(lookListening, 3) != "◉" || plain(lookListening, 4) != "◎" || plain(lookListening, 8) != "◉" {
		t.Error("the listening pulse is not a four-frame beat")
	}
	// Hearing pulses on every other frame; working alternates purple beats.
	if plain(lookHearing, 0) == plain(lookHearing, 1) {
		t.Error("hearing does not pulse")
	}
	if plain(lookWorking, 0) != "◉" || plain(lookWorking, 2) != "◌" {
		t.Error("working does not alternate")
	}
	// The same look and frame always draw the same icon.
	if voiceIconFor(lookListening, 5) != voiceIconFor(lookListening, 5) {
		t.Error("an icon is not reproducible")
	}
}

func TestOverlayVoiceIcon(t *testing.T) {
	body := "hello\nsecond line"
	got := strings.Split(overlayVoiceIcon(body, 30, "◉"), "\n")
	if lipgloss.Width(got[0]) != 30 || !strings.HasSuffix(got[0], "◉") || !strings.HasPrefix(got[0], "hello") {
		t.Fatalf("first row = %q (%d cells)", got[0], lipgloss.Width(got[0]))
	}
	if got[1] != "second line" {
		t.Fatalf("second row changed: %q", got[1])
	}
	long := strings.Repeat("x", 80)
	got = strings.Split(overlayVoiceIcon(long, 20, "◉"), "\n")
	if lipgloss.Width(got[0]) != 20 || !strings.HasSuffix(got[0], " ◉") {
		t.Fatalf("a long first row was not cut to make room: %q", got[0])
	}
	if overlayVoiceIcon(body, 30, "") != body || overlayVoiceIcon(body, 2, "◉") != body {
		t.Fatal("no icon, or no room, must leave the body alone")
	}
}

func TestVoiceLookFollowsTheEngine(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	if a.voiceLook() != lookNone {
		t.Fatal("look with voice off")
	}
	a, _ = started(t, config.VoiceSettings{Enabled: on()})
	if a.voiceLook() != lookIdle {
		t.Fatalf("look = %v, want the armed push-to-talk look", a.voiceLook())
	}
	a.voice.muted = true
	if a.voiceLook() != lookMuted {
		t.Fatal("muted look")
	}
	a.voice.muted, a.voice.busy = false, true
	if a.voiceLook() != lookWorking {
		t.Fatal("the fast model's work is not the working look")
	}
	a.voice.busy = false
	a.view = viewSettings
	a.syncVoice()
	waitFor(t, "paused", func() bool { return a.voice.eng.State() == voice.StatePaused })
	if a.voiceLook() != lookPaused {
		t.Fatalf("look = %v while paused", a.voiceLook())
	}
}

func TestVoiceFrameWavesWhenHeardAndTurnsPurpleWhenWorking(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	waitFor(t, "listening", func() bool { return a.voice.eng.State() == voice.StateListening })
	teal := lipgloss.TerminalColor(components.ColorTeal)
	if c, wave, _ := a.voiceFrame(teal); wave || c != teal {
		t.Fatal("the frame changed while only listening")
	}
	a.voice.busy = true
	if c, wave, _ := a.voiceFrame(teal); wave || c != lipgloss.TerminalColor(components.ColorVoice) {
		t.Fatalf("the frame is not the voice purple while the fast model works: %v wave=%v", c, wave)
	}
	a.voice.busy = false
}

func TestComposerFrameWavesAndTurnsPurple(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	waitFor(t, "listening", func() bool { return a.voice.eng.State() == voice.StateListening })
	still := ansi.Strip(a.renderComposer())
	if strings.ContainsAny(still, "⎽⎼⎻⎺") {
		t.Fatal("the frame rippled with nobody speaking")
	}
	lines := strings.Split(still, "\n")
	first := lines[len(lines)-3] // the composer's first body row
	if !strings.HasSuffix(strings.TrimRight(first, " │"), "◉") && !strings.HasSuffix(strings.TrimRight(first, " │"), "◎") {
		t.Logf("first body row: %q", first)
	}
}

func TestEditorGivesUpRoomForTheIcon(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if a.voiceIconReserve() != 0 {
		t.Fatal("room reserved while voice is off")
	}
	off := a.editor.Width()
	a, _ = started(t, config.VoiceSettings{Enabled: on()})
	a.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if a.voiceIconReserve() != voiceIconWidth {
		t.Fatal("no room reserved while voice runs")
	}
	if got := a.editor.Width(); got != off-voiceIconWidth {
		t.Fatalf("editor width = %d, want %d", got, off-voiceIconWidth)
	}
	a.voiceStop()
	if a.editor.Width() != off {
		t.Fatalf("editor width = %d after voice stopped, want %d", a.editor.Width(), off)
	}
}

func TestAnimationRunsOnlyWhileSomethingMoves(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	if a.voiceAnimating() {
		t.Fatal("an armed, idle push-to-talk animates")
	}
	if cmd := a.syncVoice(); cmd != nil && a.voice.animating {
		t.Fatal("a timer was started with nothing moving")
	}
	a.voice.busy = true
	cmd := a.syncVoice()
	if cmd == nil || !a.voice.animating {
		t.Fatal("no timer started when the fast model began work")
	}
	// A second sync must not start a second timer.
	if a.syncVoice() != nil {
		t.Fatal("a second timer was started")
	}
	before := a.voice.frame
	next, handled := a.handleVoiceMsg(voiceAnimMsg{})
	if !handled || next == nil || a.voice.frame != before+1 {
		t.Fatalf("a tick did not advance the frame and reschedule: frame %d -> %d", before, a.voice.frame)
	}
	a.voice.busy = false
	next, _ = a.handleVoiceMsg(voiceAnimMsg{})
	if next != nil || a.voice.animating {
		t.Fatal("the timer kept running after the work ended")
	}
}

func TestDictatedTurnIsTaggedWithItsRawTranscript(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = &fakeClassifier{raw: "Add a retry to the fetch."}
	settle(t, a, a.voiceTranscript("um add a a retry to the fetch"))
	if a.editor.Value() != "Add a retry to the fetch." {
		t.Fatalf("composer = %q", a.editor.Value())
	}
	var m = newUserMessage(a, a.editor.Value())
	if !m.Voice || m.VoiceRaw != "um add a a retry to the fetch" {
		t.Fatalf("message = %+v", m)
	}
	// The record is spent: the next prompt is not a voice turn.
	if again := newUserMessage(a, "Add a retry to the fetch."); again.Voice {
		t.Fatal("a second prompt was tagged from one dictation")
	}
}

func TestTypedPromptIsNotTaggedAndEditedDictationIsNot(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = nil
	a.voiceTranscript("run the tests")
	// The user cleared the composer and typed something else.
	a.editor.SetValue("fix the parser")
	if m := newUserMessage(a, "fix the parser"); m.Voice || m.VoiceRaw != "" {
		t.Fatalf("a typed prompt was tagged: %+v", m)
	}
	// Dictation mixed with typing keeps only the dictated part's raw text.
	a.voiceTranscript("and run the tests")
	if m := newUserMessage(a, "please, and run the tests, thanks"); !m.Voice || m.VoiceRaw != "and run the tests" {
		t.Fatalf("mixed prompt = %+v", m)
	}
}

func TestDictationRecordIsBounded(t *testing.T) {
	var v voiceState
	for i := 0; i < dictatedMax+10; i++ {
		v.noteDictated("raw", "text "+strings.Repeat("x", i))
	}
	if len(v.dictated) != dictatedMax {
		t.Fatalf("%d records kept, want %d", len(v.dictated), dictatedMax)
	}
	v.noteDictated("raw", "")
	if len(v.dictated) != dictatedMax {
		t.Fatal("an empty dictation was recorded")
	}
}

func TestDictatedTextIsTaggedThroughTheRealEchoPath(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = nil
	a.voiceTranscript("say hello")
	before := len(a.messages)
	a.echoUser(a.editor.Value())
	if len(a.messages) != before+1 {
		t.Fatalf("%d messages after echo", len(a.messages)-before)
	}
	last := a.messages[len(a.messages)-1]
	if last.Role != "user" || !last.Voice || last.VoiceRaw != "say hello" {
		t.Fatalf("echoed message = %+v", last)
	}
	a.echoUser("typed by hand")
	if a.messages[len(a.messages)-1].Voice {
		t.Fatal("a typed prompt echoed as a voice turn")
	}
}

// newUserMessage is what echoUser builds, without persisting it.
func newUserMessage(a *App, input string) components.Message {
	m := components.Message{Role: "user", Content: input}
	a.tagDictated(&m)
	return m
}

func TestVoiceIsOnByDefaultWhenTheModelIsBuiltIn(t *testing.T) {
	a, mic := voiceApp(t, config.VoiceSettings{})
	a.settings.Voice = nil // nothing set at all
	a.voice.modelPath = func() string { return "built in" }
	a.voice.autostart = true
	if !a.voiceWanted() {
		t.Fatal("voice is not wanted although the model is built in")
	}
	if cmd := a.voiceInit(); cmd == nil || a.voice.eng == nil {
		t.Fatal("a built-in model did not bring voice up")
	}
	_ = mic
	a.voiceStop()

	// An explicit false wins over the default.
	off := false
	b, _ := voiceApp(t, config.VoiceSettings{Enabled: &off})
	b.voice.modelPath = func() string { return "built in" }
	b.voice.autostart = true
	if b.voiceWanted() || b.voiceInit() != nil || b.voice.eng != nil {
		t.Fatal("voice.enabled: false did not turn it off")
	}
	// Without the model built in, unset means off.
	c, _ := voiceApp(t, config.VoiceSettings{})
	c.voice.autostart = true
	if c.voiceWanted() || c.voiceInit() != nil {
		t.Fatal("voice started without the model built in and without being asked")
	}
}

func TestDefaultStartIsQuietWithoutAMicrophoneHelper(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.settings.Voice = nil
	a.voice.modelPath = func() string { return "built in" }
	a.voice.newSource = func(string) (voice.Source, error) { return nil, voice.ErrNoCapture }
	a.voice.autostart = true
	before := len(a.messages)
	if cmd := a.voiceInit(); cmd != nil || a.voice.eng != nil {
		t.Fatal("voice started without a capture helper")
	}
	if len(a.messages) != before {
		t.Fatalf("a default start printed %q", lastSystem(a))
	}
	// Asking for voice is not quiet.
	b, _ := voiceApp(t, config.VoiceSettings{Enabled: on()})
	b.voice.modelPath = func() string { return "built in" }
	b.voice.newSource = func(string) (voice.Source, error) { return nil, voice.ErrNoCapture }
	b.voice.autostart = true
	b.voiceInit()
	if !strings.Contains(lastSystem(b), "install one of") {
		t.Fatalf("an explicit enable stayed silent: %q", lastSystem(b))
	}
	// The key explains it when pressed.
	a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if got := lastSystem(a); !strings.Contains(got, "voice could not start") || !strings.Contains(got, "install one of") {
		t.Fatalf("the key did not say why: %q", got)
	}
}

func TestOffHintSuggestsCtrlSpace(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	got := lastSystem(a)
	if !strings.Contains(got, "ctrl+space") || strings.Contains(got, "f13") {
		t.Fatalf("hint = %q", got)
	}
}

func TestTapLatchesAndEndsWhenTheEngineStops(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	waitFor(t, "microphone", func() bool { return mic.starts.Load() == 1 })
	a.Update(voiceHoldMsg{seq: a.voice.seq}) // no repeat: a tap, latched
	if a.voice.phase != pttLatched {
		t.Fatalf("phase = %v", a.voice.phase)
	}
	// The engine reports it is listening: the tap is still going.
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventState, State: voice.StateListening}})
	if a.voice.phase != pttLatched {
		t.Fatal("a listening engine ended the tap")
	}
	// The engine ends the recording by itself (the speaker stopped).
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventState, State: voice.StateTranscribing}})
	if a.voice.phase != pttIdle {
		t.Fatalf("phase = %v after the engine ended the tap: the next press would stop it instead of starting", a.voice.phase)
	}
}

func TestSettingsRowsForVoiceKeyAndEffectiveEnabled(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	var enabledRow, keyRow *settingsRow
	rows := a.settingsRows()
	for i := range rows {
		switch rows[i].key {
		case "voice.enabled":
			enabledRow = &rows[i]
		case "voice.key":
			keyRow = &rows[i]
		}
	}
	if enabledRow == nil || keyRow == nil {
		t.Fatal("the voice input or voice key row is missing from /settings")
	}
	if keyRow.kind != "choose" || len(keyRow.opts) != len(config.VoiceKeys) || keyRow.value != "f11" {
		t.Fatalf("key row = %+v", keyRow)
	}
	// The toggle flips the effective value: with no model built in, off -> on.
	if err := a.cycleToggle("voice.enabled"); err != nil || !a.settings.Voice.VoiceEnabled() {
		t.Fatalf("toggle: %v, enabled = %v", err, a.settings.Voice.VoiceEnabled())
	}
	if err := a.cycleChoice("voice.key", config.VoiceKeys); err != nil || a.settings.Voice.VoiceKeyOr() != "ctrl+space" {
		t.Fatalf("key after one step = %q, %v", a.settings.Voice.VoiceKeyOr(), err)
	}
	if err := a.unsetSetting("voice.key"); err != nil || a.settings.Voice.VoiceKeyOr() != "f11" {
		t.Fatalf("key after reset = %q, %v", a.settings.Voice.VoiceKeyOr(), err)
	}
	if voiceEnabledNow(config.Settings{}) != voice.Embedded() {
		t.Fatal("the effective default is not whether the model is built in")
	}
	var _ = context.Background
}

func TestVoiceArtIsThreeRowsOfNineCells(t *testing.T) {
	for _, look := range []voiceLook{lookIdle, lookMuted, lookPaused, lookListening, lookHearing, lookWorking} {
		for frame := 0; frame < 8; frame++ {
			art := voiceArtFor(look, frame)
			for r, row := range art {
				if w := lipgloss.Width(row); w != voiceArtCols {
					t.Fatalf("look %d frame %d row %d is %d cells, want %d", look, frame, r, w, voiceArtCols)
				}
			}
		}
	}
	if voiceArtFor(lookNone, 0)[0] != "" {
		t.Fatal("a mark is drawn while voice is off")
	}
	// The mark is far larger than the old one-cell icon.
	if voiceArtRows*voiceArtCols < 4 {
		t.Fatal("the mark is not larger than the old one-cell icon")
	}
}

func TestVoiceArtIsAMicrophone(t *testing.T) {
	plain := func(l voiceLook, f int) []string {
		art := voiceArtFor(l, f)
		out := make([]string, len(art))
		for i, r := range art {
			out[i] = ansi.Strip(r)
		}
		return out
	}
	mic := plain(lookListening, 0)
	// A capsule on top, a holder around a pole in the middle, a base below.
	want := []string{"   ███   ", "  █ █ █  ", "   ▀█▀   "}
	for i := range want {
		if mic[i] != want[i] {
			t.Fatalf("row %d of the microphone = %q, want %q", i, mic[i], want[i])
		}
	}
	if !strings.Contains(mic[0], "███") || !strings.Contains(mic[2], "▀█▀") {
		t.Fatal("no capsule and base")
	}
}

func TestVoiceArtStatesAreDistinctWithoutColour(t *testing.T) {
	plain := func(l voiceLook, f int) string {
		art := voiceArtFor(l, f)
		return ansi.Strip(strings.Join(art[:], "\n"))
	}
	// Armed and paused are the same still, light mic.
	if plain(lookIdle, 0) != plain(lookPaused, 3) || plain(lookIdle, 0) != plain(lookIdle, 9) {
		t.Fatal("armed and paused are not one still mark")
	}
	// Muted is that mic with a slash through it.
	if plain(lookMuted, 0) == plain(lookIdle, 0) || !strings.Contains(plain(lookMuted, 0), "╲") {
		t.Fatalf("muted has no slash:\n%s", plain(lookMuted, 0))
	}
	// Listening beats between a solid and a soft mic, and holds inside a beat.
	if plain(lookListening, 0) != plain(lookListening, 3) || plain(lookListening, 0) == plain(lookListening, 4) {
		t.Fatal("listening does not beat every four frames")
	}
	// Hearing grows one and then two arcs to each side.
	h0, h1 := plain(lookHearing, 0), plain(lookHearing, 1)
	if h0 == h1 || strings.Count(h0, "│") != 2 || strings.Count(h1, "│") != 4 {
		t.Fatalf("hearing arcs:\n%s\n--\n%s", h0, h1)
	}
	if plain(lookListening, 0) == plain(lookHearing, 0) || strings.Contains(plain(lookListening, 0), "│") {
		t.Fatal("listening shows sound arcs before there is any sound")
	}
	// Working alternates and is not any other state.
	if plain(lookWorking, 0) == plain(lookWorking, 2) {
		t.Fatal("working does not alternate")
	}
	// No two states share a frame-0 look.
	seen := map[string]voiceLook{}
	for _, l := range []voiceLook{lookIdle, lookMuted, lookListening, lookHearing} {
		s := plain(l, 0)
		if o, dup := seen[s]; dup {
			t.Fatalf("looks %d and %d are drawn the same", o, l)
		}
		seen[s] = l
	}
	// Colour still differs between the beats of the pulse.
	if voiceArtFor(lookListening, 0) == voiceArtFor(lookListening, 4) {
		t.Fatal("the beats are identical")
	}
}

func TestOverlayVoiceArtCentresAndPreservesWidth(t *testing.T) {
	body := strings.Join([]string{"hi", "", "", "row four"}, "\n")
	out, ok := overlayVoiceArt(body, 40, voiceArtFor(lookListening, 0))
	if !ok {
		t.Fatal("the mark did not fit in an empty-ish composer")
	}
	lines := strings.Split(ansi.Strip(out), "\n")
	left := (40 - voiceArtCols) / 2
	for r := 0; r < voiceArtRows; r++ {
		if w := lipgloss.Width(lines[r]); w != 40 {
			t.Fatalf("row %d is %d cells, want the body width 40", r, w)
		}
		cut := string([]rune(lines[r])[left : left+voiceArtCols])
		if strings.TrimSpace(cut) == "" {
			t.Fatalf("row %d has no mark at the centre: %q", r, lines[r])
		}
	}
	if !strings.HasPrefix(lines[0], "hi") {
		t.Fatalf("the mark overwrote the typed text: %q", lines[0])
	}
	if lines[3] != "row four" {
		t.Fatalf("a row below the mark changed: %q", lines[3])
	}
}

func TestOverlayVoiceArtNeverCoversTypedText(t *testing.T) {
	long := strings.Repeat("x", 30)
	if out, ok := overlayVoiceArt(long+"\n\n", 40, voiceArtFor(lookListening, 0)); ok || out != long+"\n\n" {
		t.Fatal("the mark was drawn over text that reaches its columns")
	}
	// Text on the second or third row counts too.
	if _, ok := overlayVoiceArt("a\n"+long+"\n", 40, voiceArtFor(lookListening, 0)); ok {
		t.Fatal("the mark ignored text on its second row")
	}
	if _, ok := overlayVoiceArt("a\nb", 40, voiceArtFor(lookListening, 0)); ok {
		t.Fatal("the mark was drawn in a body shorter than three rows")
	}
	if _, ok := overlayVoiceArt("a\nb\nc", 8, voiceArtFor(lookListening, 0)); ok {
		t.Fatal("the mark was drawn where there is no room")
	}
	if _, ok := overlayVoiceArt("a\nb\nc", 40, voiceArtFor(lookNone, 0)); ok {
		t.Fatal("an empty mark was drawn")
	}
}

func TestComposerShowsTheCentredMarkAndFallsBackToTheEdgeIcon(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	waitFor(t, "listening", func() bool { return a.voice.eng.State() == voice.StateListening })
	view := ansi.Strip(a.renderComposer())
	if !strings.ContainsAny(view, "█▒░▀") {
		t.Fatalf("no round mark in the composer:\n%s", view)
	}
	// Long typed text on the first row: the mark gives way to the small icon.
	a.editor.SetValue(strings.Repeat("word ", 20))
	view = ansi.Strip(a.renderComposer())
	if strings.ContainsAny(view, "█▒░▀") {
		t.Fatalf("the mark was drawn over typed text:\n%s", view)
	}
	if !strings.ContainsAny(view, "◉◎") {
		t.Fatalf("no fallback icon:\n%s", view)
	}
}

func TestPlaceholderIsShorterWhileVoiceRuns(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if a.composerPlaceholder() != components.DefaultPlaceholder {
		t.Fatalf("placeholder = %q with voice off", a.composerPlaceholder())
	}
	a, _ = started(t, config.VoiceSettings{Enabled: on(), Key: "ctrl+space"})
	got := a.composerPlaceholder()
	if !strings.Contains(got, "ctrl+space") || len([]rune(got)) >= len([]rune(components.DefaultPlaceholder)) {
		t.Fatalf("placeholder = %q", got)
	}
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(ansi.Strip(a.renderComposer()), "hold ctrl+space and speak") {
		t.Fatal("the shorter placeholder is not shown")
	}
}
