package tts

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fakeSink records what each playback was given, one buffer per Opener call.
// With a gate it blocks each write until released, like a full device buffer.
type fakeSink struct {
	mu    sync.Mutex
	opens [][]byte
	gate  chan struct{}
	err   error
}

func (f *fakeSink) opener() Opener {
	return func(ctx context.Context) (io.WriteCloser, error) {
		if f.err != nil {
			return nil, f.err
		}
		f.mu.Lock()
		f.opens = append(f.opens, nil)
		i := len(f.opens) - 1
		f.mu.Unlock()
		return &fakeW{f: f, i: i, ctx: ctx}, nil
	}
}

type fakeW struct {
	f   *fakeSink
	i   int
	ctx context.Context
}

func (w *fakeW) Write(b []byte) (int, error) {
	if w.f.gate != nil {
		select {
		case <-w.f.gate:
		case <-w.ctx.Done():
			return 0, w.ctx.Err()
		}
	}
	w.f.mu.Lock()
	w.f.opens[w.i] = append(w.f.opens[w.i], b...)
	w.f.mu.Unlock()
	return len(b), nil
}
func (w *fakeW) Close() error { return nil }

func (f *fakeSink) open(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i >= len(f.opens) {
		return nil
	}
	return append([]byte(nil), f.opens[i]...)
}

// release lets writes through until playback i has received data. A writer
// that was cancelled can still take a token, so one is never enough.
func (f *fakeSink) release(i int) {
	deadline := time.Now().Add(2 * time.Second)
	for len(f.open(i)) == 0 && time.Now().Before(deadline) {
		select {
		case f.gate <- struct{}{}:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *fakeSink) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.opens {
		n += len(o)
	}
	return n
}

// ramp is n samples where sample i is i, so any byte offset names its sample.
func ramp(n int) []byte {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(i % 30000)
	}
	return pcmBytes(s)
}

func sample(b []byte, i int) int16 { return int16(uint16(b[2*i]) | uint16(b[2*i+1])<<8) }

func waitState(t *testing.T, p *Player, want State) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := p.Snapshot(); s.State == want {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("state never became %v, is %v", want, p.Snapshot().State)
	return Snapshot{}
}

func fastPlayer(f *fakeSink) *Player {
	p := NewPlayer(f.opener())
	p.Pace = 0
	return p
}

func TestPlayerPlaysAllAudioInOrderAndEnds(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(5000)
	p.Append(in[:4000])
	p.Append(in[4000:])
	p.Finish(nil)
	p.Play()
	s := waitState(t, p, Ended)
	if string(f.open(0)) != string(in) {
		t.Fatalf("wrote %d bytes, want the %d given, in order", len(f.open(0)), len(in))
	}
	if s.Pos != s.Dur || !s.Final {
		t.Fatalf("ended at %v of %v, final %v", s.Pos, s.Dur, s.Final)
	}
}

func TestPlayerWaitsForAudioThatArrivesLater(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	p.Play()
	waitState(t, p, Buffering)
	time.Sleep(60 * time.Millisecond)
	if p.Snapshot().State != Buffering || f.total() != 0 {
		t.Fatal("played with no audio")
	}
	in := ramp(3000)
	p.Append(in)
	deadline := time.Now().Add(2 * time.Second)
	for f.total() < len(in) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.Finish(nil)
	waitState(t, p, Ended)
	if string(f.open(0)) != string(in) {
		t.Fatal("late audio was lost or reordered")
	}
}

func TestPlayerSeekStartsAtTheOffset(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(2 * SampleRate)
	p.Append(in)
	p.Finish(nil)
	p.Seek(500 * time.Millisecond)
	p.Play()
	waitState(t, p, Ended)
	got := f.open(0)
	if len(got) == 0 || sample(got, 0) != sample(in, SampleRate/2) {
		t.Fatalf("first sample %d, want %d (0.5 s in)", sample(got, 0), sample(in, SampleRate/2))
	}
	if want := len(in) - SampleRate; len(got) != want {
		t.Fatalf("wrote %d bytes, want the last %d", len(got), want)
	}
}

func TestPlayerSeekWhilePlayingRestartsThere(t *testing.T) {
	f := &fakeSink{gate: make(chan struct{})}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(2 * SampleRate)
	p.Append(in)
	p.Finish(nil)
	p.Play()
	f.gate <- struct{}{}
	f.gate <- struct{}{}
	waitState(t, p, Playing)
	p.Seek(time.Second)
	f.release(1)
	if got := f.open(1); len(got) == 0 || sample(got, 0) != sample(in, SampleRate) {
		t.Fatal("a seek while playing did not restart at the new offset")
	}
}

func TestPlayerPauseIsImmediateAndResumeLosesNothing(t *testing.T) {
	f := &fakeSink{gate: make(chan struct{})}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(SampleRate)
	p.Append(in)
	p.Finish(nil)
	p.Play()
	for i := 0; i < 5; i++ {
		f.gate <- struct{}{}
	}
	waitState(t, p, Playing)
	time.Sleep(20 * time.Millisecond)
	p.Pause() // the writer is blocked on the gate: pause must not wait for it
	s := p.Snapshot()
	if s.State != Paused || s.Pos <= 0 || s.Pos >= s.Dur {
		t.Fatalf("after pause: %v at %v of %v", s.State, s.Pos, s.Dur)
	}
	p.mu.Lock()
	at := p.pos
	p.mu.Unlock()
	p.Play()
	f.release(1)
	if got := f.open(1); len(got) == 0 || sample(got, 0) != sample(in, at) {
		t.Fatalf("resumed at the wrong sample, want %d", at)
	}
}

func TestPlayerSpeedShortensTheAudioWritten(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	in := sine(220, 3)
	p.Append(pcmBytes(in))
	p.Finish(nil)
	p.SetSpeed(2)
	p.Play()
	waitState(t, p, Ended)
	want := len(in) // bytes: half the samples at 2 bytes each
	if got := len(f.open(0)); got < want*98/100 || got > want*102/100 {
		t.Fatalf("wrote %d bytes at 2x, want about %d", got, want)
	}
}

func TestPlayerEndedReplaysFromTheTop(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(3000)
	p.Append(in)
	p.Finish(nil)
	p.Play()
	waitState(t, p, Ended)
	p.Play()
	waitState(t, p, Ended)
	if string(f.open(1)) != string(in) {
		t.Fatal("replay did not play the whole clip")
	}
}

func TestPlayerStopThenPlayRestarts(t *testing.T) {
	f := &fakeSink{gate: make(chan struct{})}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(SampleRate)
	p.Append(in)
	p.Finish(nil)
	p.Play()
	f.gate <- struct{}{}
	f.gate <- struct{}{}
	waitState(t, p, Playing)
	p.Stop()
	if p.Snapshot().State != Stopped {
		t.Fatal("not stopped")
	}
	p.Play()
	f.release(1)
	if got := f.open(1); len(got) == 0 || sample(got, 0) != sample(in, 0) {
		t.Fatal("play after stop did not start from the top")
	}
}

func TestPlayerOpenErrorIsFailed(t *testing.T) {
	boom := errors.New("no sound card")
	p := NewPlayer((&fakeSink{err: boom}).opener())
	defer p.Close()
	p.Append(ramp(100))
	p.Play()
	if s := waitState(t, p, Failed); !errors.Is(s.Err, boom) {
		t.Fatalf("err = %v", s.Err)
	}
}

func TestPlayerSynthesisErrorPlaysWhatArrivedThenFails(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	in := ramp(2000)
	p.Append(in)
	boom := errors.New("service gone")
	p.Finish(boom)
	p.Play()
	s := waitState(t, p, Failed)
	if !errors.Is(s.Err, boom) || string(f.open(0)) != string(in) {
		t.Fatalf("err %v, wrote %d of %d bytes", s.Err, len(f.open(0)), len(in))
	}
}

func TestPlayerFailsWhenSynthesisFailsBeforeAnyAudio(t *testing.T) {
	p := fastPlayer(&fakeSink{})
	defer p.Close()
	p.Play()
	p.Finish(errors.New("nope"))
	waitState(t, p, Failed)
}

func TestPlayerLevelsFollowTheLoudness(t *testing.T) {
	f := &fakeSink{gate: make(chan struct{})}
	p := fastPlayer(f)
	defer p.Close()
	p.Append(pcmBytes(sine(440, 1)))
	p.Finish(nil)
	p.Play()
	for i := 0; i < 3; i++ {
		f.gate <- struct{}{}
	}
	waitState(t, p, Playing)
	time.Sleep(20 * time.Millisecond)
	lv := p.Snapshot().Levels
	if len(lv) != Levels || lv[Levels-1] <= 0.2 || lv[Levels-1] > 1 {
		t.Fatalf("levels %v for a loud tone", lv)
	}
	if level(make([]int16, 1200)) != 0 {
		t.Fatal("silence is not level 0")
	}
}

func TestPlayerHoldsWritesToTheClock(t *testing.T) {
	f := &fakeSink{}
	p := NewPlayer(f.opener()) // real time
	defer p.Close()
	p.Append(pcmBytes(sine(220, 2)))
	p.Finish(nil)
	p.Play()
	time.Sleep(120 * time.Millisecond)
	got := f.total()
	ceiling := int((lead + 120*time.Millisecond + 50*time.Millisecond).Seconds() * SampleRate * BytesPerSample)
	if got == 0 || got > ceiling {
		t.Fatalf("after 120 ms %d bytes were written, want 1..%d: writes must follow the clock", got, ceiling)
	}
}

func TestPlayerSnapshotIsSafeUnderRace(t *testing.T) {
	f := &fakeSink{}
	p := fastPlayer(f)
	defer p.Close()
	p.Append(ramp(SampleRate))
	p.Finish(nil)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			p.Snapshot()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			p.Play()
			p.Seek(time.Duration(i) * 10 * time.Millisecond)
			p.SetSpeed(1 + float64(i%3)/2)
			p.Pause()
		}
	}()
	wg.Wait()
}

func TestHelpersUseAFixedArgvAtTheRightRate(t *testing.T) {
	hs := helpers()
	if len(hs) == 0 {
		t.Fatal("no helpers for " + runtime.GOOS)
	}
	for _, h := range hs {
		if h.name == "" || len(h.args) == 0 {
			t.Errorf("helper %+v", h)
		}
		joined := ""
		for _, a := range h.args {
			joined += a + " "
		}
		if !containsAny(joined, "24000", "rate=24000") {
			t.Errorf("%s does not name the 24 kHz rate: %s", h.name, joined)
		}
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func TestExecOpenerFeedsAProcessAndCancelKillsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses cat and sleep")
	}
	ctx := context.Background()
	w, err := execOpener("cat", nil)(ctx)
	if err != nil {
		t.Skip("no cat: " + err.Error())
	}
	if _, err := w.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	cctx, cancel := context.WithCancel(ctx)
	w, err = execOpener("sleep", []string{"30"})(cctx)
	if err != nil {
		t.Skip("no sleep: " + err.Error())
	}
	done := make(chan struct{})
	go func() { cancel(); _ = w.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop the helper")
	}
}
