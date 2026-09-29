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

func newTestEngine(t *testing.T, mode Mode, rec *fakeRec, src *fakeSource) *Engine {
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
	eventually(t, "hearing", func() bool { return e.State() == StateHearing })
	for i := 0; i < 5; i++ {
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
	if rec.callCount() != 2 {
		t.Fatalf("recogniser calls = %d, want 2", rec.callCount())
	}
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
