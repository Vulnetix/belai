package tui

import (
	"context"
	"encoding/binary"
	"io"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/tts"
	"github.com/vulnetix/belai/internal/tui/components"
)

// tone is n samples of a loud sine as 16-bit PCM.
func tone(n int) []byte {
	b := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(12000 * math.Sin(2*math.Pi*220*float64(i)/tts.SampleRate))
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// ttsFakeEngine returns fixed audio and records what it was asked to say.
type ttsFakeEngine struct {
	mu    sync.Mutex
	texts []string
	voice []string
	pcm   []byte
	err   error
}

func (f *ttsFakeEngine) Name() string { return "fake" }
func (f *ttsFakeEngine) Synthesize(_ context.Context, text, voice string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.texts = append(f.texts, text)
	f.voice = append(f.voice, voice)
	if f.err != nil {
		return nil, f.err
	}
	return f.pcm, nil
}
func (f *ttsFakeEngine) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts)
}

// ttsSink stands in for the playback helper; with a gate each write waits.
type ttsSink struct {
	mu    sync.Mutex
	buf   []byte
	gate  chan struct{}
	opens int
}

func (s *ttsSink) opener() tts.Opener {
	return func(ctx context.Context) (io.WriteCloser, error) {
		s.mu.Lock()
		s.opens++
		s.mu.Unlock()
		return &ttsSinkW{s: s, ctx: ctx}, nil
	}
}

type ttsSinkW struct {
	s   *ttsSink
	ctx context.Context
}

func (w *ttsSinkW) Write(b []byte) (int, error) {
	if w.s.gate != nil {
		select {
		case <-w.s.gate:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	w.s.mu.Lock()
	w.s.buf = append(w.s.buf, b...)
	w.s.mu.Unlock()
	return len(b), nil
}
func (w *ttsSinkW) Close() error { return nil }

func (s *ttsSink) written() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.buf)
}

const ttsReply = "Here is the final report.\n\n```go\nx := 1\n```\n\nThe build passed and I pushed the fix."

// ttsApp is an App with reading aloud on and agreed to, and every seam set so
// nothing touches the network, a sound card or the real cache.
func ttsApp(t *testing.T) (*App, *ttsFakeEngine, *ttsSink) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
	a := New(Options{Workdir: t.TempDir()})
	a.classifier = nil
	a.settings.TTS = &config.TTSSettings{Enabled: on(), Consented: on()}
	eng := &ttsFakeEngine{pcm: tone(tts.SampleRate)} // one second
	sink := &ttsSink{}
	a.tts.engine, a.tts.open, a.tts.cacheDir, a.tts.noPace = eng, sink.opener(), t.TempDir(), true
	a.messages = append(a.messages, components.Message{Role: "assistant", Content: ttsReply})
	t.Cleanup(func() { a.ttsStop(true) })
	return a, eng, sink
}

func ttsDrain(t *testing.T, a *App) {
	t.Helper()
	for i := 0; i < 50; i++ {
		msg := ttsWait(a.tts.gen, a.tts.ch)().(ttsResultMsg)
		a.handleTTSMsg(msg)
		if msg.closed {
			return
		}
	}
	t.Fatal("the synthesis stream never closed")
}

func waitTTS(t *testing.T, a *App, want tts.State) tts.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := a.tts.player.Snapshot(); s.State == want {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("player never became %v, is %v", want, a.tts.player.Snapshot().State)
	return tts.Snapshot{}
}

func lastCard(a *App) *components.Message {
	for i := len(a.messages) - 1; i >= 0; i-- {
		if a.messages[i].Role == components.PlayerRole {
			return &a.messages[i]
		}
	}
	return nil
}

func TestTTSKeyReadsTheLastReplyAndDrawsACard(t *testing.T) {
	a, eng, sink := ttsApp(t)
	if cmd := a.ttsKey(); cmd == nil {
		t.Fatal("ctrl+b started nothing")
	}
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	if eng.calls() != 1 {
		t.Fatalf("%d requests", eng.calls())
	}
	said := eng.texts[0]
	if strings.Contains(said, "x := 1") || !strings.Contains(said, "Code omitted.") || !strings.Contains(said, "build passed") {
		t.Fatalf("what was sent: %q", said)
	}
	if eng.voice[0] != config.DefaultTTSVoice {
		t.Fatalf("voice %q", eng.voice[0])
	}
	if sink.written() != len(eng.pcm) {
		t.Fatalf("played %d bytes of %d", sink.written(), len(eng.pcm))
	}
	a.ttsRefreshCard() // the animation tick does this while it runs
	card := lastCard(a)
	if card == nil || !card.Ephemeral || card.Player == nil || card.Player.State != "ended" {
		t.Fatalf("card = %+v", card)
	}
	if !strings.HasPrefix(card.Player.Title, "Here is the final report.") {
		t.Fatalf("title %q", card.Player.Title)
	}
	if a.ttsCache().Size() == 0 {
		t.Fatal("a message read to the end was not cached")
	}
}

func TestTTSCardNeverReachesTheSessionRecordOrAModel(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.ttsKey()
	card := lastCard(a)
	if !neverPersisted(*card) {
		t.Fatal("the card would be persisted")
	}
	if card.Text() != "" || card.Content != "" {
		t.Fatal("the card carries text")
	}
	for _, turn := range a.buildTurns() {
		if strings.Contains(turn.Content, "read aloud") {
			t.Fatalf("a model turn carries the card: %q", turn.Content)
		}
	}
}

func TestTTSKeyOnTheHoveredReplyReadsThatOne(t *testing.T) {
	a, eng, _ := ttsApp(t)
	a.messages = append([]components.Message{{Role: "assistant", Content: "An earlier answer about caching."}}, a.messages...)
	a.hover = hoverTarget{text: true, msg: 0}
	a.ttsKey()
	ttsDrain(t, a)
	if len(eng.texts) != 1 || !strings.Contains(eng.texts[0], "caching") || a.tts.srcIdx != 0 {
		t.Fatalf("read %v from message %d", eng.texts, a.tts.srcIdx)
	}
}

func TestTTSKeyWithNothingToReadSaysSo(t *testing.T) {
	a, eng, _ := ttsApp(t)
	a.messages = nil
	if cmd := a.ttsKey(); cmd != nil {
		t.Fatal("started with nothing to read")
	}
	if !strings.Contains(lastSystem(a), "no reply to read") || eng.calls() != 0 {
		t.Fatalf("notice %q, %d requests", lastSystem(a), eng.calls())
	}
}

func TestTTSOffOrNotAgreedSendsNothing(t *testing.T) {
	for name, s := range map[string]*config.TTSSettings{
		"unset":               nil,
		"enabled, no consent": {Enabled: on()},
		"consented, off":      {Consented: on()},
	} {
		a, eng, _ := ttsApp(t)
		a.settings.TTS = s
		if cmd := a.ttsKey(); cmd != nil {
			t.Errorf("%s: started", name)
		}
		if eng.calls() != 0 || lastCard(a) != nil {
			t.Errorf("%s: %d requests, card %v", name, eng.calls(), lastCard(a))
		}
		if !strings.Contains(lastSystem(a), "/tts on") {
			t.Errorf("%s: the notice does not say how to turn it on: %q", name, lastSystem(a))
		}
	}
}

func TestTTSNoPlayerProgramIsAnErrorAndNoRequest(t *testing.T) {
	a, eng, _ := ttsApp(t)
	a.tts.open = nil
	t.Setenv("PATH", "")
	if cmd := a.ttsKey(); cmd != nil {
		t.Fatal("started without a player program")
	}
	if !strings.Contains(lastSystem(a), "no audio player") || eng.calls() != 0 || lastCard(a) != nil {
		t.Fatalf("notice %q, %d requests", lastSystem(a), eng.calls())
	}
}

func TestTTSNothingReadableSendsNothing(t *testing.T) {
	a, eng, _ := ttsApp(t)
	if cmd := a.ttsSpeak("![](only-an-image.png)", 0); cmd != nil {
		t.Fatal("started")
	}
	if eng.calls() != 0 || !strings.Contains(lastSystem(a), "nothing to read") {
		t.Fatalf("%d requests, %q", eng.calls(), lastSystem(a))
	}
}

func TestTTSReplayComesFromTheCacheWithNoRequest(t *testing.T) {
	a, eng, sink := ttsApp(t)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	if eng.calls() != 1 {
		t.Fatalf("%d requests: the second read must come from the cache", eng.calls())
	}
	if sink.written() != 2*len(eng.pcm) {
		t.Fatalf("played %d bytes, want the clip twice", sink.written())
	}
	if got := lastCard(a).Player.Status; got != "from cache" {
		t.Fatalf("status %q", got)
	}
}

func TestTTSCacheZeroRequestsEveryTime(t *testing.T) {
	a, eng, _ := ttsApp(t)
	zero := 0
	a.settings.TTS.CacheMB = &zero
	for i := 0; i < 2; i++ {
		a.ttsSpeak(ttsReply, 0)
		ttsDrain(t, a)
		waitTTS(t, a, tts.Ended)
	}
	if eng.calls() != 2 || a.ttsCache().Size() != 0 {
		t.Fatalf("%d requests, %d bytes cached", eng.calls(), a.ttsCache().Size())
	}
}

func TestTTSAFailedSynthesisShowsTheErrorAndDoesNotCache(t *testing.T) {
	a, eng, _ := ttsApp(t)
	eng.err = io.ErrUnexpectedEOF
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	s := waitTTS(t, a, tts.Failed)
	if s.Err == nil || !strings.Contains(lastCard(a).Player.Status, "failed") {
		t.Fatalf("err %v, status %q", s.Err, lastCard(a).Player.Status)
	}
	if a.ttsCache().Size() != 0 {
		t.Fatal("a failed read was cached")
	}
}

func TestTTSKeyOnTheReplyBeingReadPausesAndResumes(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.ttsKey()
	ttsDrain(t, a)
	sink.gate <- struct{}{}
	waitTTS(t, a, tts.Playing)
	a.ttsKey() // same reply: pause
	if s := a.tts.player.Snapshot(); s.State != tts.Paused {
		t.Fatalf("state %v after the second press", s.State)
	}
	a.ttsKey() // again: resume
	if s := a.tts.player.Snapshot(); !s.State.Active() {
		t.Fatalf("state %v after the third press", s.State)
	}
}

func TestTTSReadingAnotherReplyFreezesTheOldCard(t *testing.T) {
	a, eng, _ := ttsApp(t)
	a.ttsSpeak("First reply here.", 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	old := a.tts.cardIdx
	a.ttsSpeak("Second reply here.", 0)
	ttsDrain(t, a)
	if a.tts.cardIdx == old {
		t.Fatal("no new card")
	}
	frozen := a.messages[old].Player
	if frozen.State != "stopped" || frozen.Status != "replaced" {
		t.Fatalf("old card = %+v", frozen)
	}
	if eng.calls() != 2 {
		t.Fatalf("%d requests", eng.calls())
	}
	// A click on the old card does nothing to the new clip.
	before := a.tts.player.Snapshot().Speed
	a.ttsHit(old, components.Hit{Action: components.PlayerSpeed + "2"}, 0)
	if a.tts.player.Snapshot().Speed != before {
		t.Fatal("a click on a replaced card changed the current clip")
	}
}

func TestTTSCardClicks(t *testing.T) {
	a, eng, _ := ttsApp(t)
	eng.pcm = tone(30 * tts.SampleRate)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	card := a.tts.cardIdx
	dur := a.tts.player.Snapshot().Dur

	a.ttsHit(card, components.Hit{Action: components.PlayerSpeed + "1.5"}, 0)
	if a.tts.player.Snapshot().Speed != 1.5 {
		t.Fatal("the speed chip did not set the speed")
	}
	a.ttsHit(card, components.Hit{Action: components.PlayerBack}, 0)
	s := a.tts.player.Snapshot()
	if s.State != tts.Paused || s.Pos != dur-components.PlayerSkip {
		t.Fatalf("back from the end: %v at %v of %v", s.State, s.Pos, dur)
	}
	a.ttsHit(card, components.Hit{Action: components.PlayerFwd}, 0)
	if s := a.tts.player.Snapshot(); s.Pos != dur {
		t.Fatalf("forward clamps to the end, got %v of %v", s.Pos, dur)
	}
	bar := components.Hit{Col: 10, Width: 21, Action: components.PlayerSeek}
	a.ttsHit(card, bar, 20) // halfway along the bar
	if s := a.tts.player.Snapshot(); s.Pos != dur/2 || !a.tts.scrub {
		t.Fatalf("seek halfway: %v of %v, scrubbing %v", s.Pos, dur, a.tts.scrub)
	}
	a.ttsSeekTo(10) // drag to the left edge
	if a.tts.player.Snapshot().Pos != 0 {
		t.Fatal("dragging to the left edge did not go to the start")
	}
	a.ttsSeekTo(999) // and past the right edge
	if a.tts.player.Snapshot().Pos != dur {
		t.Fatal("dragging past the right edge did not clamp to the end")
	}
	a.ttsHit(card, components.Hit{Action: components.PlayerToggle}, 0)
	waitTTS(t, a, tts.Ended)
	a.ttsHit(card, components.Hit{Action: components.PlayerStop}, 0)
	if a.tts.player == nil {
		t.Fatal("stop dropped the clip; it must stay for replay")
	}
	a.ttsHit(card, components.Hit{Action: "nonsense"}, 0) // ignored
}

func TestTTSMouseHitsComeFromTheRenderedCard(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	a.width, a.height = 100, 40
	a.relayout()
	ml := components.MessageList{Messages: a.messages, Width: 96}
	_, lm := ml.Render()
	var found []string
	for i, sl := range lm {
		for _, h := range sl.Hits {
			if sl.Owner != a.tts.cardIdx {
				t.Fatalf("hit on line %d owned by %d, want the card", i, sl.Owner)
			}
			found = append(found, h.Action)
		}
	}
	for _, want := range []string{components.PlayerToggle, components.PlayerStop, components.PlayerBack, components.PlayerFwd, components.PlayerSeek, components.PlayerSpeed + "1.5"} {
		ok := false
		for _, f := range found {
			ok = ok || f == want
		}
		if !ok {
			t.Errorf("no %q hit in the rendered card: %v", want, found)
		}
	}
	// ttsMouse finds a hit by frame position.
	for i, sl := range lm {
		if len(sl.Hits) == 0 {
			continue
		}
		a.lastFrame.lines = lm
		h := sl.Hits[0]
		handled, _ := a.ttsMouse(components.Pos{Line: i, Col: h.Col})
		if !handled {
			t.Fatal("a press on a hit region was not handled")
		}
		if handled, _ := a.ttsMouse(components.Pos{Line: i, Col: 0}); handled {
			t.Fatal("a press left of every region was handled")
		}
		break
	}
	if handled, _ := a.ttsMouse(components.Pos{Line: 99999}); handled {
		t.Fatal("a press outside the frame was handled")
	}
}

func TestTTSSpokenStopStopsTheAudioBeforeTheTurn(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.voice.mode, a.voice.ready = config.VoiceModeListen, true
	turnCancelled := false
	a.cancel = func() { turnCancelled = true }
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	sink.gate <- struct{}{}
	waitTTS(t, a, tts.Playing)
	if !a.voiceCommandReady() && a.view == viewChat {
		// chat with a ready composer is ready by itself; the mic stays open
		// for "stop" in every other case through ttsActive.
		t.Log("composer ready")
	}
	if _, _, done := a.voiceGate("Stop."); !done {
		t.Fatal("stop was not consumed")
	}
	if a.tts.player.Snapshot().State != tts.Stopped {
		t.Fatal("the audio is still playing")
	}
	if turnCancelled {
		t.Fatal("the first stop also interrupted the turn")
	}
	if _, _, done := a.voiceGate("stop"); !done || !turnCancelled {
		t.Fatal("with nothing playing, stop interrupts the turn as before")
	}
}

func TestTTSKeepsTheMicrophoneOpenForStopWhilePlaying(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.voice.mode = config.VoiceModeListen
	if a.voiceCommandReady() {
		t.Fatal("ready with nothing playing")
	}
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	sink.gate <- struct{}{}
	waitTTS(t, a, tts.Playing)
	if !a.voiceCommandReady() {
		t.Fatal("not ready while a reply is being read")
	}
	off := false
	a.settings.Voice = &config.VoiceSettings{Commands: &off}
	if a.voiceCommandReady() {
		t.Fatal("voice.commands off still kept the microphone open")
	}
}

func TestTTSEchoFilter(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.voice.mode, a.voice.ready = config.VoiceModeListen, true
	if a.ttsEcho("the build passed and i pushed the fix") {
		t.Fatal("echo with nothing read")
	}
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	sink.gate <- struct{}{}
	waitTTS(t, a, tts.Playing)

	if !a.ttsEcho("The build passed and I pushed the fix.") {
		t.Fatal("the speaker's words were not recognised as echo")
	}
	if !a.ttsEcho("build passed pushed fix tomorrow morning") { // four of six, past six in ten
		t.Fatal("a mostly matching transcript was not echo")
	}
	if a.ttsEcho("build passed pushed deploy tomorrow morning") { // three of six
		t.Fatal("a half matching transcript was taken for echo")
	}
	if a.ttsEcho("deploy it to production tomorrow morning please") {
		t.Fatal("the user's own words were taken for echo")
	}
	if a.ttsEcho("stop") || a.ttsEcho("passed") {
		t.Fatal("a single word is never echo")
	}
	text, _, done := a.voiceGate("the build passed and I pushed the fix")
	if !done || text != "" || len(a.voice.queue) != 0 {
		t.Fatal("echo reached dictation")
	}
	if text, _, done := a.voiceGate("deploy it to production tomorrow morning"); done || text == "" {
		t.Fatalf("the user's words were dropped: %q %v", text, done)
	}

	a.ttsStop(false) // stopped: still echo for a moment
	if !a.ttsEcho("the build passed and i pushed the fix") {
		t.Fatal("the tail of the sound was not covered by the echo window")
	}
	a.tts.echoUntil = time.Now().Add(-time.Second)
	if a.ttsEcho("the build passed and i pushed the fix") {
		t.Fatal("echo after the window")
	}
}

func TestTTSAutoReadNeedsBothSettings(t *testing.T) {
	cases := []struct {
		name string
		s    *config.TTSSettings
		want bool
	}{
		{"reports off", &config.TTSSettings{Enabled: on(), Consented: on()}, false},
		{"reports on", &config.TTSSettings{Enabled: on(), Consented: on(), ReadReports: on()}, true},
		{"reports on, not agreed", &config.TTSSettings{Enabled: on(), ReadReports: on()}, false},
	}
	for _, c := range cases {
		a, eng, _ := ttsApp(t)
		a.settings.TTS = c.s
		cmd := a.ttsAutoRead(ttsReply)
		if (cmd != nil) != c.want {
			t.Errorf("%s: started=%v", c.name, cmd != nil)
		}
		if c.want {
			ttsDrain(t, a)
			if eng.calls() != 1 {
				t.Errorf("%s: %d requests", c.name, eng.calls())
			}
		} else if eng.calls() != 0 {
			t.Errorf("%s: a request was made", c.name)
		}
	}
	a, _, _ := ttsApp(t)
	a.settings.TTS.ReadReports = on()
	if a.ttsAutoRead("   ") != nil {
		t.Fatal("an empty reply was read")
	}
}

func TestTTSCommandOnIsTheConsent(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.settings.TTS = nil
	a.ttsCommand("on")
	if !a.settings.TTS.TTSEnabled() || !a.settings.TTS.TTSConsented() {
		t.Fatalf("/tts on left %+v", a.settings.TTS)
	}
	if out := lastSystem(a); !strings.Contains(out, "Microsoft") || !strings.Contains(out, "speech.platform.bing.com") {
		t.Fatalf("the disclosure is missing: %q", out)
	}
}

func TestTTSSettingsRowCannotGiveConsent(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.settings.TTS = nil
	if err := a.ttsToggle("tts.enabled"); err != nil {
		t.Fatal(err)
	}
	if a.settings.TTS.TTSEnabled() || a.settings.TTS.TTSConsented() {
		t.Fatal("the toggle turned reading aloud on without consent")
	}
	if !strings.Contains(lastSystem(a), "/tts on") {
		t.Fatalf("notice %q", lastSystem(a))
	}
	a.ttsCommand("on")
	if err := a.ttsToggle("tts.enabled"); err != nil {
		t.Fatal(err)
	}
	if a.settings.TTS.TTSEnabled() || !a.settings.TTS.TTSConsented() {
		t.Fatalf("toggling off after consent: %+v", a.settings.TTS)
	}
	if err := a.ttsToggle("tts.enabled"); err != nil {
		t.Fatal(err)
	}
	if !a.settings.TTS.TTSEnabled() {
		t.Fatal("an agreed feature could not be turned back on from the row")
	}
}

func TestTTSCommandOffStopsAndDropsTheClip(t *testing.T) {
	a, _, sink := ttsApp(t)
	a.ttsCommand("on") // writes the settings file, as a user would
	sink.gate = make(chan struct{})
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	a.ttsCommand("off")
	if a.settings.TTS.TTSEnabled() || a.tts.player != nil {
		t.Fatal("off left the feature on or the clip playing")
	}
	if !a.settings.TTS.TTSConsented() {
		t.Fatal("off forgot the agreement")
	}
}

func TestTTSCommandValidation(t *testing.T) {
	a, _, _ := ttsApp(t)
	for _, bad := range []string{"voice", "voice a b", "voice bad!name", "voice x'/>", "speed", "speed 9", "speed 0.1", "speed fast", "reports maybe", "reports"} {
		before := *a.settings.TTS
		a.ttsCommand(bad)
		if !strings.Contains(lastSystem(a), "usage") && !strings.Contains(lastSystem(a), "not a voice") {
			t.Errorf("/tts %s: %q", bad, lastSystem(a))
		}
		if a.settings.TTS.Voice != before.Voice || a.settings.TTS.Speed != before.Speed {
			t.Errorf("/tts %s changed a setting", bad)
		}
	}
	a.ttsCommand("voice en-GB-RyanNeural")
	a.ttsCommand("speed 1.5")
	a.ttsCommand("reports on")
	s := a.settings.TTS
	if s.TTSVoiceOr() != "en-GB-RyanNeural" || s.TTSSpeedOr() != 1.5 || !s.TTSReadReports() {
		t.Fatalf("settings = %+v", s)
	}
	a.ttsCommand("bogus")
	if !strings.Contains(lastSystem(a), "usage: /tts") {
		t.Fatalf("usage line: %q", lastSystem(a))
	}
}

func TestTTSCommandSpeedAppliesToThePlayingClip(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	a.ttsCommand("speed 2")
	if a.tts.player.Snapshot().Speed != 2 {
		t.Fatal("the playing clip kept its old speed")
	}
}

func TestTTSCommandStatusAndStopAndCache(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.ttsCommand("")
	out := lastSystem(a)
	for _, want := range []string{"read aloud: on", "reports off", config.DefaultTTSVoice, "1×", "256 MB", "ctrl+b", "Microsoft"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q: %s", want, out)
		}
	}
	a.ttsCommand("stop")
	if !strings.Contains(lastSystem(a), "nothing is playing") {
		t.Fatalf("stop with nothing playing: %q", lastSystem(a))
	}
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	a.ttsCommand("cache")
	if !strings.Contains(lastSystem(a), "read aloud cache:") || a.ttsCache().Size() == 0 {
		t.Fatalf("cache status %q", lastSystem(a))
	}
	a.ttsCommand("cache clear")
	if a.ttsCache().Size() != 0 || !strings.Contains(lastSystem(a), "cache cleared") {
		t.Fatalf("cache clear left %d bytes: %q", a.ttsCache().Size(), lastSystem(a))
	}
}

func TestTTSSettingsRows(t *testing.T) {
	a, _, _ := ttsApp(t)
	rows := map[string]settingsRow{}
	for _, r := range a.settingsRows() {
		rows[r.key] = r
	}
	want := map[string]string{"tts.enabled": "toggle", "tts.read_reports": "toggle", "tts.voice": "choose", "tts.speed": "choose", "tts.cache_mb": "choose"}
	for key, kind := range want {
		r, ok := rows[key]
		if !ok || r.kind != kind || r.help == "" || r.label == "" {
			t.Errorf("row %s = %+v", key, r)
		}
	}
	if err := a.ttsChoose("tts.speed", ttsSpeedChoices()); err != nil {
		t.Fatal(err)
	}
	if got := a.settings.TTS.TTSSpeedOr(); got != 1.25 {
		t.Fatalf("speed stepped to %v, want 1.25", got)
	}
	if err := a.ttsChoose("tts.cache_mb", []string{"0", "64", "256", "1024"}); err != nil || a.settings.TTS.TTSCacheMBOr() != 1024 {
		t.Fatalf("cache_mb = %d, %v", a.settings.TTS.TTSCacheMBOr(), err)
	}
	if err := a.ttsChoose("tts.voice", ttsVoices); err != nil || a.settings.TTS.TTSVoiceOr() != ttsVoices[1] {
		t.Fatalf("voice = %q, %v", a.settings.TTS.TTSVoiceOr(), err)
	}
	for _, key := range []string{"tts.voice", "tts.speed", "tts.cache_mb", "tts.read_reports", "tts.enabled"} {
		if err := a.ttsUnset(key); err != nil {
			t.Fatal(err)
		}
	}
	s := a.settings.TTS
	if s.TTSVoiceOr() != config.DefaultTTSVoice || s.TTSSpeedOr() != 1 || s.TTSCacheMBOr() != config.DefaultTTSCacheMB || s.TTSEnabled() {
		t.Fatalf("x did not restore the defaults: %+v", s)
	}
}

func TestTTSVoiceRowKeepsACustomVoice(t *testing.T) {
	opts := ttsVoiceOpts("en-AU-NatashaNeural")
	if opts[0] != "en-AU-NatashaNeural" || len(opts) != len(ttsVoices)+1 {
		t.Fatalf("opts = %v", opts)
	}
	if got := ttsVoiceOpts(ttsVoices[2]); len(got) != len(ttsVoices) {
		t.Fatalf("a listed voice was duplicated: %v", got)
	}
}

func TestTTSAfterSettingTurnOffStops(t *testing.T) {
	a, _, sink := ttsApp(t)
	sink.gate = make(chan struct{})
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	off := false
	a.settings.TTS.Enabled = &off
	a.ttsAfterSetting("tts.enabled")
	if a.tts.player != nil {
		t.Fatal("turning the row off left the clip")
	}
}

func TestTTSHelpAndHint(t *testing.T) {
	found := false
	for _, sec := range keySections() {
		for _, b := range sec.Bindings {
			found = found || b.Keys == "ctrl+b"
		}
	}
	if !found {
		t.Fatal("ctrl+b is not in the key help")
	}
	a, _, _ := ttsApp(t)
	a.hover = hoverTarget{text: true, msg: 0}
	if !strings.Contains(a.hoverHint(), "read aloud") {
		t.Fatalf("hint %q", a.hoverHint())
	}
	a.settings.TTS = nil
	if strings.Contains(a.hoverHint(), "read aloud") {
		t.Fatal("the hint offers reading aloud while it is off")
	}
}

func TestTTSStatusLines(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.tts.total, a.tts.done = 5, 1
	cases := []struct {
		s        tts.Snapshot
		finished bool
		cached   bool
		want     string
	}{
		{tts.Snapshot{State: tts.Buffering}, false, false, "synthesising 2 of 5"},
		{tts.Snapshot{State: tts.Playing}, false, true, "from cache"},
		{tts.Snapshot{State: tts.Ended}, true, true, "from cache"},
		{tts.Snapshot{State: tts.Ended}, true, false, "done · cached for replay"},
		{tts.Snapshot{State: tts.Paused}, true, false, "ready"},
		{tts.Snapshot{State: tts.Failed, Err: io.EOF}, true, false, "EOF"},
		{tts.Snapshot{State: tts.Failed}, true, false, "failed"},
	}
	for _, c := range cases {
		a.tts.finished, a.tts.cached = c.finished, c.cached
		if got := a.ttsStatus(c.s); got != c.want {
			t.Errorf("%v finished=%v cached=%v: %q, want %q", c.s.State, c.finished, c.cached, got, c.want)
		}
	}
}

func TestTTSTitleIsShortAndClean(t *testing.T) {
	got := ttsTitle("one two three four five six seven eight nine ten eleven")
	if got != "one two three four five six seven eight …" {
		t.Fatalf("title %q", got)
	}
	if strings.ContainsAny(ttsTitle("a\x1b[31mred\x00 text"), "\x1b\x00") {
		t.Fatal("control characters in the title")
	}
}

func TestTTSAnimationStopsWhenNothingMoves(t *testing.T) {
	a, _, _ := ttsApp(t)
	cmd := a.ttsSpeak(ttsReply, 0)
	if cmd == nil || !a.tts.animating {
		t.Fatal("reading did not start the animation")
	}
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	gen := a.tts.gen
	next, _ := a.handleTTSMsg(ttsAnimMsg{gen: gen})
	if next != nil || a.tts.animating {
		t.Fatal("the animation kept ticking after the clip ended")
	}
	if a.tts.echoUntil.IsZero() {
		t.Fatal("the echo window was not opened at the end")
	}
	if next, _ := a.handleTTSMsg(ttsAnimMsg{gen: gen + 1}); next != nil {
		t.Fatal("a tick from another clip was honoured")
	}
	if next, _ := a.handleTTSMsg(ttsResultMsg{gen: gen + 1}); next != nil {
		t.Fatal("a result from another clip was honoured")
	}
}

func TestTTSCardSurvivesATranscriptThatShrank(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	a.messages = nil // /clear and compaction rebuild the list
	a.ttsRefreshCard()
	a.ttsHit(a.tts.cardIdx, components.Hit{Action: components.PlayerToggle}, 0)
	if a.ttsKey() != nil {
		t.Fatal("started with nothing to read")
	}
}

func TestTTSRefreshCardCopiesThePlayersState(t *testing.T) {
	a, _, _ := ttsApp(t)
	a.ttsSpeak(ttsReply, 0)
	ttsDrain(t, a)
	waitTTS(t, a, tts.Ended)
	a.ttsRefreshCard()
	c := lastCard(a).Player
	if c.Speed != 1 || c.Dur != time.Second || !c.Final || len(c.Levels) != tts.Levels {
		t.Fatalf("card = %+v", c)
	}
}
