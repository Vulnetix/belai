package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/voice"
)

// gateClassifier streams its pieces one at a time, each waiting for the test to
// let it through, so a test can look at the composer between two pieces.
type gateClassifier struct {
	pieces []string
	gate   chan struct{}
	err    error
}

func newGate(pieces ...string) *gateClassifier {
	return &gateClassifier{pieces: pieces, gate: make(chan struct{}, 64)}
}

func (g *gateClassifier) Classify(context.Context, rolemanager.ClassifierPayload) (string, error) {
	return strings.Join(g.pieces, ""), g.err
}

func (g *gateClassifier) ClassifyStream(ctx context.Context, _ rolemanager.ClassifierPayload, onText func(string)) (string, error) {
	var b strings.Builder
	for _, p := range g.pieces {
		select {
		case <-g.gate:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		b.WriteString(p)
		onText(p)
	}
	return b.String(), g.err
}

func partialEvent(a *App, text string) {
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventPartial, Text: text}})
}

func TestGuessShowsWhileSpeakingAndTheFinalReplacesIt(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = nil
	partialEvent(a, "add a")
	if a.editor.Value() != "add a" {
		t.Fatalf("the first guess did not appear: %q", a.editor.Value())
	}
	partialEvent(a, "add a retry to")
	if a.editor.Value() != "add a retry to" {
		t.Fatalf("a later guess did not replace the earlier one: %q", a.editor.Value())
	}
	// The final text takes the guess's place, it is not appended after it.
	a.voiceTranscript("add a retry to the fetch")
	if got := a.editor.Value(); got != "add a retry to the fetch" {
		t.Fatalf("the final text did not replace the guess: %q", got)
	}
	if a.voice.live.active {
		t.Fatal("the span was left open after the phrase finished")
	}
}

func TestGuessKeepsTypedTextAndAddsASpace(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = nil
	a.editor.SetValue("please")
	a.editor.CursorEnd()
	partialEvent(a, "add a retry")
	if a.editor.Value() != "please add a retry" {
		t.Fatalf("composer = %q", a.editor.Value())
	}
	a.voiceTranscript("add a retry to it")
	if a.editor.Value() != "please add a retry to it" {
		t.Fatalf("composer = %q", a.editor.Value())
	}
}

func TestGuessIsIgnoredWhenTheComposerCannotTakeItOrTheModelIsBusy(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.voice.busy = true
	partialEvent(a, "should not show")
	a.voice.busy = false
	if a.editor.Value() != "" {
		t.Fatalf("a guess appeared while the fast model was busy: %q", a.editor.Value())
	}
	a.view = viewSettings
	a.syncVoice()
	partialEvent(a, "nor this")
	a.view = viewChat
	a.editor.Reset()
	if a.editor.Value() != "" || a.voice.live.active {
		t.Fatal("a guess was written while the composer was unavailable")
	}
	partialEvent(a, "   ")
	if a.voice.live.active {
		t.Fatal("an empty guess opened a span")
	}
}

func TestEditingTheLiveTextTakesItOverAndTheFinalGoesAfterIt(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = nil
	partialEvent(a, "add a retry")
	// The user edits inside the guess (this stands in for a keystroke that
	// somehow did not cancel: the span must still refuse to overwrite it).
	a.editor.SetValue("add a RETRY")
	a.editor.CursorEnd()
	partialEvent(a, "add a retry now")
	if a.editor.Value() != "add a RETRY" {
		t.Fatalf("a guess overwrote text the user changed: %q", a.editor.Value())
	}
	a.voiceTranscript("add a retry now")
	if !strings.HasPrefix(a.editor.Value(), "add a RETRY") || !strings.Contains(a.editor.Value(), "add a retry now") {
		t.Fatalf("the final text was lost or replaced the user's edit: %q", a.editor.Value())
	}
}

func TestStreamedCleanupReplacesTheRawWordsPieceByPiece(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	g := newGate("Add ", "a retry", " to the fetch.")
	a.classifier = g
	cmd := a.voiceTranscript("um add a a retry to the fetch")
	if a.editor.Value() != "um add a a retry to the fetch" {
		t.Fatalf("raw text first: %q", a.editor.Value())
	}
	want := []string{"Add", "Add a retry", "Add a retry to the fetch."}
	for i, w := range want {
		g.gate <- struct{}{}
		msg := cmd()
		if _, ok := msg.(voiceCleanMsg); !ok {
			t.Fatalf("step %d: message %T", i, msg)
		}
		cmd, _ = a.handleVoiceMsg(msg)
		if a.editor.Value() != w {
			t.Fatalf("step %d: composer = %q, want %q", i, a.editor.Value(), w)
		}
		if i < len(want)-1 && !a.voice.busy {
			t.Fatalf("step %d: the tidy-up ended early", i)
		}
	}
	settle(t, a, cmd)
	if a.voice.busy || a.voice.live.active || a.editor.Value() != "Add a retry to the fetch." {
		t.Fatalf("end state: busy=%v live=%v composer=%q", a.voice.busy, a.voice.live.active, a.editor.Value())
	}
}

func TestCleanupFailureMidStreamPutsTheRawWordsBack(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	g := newGate("Add ", "a")
	g.err = errors.New("connection reset")
	a.classifier = g
	cmd := a.voiceTranscript("um add a")
	g.gate <- struct{}{}
	_, cmd = a.Update(cmd())
	if a.editor.Value() != "Add" {
		t.Fatalf("composer = %q", a.editor.Value())
	}
	g.gate <- struct{}{}
	settle(t, a, cmd)
	if a.editor.Value() != "um add a" || a.voice.busy {
		t.Fatalf("after the failure: composer=%q busy=%v", a.editor.Value(), a.voice.busy)
	}
}

func TestAnEmptyOrRunawayCleanupKeepsTheRawWords(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.classifier = &fakeClassifier{raw: strings.Repeat("Sure, here is a very long answer. ", 20)}
	settle(t, a, a.voiceTranscript("run tests"))
	if a.editor.Value() != "run tests" {
		t.Fatalf("a runaway reply replaced the words: %q", a.editor.Value())
	}
	a.editor.Reset()
	a.classifier = &fakeClassifier{raw: "   "}
	settle(t, a, a.voiceTranscript("run tests"))
	if a.editor.Value() != "run tests" {
		t.Fatalf("an empty reply replaced the words: %q", a.editor.Value())
	}
}

func typeKey(a *App, r rune) { a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}) }

func TestTypingCancelsARecordingAtOnce(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on()})
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	waitFor(t, "microphone opens", func() bool { return mic.starts.Load() == 1 })
	if !a.voiceActivity() {
		t.Fatal("a recording is not voice activity")
	}
	typeKey(a, 'x')
	if a.voice.phase != pttIdle {
		t.Fatalf("phase = %v after typing", a.voice.phase)
	}
	waitFor(t, "microphone closes", func() bool { return mic.stops.Load() == 1 })
	waitFor(t, "engine idle", func() bool { return a.voice.eng.State() == voice.StateIdle })
	if !strings.Contains(a.editor.Value(), "x") {
		t.Fatalf("the typed key did not reach the composer: %q", a.editor.Value())
	}
	// The key press that follows starts a fresh recording, not a stop.
	a.Update(tea.KeyMsg{Type: tea.KeyF11})
	if a.voice.phase != pttPressing {
		t.Fatalf("phase = %v: the next press should start a recording", a.voice.phase)
	}
}

func TestTypingCancelsTheLiveTextAndTheTidyUp(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	g := newGate("Tidied ", "words.")
	a.classifier = g
	cmd := a.voiceTranscript("raw words")
	g.gate <- struct{}{}
	_, cmd = a.Update(cmd()) // the tidy-up has begun replacing the raw words
	if a.editor.Value() != "Tidied" {
		t.Fatalf("composer = %q", a.editor.Value())
	}
	gen := a.voice.cleanGen
	typeKey(a, '!')
	if a.voice.busy || a.voice.live.active || a.voice.cleanGen == gen {
		t.Fatalf("typing left the tidy-up running: busy=%v live=%v", a.voice.busy, a.voice.live.active)
	}
	typed := a.editor.Value()
	// The tidy-up's remaining pieces arrive late and change nothing.
	g.gate <- struct{}{}
	settle(t, a, cmd)
	if a.editor.Value() != typed {
		t.Fatalf("a cancelled tidy-up still wrote to the composer: %q -> %q", typed, a.editor.Value())
	}
}

func TestTypingStopsSubmitDeliverySendingThePrompt(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Delivery: config.VoiceDeliverySubmit})
	g := newGate("Say hello.")
	a.classifier = g
	cmd := a.voiceTranscript("say hello")
	typeKey(a, '?')
	g.gate <- struct{}{}
	settle(t, a, cmd)
	// Enter would open the agent picker in this unconfigured app: it was not pressed.
	if a.agentPickerOpen || a.agentPickerSubmit {
		t.Fatal("submit delivery pressed Enter after the user had typed")
	}
	if len(a.voice.queue) != 0 {
		t.Fatal("the queue outlived the cancel")
	}
}

func TestTypingClearsQueuedAndHeldPhrases(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.voice.queue = []string{"waiting one", "waiting two"}
	a.voice.held, a.voice.heldRaw = "held text", "held raw"
	typeKey(a, 'a')
	if len(a.voice.queue) != 0 || a.voice.held != "" || a.voice.heldRaw != "" {
		t.Fatalf("queue=%v held=%q heldRaw=%q", a.voice.queue, a.voice.held, a.voice.heldRaw)
	}
}

func TestEnterAlsoCancelsVoice(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	g := newGate("x")
	a.classifier = g
	a.voiceTranscript("run the tests")
	if !a.voice.busy {
		t.Fatal("test setup: nothing in flight")
	}
	a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if a.voice.busy || a.voice.live.active {
		t.Fatal("the user's Enter did not cancel voice")
	}
}

func TestOnlyChatKeysCancelAndTheVoiceKeyDoesNot(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	a.voice.queue = []string{"pending"}
	a.view = viewSettings
	typeKey(a, 'q')
	if len(a.voice.queue) != 1 {
		t.Fatal("a key on another screen cancelled voice")
	}
	a.view = viewChat
	a.syncVoice()
	a.Update(tea.KeyMsg{Type: tea.KeyF11}) // the voice key itself
	if a.voice.phase == pttIdle {
		t.Fatal("test setup: the voice key did not start a recording")
	}
	if len(a.voice.queue) != 1 {
		t.Fatal("the voice key cancelled voice")
	}
	typeKey(a, 'z')
	if len(a.voice.queue) != 0 {
		t.Fatal("a typed key did not cancel")
	}
}

func TestTypingWithNothingHappeningLeavesVoiceAlone(t *testing.T) {
	a, mic := started(t, config.VoiceSettings{Enabled: on(), Mode: config.VoiceModeListen})
	waitFor(t, "listening", func() bool { return a.voice.eng.State() == voice.StateListening })
	if a.voiceActivity() {
		t.Fatal("a quietly listening microphone is not activity")
	}
	typeKey(a, 'h')
	typeKey(a, 'i')
	if mic.stops.Load() != 0 || a.voice.eng.State() != voice.StateListening {
		t.Fatalf("typing disturbed an idle always-listening microphone: stops=%d state=%v", mic.stops.Load(), a.voice.eng.State())
	}
}

func TestVoiceActivityTable(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	if a.voiceActivity() {
		t.Fatal("activity with voice off")
	}
	a, _ = started(t, config.VoiceSettings{Enabled: on()})
	if a.voiceActivity() {
		t.Fatal("activity while armed and idle")
	}
	for name, set := range map[string]func(){
		"live text": func() { a.voice.live.active = true },
		"tidy-up":   func() { a.voice.busy = true },
		"queue":     func() { a.voice.queue = []string{"x"} },
		"held":      func() { a.voice.held = "x" },
		"recording": func() { a.voice.phase = pttHeld },
	} {
		a.voice.live, a.voice.busy, a.voice.queue, a.voice.held, a.voice.phase = liveSpan{}, false, nil, "", pttIdle
		set()
		if !a.voiceActivity() {
			t.Errorf("%s: not counted as activity", name)
		}
	}
	a.voice.live, a.voice.busy, a.voice.queue, a.voice.held, a.voice.phase = liveSpan{}, false, nil, "", pttIdle
}

func TestLosingTheComposerMidStreamCutsTheTidyUpButKeepsTheWords(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	g := newGate("Tidied.")
	a.classifier = g
	cmd := a.voiceTranscript("raw words")
	a.view = viewSettings
	a.syncVoice()
	if a.voice.busy || a.voice.live.active {
		t.Fatal("the tidy-up kept running behind another screen")
	}
	g.gate <- struct{}{}
	a.view = viewChat
	settle(t, a, cmd)
	if a.editor.Value() != "raw words" {
		t.Fatalf("composer = %q: the words already shown must stay", a.editor.Value())
	}
}

func TestVoiceLogOffHidesAutomaticNoticesAndCleanupRows(t *testing.T) {
	off := false
	a, _ := started(t, config.VoiceSettings{Enabled: on(), Log: &off})
	before := len(a.messages)
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventError, Err: errors.New("helper stopped")}})
	a.addRMActivity(rolemanager.Activity{Event: rolemanager.EventVoiceCleanup, Verdict: "cleaned"})
	if len(a.messages) != before {
		t.Fatalf("the log is off but %d rows were added: %q", len(a.messages)-before, lastSystem(a))
	}
	// A command's own answer is always shown.
	a.voiceCommand("status")
	if len(a.messages) != before+1 {
		t.Fatal("a command's answer was hidden by the log switch")
	}
	// The hint that explains a silent key stays too.
	b, _ := voiceApp(t, config.VoiceSettings{Log: &off})
	b.handleVoiceMsg(tea.KeyMsg{Type: tea.KeyF11})
	if !strings.Contains(lastSystem(b), "voice is off") {
		t.Fatalf("the key hint was hidden: %q", lastSystem(b))
	}
}

func TestVoiceLogOnShowsThem(t *testing.T) {
	a, _ := started(t, config.VoiceSettings{Enabled: on()})
	before := len(a.messages)
	a.handleVoiceMsg(voiceEventMsg{eng: a.voice.eng, ev: voice.Event{Kind: voice.EventError, Err: errors.New("helper stopped")}})
	if len(a.messages) != before+1 || !strings.Contains(lastSystem(a), "helper stopped") {
		t.Fatal("the error was not shown with the log on")
	}
	a.addRMActivity(rolemanager.Activity{Event: rolemanager.EventVoiceCleanup, Verdict: "cleaned"})
	if len(a.messages) != before+2 {
		t.Fatal("the cleanup row was not shown with the log on")
	}
}

func TestVoiceLogSettingsRow(t *testing.T) {
	a, _ := voiceApp(t, config.VoiceSettings{})
	var row *settingsRow
	rows := a.settingsRows()
	for i := range rows {
		if rows[i].key == "voice.log" {
			row = &rows[i]
		}
	}
	if row == nil || row.kind != "toggle" || row.value != "on" {
		t.Fatalf("voice log row = %+v", row)
	}
	if err := a.cycleToggle("voice.log"); err != nil || a.settings.Voice.VoiceLogEnabled() {
		t.Fatalf("toggle off: %v, log = %v", err, a.settings.Voice.VoiceLogEnabled())
	}
	if err := a.cycleToggle("voice.log"); err != nil || !a.settings.Voice.VoiceLogEnabled() {
		t.Fatalf("toggle on: %v", err)
	}
	if err := a.cycleToggle("voice.log"); err != nil {
		t.Fatal(err)
	}
	if err := a.unsetSetting("voice.log"); err != nil || !a.settings.Voice.VoiceLogEnabled() {
		t.Fatalf("reset: %v, log = %v", err, a.settings.Voice.VoiceLogEnabled())
	}
}
