package voice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSource is a microphone the test feeds by hand.
type fakeSource struct {
	mu     sync.Mutex
	starts int
	stops  int
	ch     chan []int16
	closed bool
	err    error
}

func (f *fakeSource) Start(ctx context.Context) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return Stream{}, f.err
	}
	f.starts++
	ch := make(chan []int16, 256)
	f.ch, f.closed = ch, false
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		f.stops++
		f.closed = true
		close(ch)
		f.mu.Unlock()
	}()
	return Stream{C: ch, Err: func() error { return nil }}, nil
}

// send delivers audio to the open stream; audio for a stream the engine has
// already closed is dropped, as a real microphone's would be.
func (f *fakeSource) send(pcm []int16) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.ch <- pcm
	}
}

// queued is how many chunks the engine has not read yet.
func (f *fakeSource) queued() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ch == nil {
		return 0
	}
	return len(f.ch)
}

func (f *fakeSource) counts() (starts, stops int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

type fakeRec struct {
	mu    sync.Mutex
	calls int
	text  string
	err   error
}

func (r *fakeRec) Transcribe(ctx context.Context, pcm []float32) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.text, r.err
}

func (r *fakeRec) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func eventually(t *testing.T, what string, cond func() bool) {
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

func nextEvent(t *testing.T, e *Engine, kind EventKind) Event {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == kind {
				return ev
			}
		case <-timeout:
			t.Fatalf("no event of kind %d", kind)
		}
	}
}

func noTranscript(t *testing.T, e *Engine) {
	t.Helper()
	deadline := time.After(150 * time.Millisecond)
	for {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventTranscript {
				t.Fatalf("unexpected transcript %q", ev.Text)
			}
		case <-deadline:
			return
		}
	}
}

func newTestEngine(t *testing.T, mode Mode, rec Recognizer, src *fakeSource) *Engine {
	t.Helper()
	e := New(context.Background(), Config{
		Source: src, Mode: mode,
		Load: func(context.Context) (Recognizer, error) { return rec, nil },
	})
	t.Cleanup(e.Close)
	return e
}

func ready(t *testing.T, e *Engine) {
	t.Helper()
	e.SetReady(true)
	e.SetEnabled(true)
	eventually(t, "model loaded", func() bool { return e.State() != StateLoading && e.State() != StateOff })
}

func TestEngineIsOffAndSilentUntilEnabled(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "x"}
	e := newTestEngine(t, ModeListen, rec, src)
	e.SetReady(true)
	e.PTTDown()
	time.Sleep(30 * time.Millisecond)
	if e.State() != StateOff {
		t.Fatalf("state = %v, want off", e.State())
	}
	if starts, _ := src.counts(); starts != 0 {
		t.Fatalf("the microphone opened %d times before enabling", starts)
	}
}

func TestPushToTalkTranscribesAfterRelease(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "add a retry to the fetch"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	if e.State() != StateIdle {
		t.Fatalf("state = %v, want ready", e.State())
	}
	if starts, _ := src.counts(); starts != 0 {
		t.Fatal("push-to-talk opened the microphone before the key was held")
	}

	e.PTTDown()
	eventually(t, "microphone opens on key down", func() bool { s, _ := src.counts(); return s == 1 })
	// Holding the key opens the microphone; the state follows the voice, not the key.
	eventually(t, "listening while the key is held", func() bool { return e.State() == StateListening })
	src.send(tone(ms(100), 0.3))
	eventually(t, "hearing once there is speech", func() bool { return e.State() == StateHearing })
	for i := 0; i < 4; i++ {
		src.send(tone(ms(100), 0.3))
	}
	// Audio still queued at the release would count toward the tail, so let
	// the engine read what was held before letting go.
	eventually(t, "held audio read", func() bool { return src.queued() == 0 })
	e.PTTUp()
	// The tail keeps the microphone open until 300 ms more audio arrives.
	src.send(silence(ms(200)))
	if _, stops := src.counts(); stops != 0 {
		t.Fatal("the microphone closed before the release tail was captured")
	}
	src.send(silence(ms(200)))

	ev := nextEvent(t, e, EventTranscript)
	if ev.Text != "add a retry to the fetch" {
		t.Fatalf("transcript = %q", ev.Text)
	}
	eventually(t, "microphone closes", func() bool { _, s := src.counts(); return s == 1 })
	eventually(t, "back to ready", func() bool { return e.State() == StateIdle })
}

func TestPushToTalkSilenceIsNeverRecognised(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "you"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone opens", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(silence(ms(1000)))
	e.PTTUp()
	src.send(silence(ms(400)))
	noTranscript(t, e)
	if rec.callCount() != 0 {
		t.Fatal("the recogniser ran on silence")
	}
}

func TestListenModeTranscribesEachUtterance(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "run the tests"}
	e := newTestEngine(t, ModeListen, rec, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	eventually(t, "listening", func() bool { return e.State() == StateListening })

	src.send(silence(ms(300)))
	src.send(tone(ms(700), 0.3))
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })
	src.send(silence(ms(1200)))
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "run the tests" {
		t.Fatalf("transcript = %q", ev.Text)
	}
	src.send(tone(ms(700), 0.3))
	src.send(silence(ms(1200)))
	nextEvent(t, e, EventTranscript)
	// Partial guesses also call the recogniser, so calls are not counted: two
	// utterances gave two final transcripts, which is what was received.
}

func TestNotReadyClosesTheMicrophoneAndDropsAudio(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "should not appear"}
	e := newTestEngine(t, ModeListen, rec, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(700), 0.3))
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })

	e.SetReady(false)
	eventually(t, "paused", func() bool { return e.State() == StatePaused })
	eventually(t, "microphone closed", func() bool { _, s := src.counts(); return s == 1 })

	e.SetReady(true)
	eventually(t, "microphone reopens", func() bool { s, _ := src.counts(); return s == 2 })
	// The half-heard sentence is gone: only a fresh utterance is recognised.
	src.send(silence(ms(1000)))
	noTranscript(t, e)
	if rec.callCount() != 0 {
		t.Fatal("audio heard while paused was recognised")
	}
}

func TestPTTIsIgnoredWhenNotReady(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "x"}, src)
	ready(t, e)
	e.SetReady(false)
	eventually(t, "paused", func() bool { return e.State() == StatePaused })
	e.PTTDown()
	time.Sleep(30 * time.Millisecond)
	if starts, _ := src.counts(); starts != 0 {
		t.Fatal("the microphone opened while the composer was unavailable")
	}
}

func TestSetEnabledFalseStopsEverything(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModeListen, &fakeRec{text: "x"}, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	e.SetEnabled(false)
	eventually(t, "off", func() bool { return e.State() == StateOff })
	eventually(t, "microphone closed", func() bool { _, s := src.counts(); return s == 1 })
}

func TestModeSwitchAtRuntime(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "x"}, src)
	ready(t, e)
	e.SetMode(ModeListen)
	eventually(t, "microphone opens in listen mode", func() bool { s, _ := src.counts(); return s == 1 })
	e.SetMode(ModeListen) // no-op
	e.SetMode(ModePushToTalk)
	eventually(t, "microphone closes in push-to-talk", func() bool { _, s := src.counts(); return s == 1 })
	eventually(t, "ready", func() bool { return e.State() == StateIdle })
}

func TestModelLoadFailureReportsAndTurnsOff(t *testing.T) {
	e := New(context.Background(), Config{
		Source: &fakeSource{}, Mode: ModeListen,
		Load: func(context.Context) (Recognizer, error) { return nil, errors.New("weights missing") },
	})
	defer e.Close()
	e.SetReady(true)
	e.SetEnabled(true)
	ev := nextEvent(t, e, EventError)
	if ev.Err == nil || ev.Err.Error() != "voice model: weights missing" {
		t.Fatalf("err = %v", ev.Err)
	}
	eventually(t, "off", func() bool { return e.State() == StateOff })
}

func TestCaptureFailureTurnsOffUntilReEnabled(t *testing.T) {
	src := &fakeSource{err: errors.New("no microphone")}
	e := newTestEngine(t, ModeListen, &fakeRec{text: "x"}, src)
	e.SetReady(true)
	e.SetEnabled(true)
	if ev := nextEvent(t, e, EventError); ev.Err.Error() != "no microphone" {
		t.Fatalf("err = %v", ev.Err)
	}
	eventually(t, "off", func() bool { return e.State() == StateOff })
	// No retry loop: a failed source is not restarted until the user re-enables.
	time.Sleep(30 * time.Millisecond)
	src.mu.Lock()
	src.err = nil
	src.mu.Unlock()
	e.SetEnabled(true)
	eventually(t, "microphone opens after re-enable", func() bool { s, _ := src.counts(); return s == 1 })
}

func TestRecogniserErrorIsReportedNotFatal(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{err: errors.New("boom")}
	e := newTestEngine(t, ModeListen, rec, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(700), 0.3))
	src.send(silence(ms(1200)))
	if ev := nextEvent(t, e, EventError); ev.Err == nil {
		t.Fatal("nil error")
	}
	eventually(t, "still listening", func() bool { return e.State() == StateListening })
}

func TestStateEventsAndStrings(t *testing.T) {
	e := newTestEngine(t, ModeListen, &fakeRec{text: "x"}, &fakeSource{})
	e.SetReady(true)
	e.SetEnabled(true)
	seen := map[State]bool{}
	timeout := time.After(5 * time.Second)
	for !seen[StateListening] {
		select {
		case ev := <-e.Events():
			if ev.Kind == EventState {
				seen[ev.State] = true
			}
		case <-timeout:
			t.Fatalf("states seen: %v", seen)
		}
	}
	for s, want := range map[State]string{
		StateOff: "off", StateLoading: "loading", StatePaused: "paused", StateIdle: "ready",
		StateListening: "listening", StateHearing: "hearing", StateTranscribing: "transcribing", State(99): "unknown",
	} {
		if s.String() != want {
			t.Errorf("State(%d) = %q, want %q", s, s.String(), want)
		}
	}
}

func TestCloseIsIdempotentAndStopsTheMicrophone(t *testing.T) {
	src := &fakeSource{}
	e := New(context.Background(), Config{
		Source: src, Mode: ModeListen,
		Load: func(context.Context) (Recognizer, error) { return &fakeRec{}, nil },
	})
	e.SetReady(true)
	e.SetEnabled(true)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	e.Close()
	e.Close()
	if _, stops := src.counts(); stops != 1 {
		t.Fatalf("microphone stops = %d after Close", stops)
	}
	e.SetReady(false) // must not block after Close
}

func TestLevelFollowsTheMicrophone(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModeListen, &fakeRec{text: "x"}, src)
	if l := e.Level(); l != Silent {
		t.Fatalf("Level before any audio = %+v, want Silent", l)
	}
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(100), 0.3))
	eventually(t, "a level above silence", func() bool { return e.Level().RMS > -40 })
	l := e.Level()
	// 0.3 of full scale peaks near -10.5 dBFS and averages near -13.5.
	if l.Peak < -12 || l.Peak > -9 || l.RMS < -15 || l.RMS > -12 {
		t.Fatalf("Level = %+v for a 0.3 tone", l)
	}
	src.send(silence(ms(100)))
	eventually(t, "silence reads as silence", func() bool { return e.Level().RMS <= -90 })
	e.SetReady(false)
	eventually(t, "closing the microphone resets the level", func() bool { return e.Level() == Silent })
}

func TestLevelHelpers(t *testing.T) {
	if rms, peak := chunkLevels(nil); rms != 0 || peak != 0 {
		t.Fatal("chunkLevels(nil)")
	}
	if toDB(0) != floorDB || toDB(1) != 0 {
		t.Fatalf("toDB(0) = %v, toDB(1) = %v", toDB(0), toDB(1))
	}
	if SpeechDB > -30 || SpeechDB < -45 {
		t.Fatalf("SpeechDB = %v, want about -38", SpeechDB)
	}
	var b levelBox
	if b.get() != Silent {
		t.Fatal("an empty levelBox is not Silent")
	}
}

func TestPushToTalkStateFollowsTheVoice(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "x"}, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	eventually(t, "listening", func() bool { return e.State() == StateListening })
	src.send(tone(ms(200), 0.3))
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })
	// Half a second of quiet: the hold is over, the key is still down.
	src.send(silence(ms(500)))
	eventually(t, "back to listening between words", func() bool { return e.State() == StateListening })
	src.send(tone(ms(200), 0.3))
	eventually(t, "hearing again", func() bool { return e.State() == StateHearing })
}

func TestLatchedRecordingEndsWhenTheSpeakerStops(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "add a retry"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	e.PTTLatch() // a tap: no release will ever be reported
	src.send(tone(ms(600), 0.3))
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })
	// Under a second of quiet keeps it going.
	src.send(silence(ms(700)))
	time.Sleep(30 * time.Millisecond)
	if _, stops := src.counts(); stops != 0 {
		t.Fatal("the recording ended before a second of quiet")
	}
	src.send(silence(ms(700)))
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "add a retry" {
		t.Fatalf("transcript = %q", ev.Text)
	}
	eventually(t, "microphone closes by itself", func() bool { _, s := src.counts(); return s == 1 })
	eventually(t, "back to ready", func() bool { return e.State() == StateIdle })
}

func TestLatchedRecordingGivesUpOnSilence(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "should not run"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	e.PTTLatch()
	for i := 0; i < 11; i++ {
		src.send(silence(ms(1000)))
	}
	eventually(t, "microphone closes after ten seconds of nothing", func() bool { _, s := src.counts(); return s == 1 })
	noTranscript(t, e)
	if rec.callCount() != 0 {
		t.Fatal("ten seconds of silence was recognised")
	}
}

func TestUnlatchedRecordingNeverEndsOnItsOwn(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "x"}, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(300), 0.3))
	for i := 0; i < 3; i++ {
		src.send(silence(ms(1000)))
	}
	time.Sleep(30 * time.Millisecond)
	if _, stops := src.counts(); stops != 0 {
		t.Fatal("a held key was ended by silence: only a latched tap may end itself")
	}
	e.PTTLatch() // still recording, so it latches; a stray latch is otherwise a no-op
	e.PTTUp()
}

func TestPTTLatchWithNoRecordingIsANoOp(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "x"}, src)
	ready(t, e)
	e.PTTLatch()
	time.Sleep(20 * time.Millisecond)
	if starts, _ := src.counts(); starts != 0 || e.State() != StateIdle {
		t.Fatalf("a latch with no recording started something: starts=%d state=%v", starts, e.State())
	}
}

func TestSamplesCountsWhatArrives(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModeListen, &fakeRec{text: "x"}, src)
	if e.Samples() != 0 {
		t.Fatal("samples before any audio")
	}
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	// Open but delivering nothing: the count stays at zero.
	time.Sleep(30 * time.Millisecond)
	if e.Samples() != 0 {
		t.Fatal("samples counted with no audio delivered")
	}
	src.send(silence(ms(100)))
	src.send(tone(ms(100), 0.3))
	eventually(t, "both chunks counted", func() bool { return e.Samples() == int64(2*ms(100)) })
}

// slowRec blocks in Transcribe until released, so a test can act while a
// clip is being recognised.
type slowRec struct {
	release chan struct{}
	started chan struct{}
	text    string
}

func newSlowRec(text string) *slowRec {
	return &slowRec{release: make(chan struct{}), started: make(chan struct{}, 8), text: text}
}

func (r *slowRec) Transcribe(ctx context.Context, _ []float32) (string, error) {
	r.started <- struct{}{}
	select {
	case <-r.release:
		return r.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func nextPartial(t *testing.T, e *Engine) Event { t.Helper(); return nextEvent(t, e, EventPartial) }

func TestListenModeGuessesWhileYouAreStillTalking(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModeListen, &fakeRec{text: "add a retry"}, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	// 2.4 seconds of continuous speech, no pause: the utterance is still open.
	for i := 0; i < 24; i++ {
		src.send(tone(ms(100), 0.3))
	}
	ev := nextPartial(t, e)
	if ev.Text != "add a retry" {
		t.Fatalf("partial = %q", ev.Text)
	}
	if e.State() != StateHearing {
		t.Fatalf("state = %v while the speaker is still talking", e.State())
	}
	// The pause ends it; the final follows the guess.
	src.send(silence(ms(1200)))
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "add a retry" {
		t.Fatalf("final = %q", ev.Text)
	}
}

func TestPushToTalkGuessesWhileTheKeyIsHeld(t *testing.T) {
	src := &fakeSource{}
	e := newTestEngine(t, ModePushToTalk, &fakeRec{text: "run the tests"}, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	for i := 0; i < 20; i++ {
		src.send(tone(ms(100), 0.3))
	}
	if ev := nextPartial(t, e); ev.Text != "run the tests" {
		t.Fatalf("partial = %q", ev.Text)
	}
	eventually(t, "held audio read", func() bool { return src.queued() == 0 })
	e.PTTUp()
	src.send(silence(ms(400)))
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "run the tests" {
		t.Fatalf("final = %q", ev.Text)
	}
}

func TestNoGuessForShortOrSilentAudio(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "x"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(300), 0.3)) // under 0.6 s
	for i := 0; i < 20; i++ {
		src.send(silence(ms(100)))
	}
	time.Sleep(60 * time.Millisecond)
	select {
	case ev := <-e.Events():
		if ev.Kind == EventPartial {
			t.Fatalf("a guess was made from %q", ev.Text)
		}
	default:
	}
}

func TestFinalSupersedesAGuessInFlight(t *testing.T) {
	src := &fakeSource{}
	rec := newSlowRec("done")
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	for i := 0; i < 20; i++ {
		src.send(tone(ms(100), 0.3))
	}
	<-rec.started // the guess is being recognised and will not finish on its own
	eventually(t, "held audio read", func() bool { return src.queued() == 0 })
	e.PTTUp()
	src.send(silence(ms(400)))
	<-rec.started // the final has started: the guess was cancelled to make room
	close(rec.release)
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "done" {
		t.Fatalf("final = %q", ev.Text)
	}
}

func TestCancelDropsARecordingAndItsResult(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "should never appear"}
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(500), 0.3))
	e.Cancel()
	eventually(t, "microphone closes", func() bool { _, s := src.counts(); return s == 1 })
	eventually(t, "back to ready", func() bool { return e.State() == StateIdle })
	e.PTTUp() // a late release finds nothing to send
	noTranscript(t, e)
	if rec.callCount() != 0 {
		t.Fatalf("the recogniser ran %d times on cancelled audio", rec.callCount())
	}
}

func TestCancelDropsAResultAlreadyBeingRecognised(t *testing.T) {
	src := &fakeSource{}
	rec := newSlowRec("late")
	e := newTestEngine(t, ModePushToTalk, rec, src)
	ready(t, e)
	e.PTTDown()
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(500), 0.3))
	eventually(t, "audio read", func() bool { return src.queued() == 0 })
	e.PTTUp()
	src.send(silence(ms(400)))
	<-rec.started // the final is being recognised
	e.Cancel()
	close(rec.release)
	noTranscript(t, e)
	eventually(t, "the engine settles", func() bool { return e.State() == StateIdle })
}

func TestCancelKeepsAnAlwaysListeningMicrophoneOpenForTheNextPhrase(t *testing.T) {
	src := &fakeSource{}
	rec := &fakeRec{text: "second phrase"}
	e := newTestEngine(t, ModeListen, rec, src)
	ready(t, e)
	eventually(t, "microphone open", func() bool { s, _ := src.counts(); return s == 1 })
	src.send(tone(ms(400), 0.3))
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })
	e.Cancel()
	eventually(t, "listening again", func() bool { return e.State() == StateListening })
	if _, stops := src.counts(); stops != 0 {
		t.Fatal("cancel closed the always-listening microphone")
	}
	// The cancelled phrase never becomes a transcript...
	src.send(silence(ms(1200)))
	noTranscript(t, e)
	// ...and the next one does.
	src.send(tone(ms(700), 0.3))
	src.send(silence(ms(1200)))
	if ev := nextEvent(t, e, EventTranscript); ev.Text != "second phrase" {
		t.Fatalf("transcript = %q", ev.Text)
	}
}

func TestPartialsAreDroppableAndFinalsAreNot(t *testing.T) {
	e := &Engine{events: make(chan Event, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.emit(ctx, Event{Kind: EventPartial, Text: "one"})
	done := make(chan struct{})
	go func() { e.emit(ctx, Event{Kind: EventPartial, Text: "two"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a partial blocked on a full channel")
	}
}
