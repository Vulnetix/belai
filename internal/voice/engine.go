package voice

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// State is what the engine is doing, in the words the footer shows.
type State int32

// Engine states. Idle is push-to-talk armed with the microphone closed;
// Listening is the microphone open in always-listening mode.
const (
	StateOff State = iota
	StateLoading
	StatePaused
	StateIdle
	StateListening
	StateHearing
	StateTranscribing
)

func (s State) String() string {
	switch s {
	case StateOff:
		return "off"
	case StateLoading:
		return "loading"
	case StatePaused:
		return "paused"
	case StateIdle:
		return "ready"
	case StateListening:
		return "listening"
	case StateHearing:
		return "hearing"
	case StateTranscribing:
		return "transcribing"
	}
	return "unknown"
}

// Mode picks how speech is captured.
type Mode string

// Capture modes.
const (
	ModePushToTalk Mode = "push_to_talk"
	ModeListen     Mode = "listen"
)

// Recognizer turns 16 kHz mono float samples into text.
type Recognizer interface {
	Transcribe(ctx context.Context, pcm []float32) (string, error)
}

// EventKind tells the consumer what an Event carries.
type EventKind int

// Event kinds. State events are best effort (State always has the truth);
// transcripts and errors are never dropped.
const (
	EventState EventKind = iota
	EventTranscript
	EventError
)

// Event is one thing the engine reports.
type Event struct {
	Kind  EventKind
	State State
	Text  string // EventTranscript: the raw recognised text
	Err   error  // EventError
}

// Config wires the engine to its parts.
type Config struct {
	Source Source
	// Load returns the recognizer. It runs once, in the background, the first
	// time the engine is enabled.
	Load func(ctx context.Context) (Recognizer, error)
	Mode Mode
}

const (
	// pttTail keeps the microphone open for 300 ms after a key release so the
	// last syllable, still in flight from the helper, is not cut off.
	pttTail = 4800
	// maxPTT caps one held-key recording at 28 seconds.
	maxPTT     = 28 * 16000
	jobBacklog = 4
)

// Engine is the voice pipeline: microphone, segmentation, recognition. All of
// its state lives in one goroutine, so its methods are safe from any
// goroutine and never block on audio.
type Engine struct {
	cfg    Config
	cmds   chan func(*loop)
	events chan Event
	state  atomic.Int32
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// New starts an engine. It does nothing (no microphone, no model) until
// SetEnabled(true).
func New(parent context.Context, cfg Config) *Engine {
	if cfg.Mode == "" {
		cfg.Mode = ModePushToTalk
	}
	ctx, cancel := context.WithCancel(parent)
	e := &Engine{
		cfg:    cfg,
		cmds:   make(chan func(*loop)),
		events: make(chan Event, 16),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go e.run(ctx)
	return e
}

// Events delivers state changes, transcripts and errors.
func (e *Engine) Events() <-chan Event { return e.events }

// State is the current state.
func (e *Engine) State() State { return State(e.state.Load()) }

// SetEnabled turns listening on or off. Turning it on loads the model the
// first time and clears an earlier capture failure.
func (e *Engine) SetEnabled(on bool) { e.do(func(l *loop) { l.setEnabled(on) }) }

// SetReady tells the engine whether the composer can take text. Not ready
// closes the microphone and drops any audio in progress.
func (e *Engine) SetReady(ready bool) { e.do(func(l *loop) { l.setReady(ready) }) }

// SetMode switches between push-to-talk and always-listening.
func (e *Engine) SetMode(m Mode) { e.do(func(l *loop) { l.setMode(m) }) }

// PTTDown starts a push-to-talk recording.
func (e *Engine) PTTDown() { e.do(func(l *loop) { l.pttDown() }) }

// PTTUp ends a push-to-talk recording and sends it for recognition.
func (e *Engine) PTTUp() { e.do(func(l *loop) { l.pttUp() }) }

// Close stops the microphone and the engine and waits for both.
func (e *Engine) Close() {
	e.once.Do(e.cancel)
	<-e.done
}

func (e *Engine) do(f func(*loop)) {
	select {
	case e.cmds <- f:
	case <-e.done:
	}
}

func (e *Engine) emit(ctx context.Context, ev Event) {
	if ev.Kind == EventState {
		select {
		case e.events <- ev:
		default:
		}
		return
	}
	select {
	case e.events <- ev:
	case <-ctx.Done():
	}
}

type loaded struct {
	rec Recognizer
	err error
}

// loop is the run goroutine's state.
type loop struct {
	e   *Engine
	ctx context.Context

	enabled, ready, broken bool
	mode                   Mode
	ptt                    bool
	tail                   int // samples of post-release capture left
	pttBuf                 []float32

	rec     Recognizer
	loading bool
	loadCh  chan loaded

	stream    *Stream
	stopCap   context.CancelFunc
	seg       *Segmenter
	jobs      chan []float32
	doneCh    chan struct{}
	pending   int
	lastState State
}

func (e *Engine) run(ctx context.Context) {
	defer close(e.done)
	l := &loop{
		e: e, ctx: ctx, mode: e.cfg.Mode, seg: NewSegmenter(),
		loadCh: make(chan loaded, 1), doneCh: make(chan struct{}, jobBacklog+1),
		lastState: -1,
	}
	l.publish()
	for {
		var audio <-chan []int16
		if l.stream != nil {
			audio = l.stream.C
		}
		select {
		case <-ctx.Done():
			l.stopCapture()
			return
		case f := <-e.cmds:
			f(l)
		case pcm, ok := <-audio:
			if ok {
				l.audio(pcm)
			} else {
				l.captureEnded()
			}
		case r := <-l.loadCh:
			l.loadDone(r)
		case <-l.doneCh:
			l.pending--
		}
		l.reconcile()
		l.publish()
	}
}

func (l *loop) setEnabled(on bool) {
	l.enabled = on
	l.broken = false
	if !on {
		l.resetAudio()
		return
	}
	if l.rec == nil && !l.loading {
		l.loading = true
		go func() {
			r, err := l.e.cfg.Load(l.ctx)
			l.loadCh <- loaded{r, err}
		}()
	}
}

func (l *loop) setReady(ready bool) {
	l.ready = ready
	if !ready {
		l.resetAudio()
	}
}

func (l *loop) setMode(m Mode) {
	if m == l.mode {
		return
	}
	l.mode = m
	l.resetAudio()
}

func (l *loop) pttDown() {
	if l.mode != ModePushToTalk || !l.enabled || !l.ready || l.rec == nil || l.broken || l.ptt {
		return
	}
	l.ptt, l.tail, l.pttBuf = true, 0, nil
}

func (l *loop) pttUp() {
	if !l.ptt {
		return
	}
	l.ptt = false
	l.tail = pttTail
}

func (l *loop) resetAudio() {
	l.seg.Reset()
	l.ptt, l.tail, l.pttBuf = false, 0, nil
}

func (l *loop) loadDone(r loaded) {
	l.loading = false
	if r.err != nil {
		l.enabled = false
		l.e.emit(l.ctx, Event{Kind: EventError, Err: fmt.Errorf("voice model: %w", r.err)})
		return
	}
	l.rec = r.rec
	l.jobs = make(chan []float32, jobBacklog)
	go l.worker(r.rec, l.jobs)
}

// worker recognises queued clips one at a time, in order.
func (l *loop) worker(rec Recognizer, jobs <-chan []float32) {
	for {
		select {
		case <-l.ctx.Done():
			return
		case pcm := <-jobs:
			text, err := rec.Transcribe(l.ctx, pcm)
			switch {
			case l.ctx.Err() != nil:
				return
			case err != nil:
				l.e.emit(l.ctx, Event{Kind: EventError, Err: fmt.Errorf("transcribe: %w", err)})
			case text != "":
				l.e.emit(l.ctx, Event{Kind: EventTranscript, Text: text})
			}
			l.doneCh <- struct{}{}
		}
	}
}

func (l *loop) submit(pcm []float32) {
	if l.rec == nil || !HasSpeech(pcm) {
		return
	}
	select {
	case l.jobs <- pcm:
		l.pending++
	default:
		l.e.emit(l.ctx, Event{Kind: EventError, Err: errors.New("voice is behind; a phrase was dropped")})
	}
}

func (l *loop) audio(pcm []int16) {
	switch l.mode {
	case ModeListen:
		for _, s := range l.seg.Feed(pcm) {
			l.submit(s)
		}
	default:
		if !l.ptt && l.tail <= 0 {
			return
		}
		if len(l.pttBuf) < maxPTT {
			l.pttBuf = append(l.pttBuf, toFloat(pcm)...)
		}
		if !l.ptt {
			l.tail -= len(pcm)
			if l.tail <= 0 {
				buf := l.pttBuf
				l.pttBuf, l.tail = nil, 0
				l.submit(buf)
			}
		}
	}
}

func (l *loop) wantCapture() bool {
	if !l.enabled || !l.ready || l.rec == nil || l.broken {
		return false
	}
	return l.mode == ModeListen || l.ptt || l.tail > 0
}

func (l *loop) reconcile() {
	want := l.wantCapture()
	switch {
	case want && l.stream == nil:
		ctx, cancel := context.WithCancel(l.ctx)
		s, err := l.e.cfg.Source.Start(ctx)
		if err != nil {
			cancel()
			l.broken = true
			l.e.emit(l.ctx, Event{Kind: EventError, Err: err})
			return
		}
		l.stream, l.stopCap = &s, cancel
	case !want && l.stream != nil:
		l.stopCapture()
	}
}

func (l *loop) stopCapture() {
	if l.stream == nil {
		return
	}
	l.stopCap()
	// Drain so the reader goroutine can finish and reap the helper.
	for range l.stream.C {
	}
	l.stream, l.stopCap = nil, nil
}

func (l *loop) captureEnded() {
	err := l.stream.Err()
	l.stopCap()
	l.stream, l.stopCap = nil, nil
	l.resetAudio()
	if err != nil {
		l.broken = true
		l.e.emit(l.ctx, Event{Kind: EventError, Err: err})
	}
}

func (l *loop) state() State {
	switch {
	case !l.enabled, l.broken:
		return StateOff
	case l.loading:
		return StateLoading
	case !l.ready:
		return StatePaused
	case l.pending > 0:
		return StateTranscribing
	case l.mode == ModeListen && l.seg.InSpeech(), l.mode == ModePushToTalk && (l.ptt || l.tail > 0):
		return StateHearing
	case l.mode == ModeListen && l.stream != nil:
		return StateListening
	}
	return StateIdle
}

func (l *loop) publish() {
	s := l.state()
	if s == l.lastState {
		return
	}
	l.lastState = s
	l.e.state.Store(int32(s))
	l.e.emit(l.ctx, Event{Kind: EventState, State: s})
}
