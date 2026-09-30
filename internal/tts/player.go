package tts

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"time"
)

// State is what a Player is doing.
type State int

// Player states.
const (
	Idle      State = iota // nothing started yet
	Buffering              // started, waiting for audio
	Playing
	Paused
	Stopped // stopped by the user; Play starts again from the top
	Ended
	Failed
)

func (s State) String() string {
	return [...]string{"idle", "buffering", "playing", "paused", "stopped", "ended", "failed"}[s]
}

// Active reports whether sound is playing or about to.
func (s State) Active() bool { return s == Buffering || s == Playing }

const (
	// Levels is how many recent level readings a Snapshot carries.
	Levels = 24
	// outputLag is how far sound trails what has been written: the writer stays
	// this far ahead of the clock, plus the device's own buffer.
	outputLag = 250 * time.Millisecond
	lead      = 150 * time.Millisecond
	// chunkSamples is one write: 50 ms.
	chunkSamples = SampleRate / 20
	// maxSegment bounds how much is time-stretched at once.
	maxSegment = 10 * SampleRate
)

// Snapshot is a consistent view of a Player for drawing.
type Snapshot struct {
	State  State
	Pos    time.Duration // what is being heard now, in the clip's own time
	Dur    time.Duration // audio available so far
	Final  bool          // Dur will not grow
	Speed  float64
	Levels []float64 // recent loudness, 0 to 1, oldest first
	Err    error
}

// Player plays 24 kHz 16-bit mono PCM through an Opener, with seek, pause and
// speed. Audio can still be arriving while it plays (Append, then Finish): it
// waits when it catches up. The player paces its own writes, so the position
// is exact, and pause and seek are instant: stopping kills the helper and a
// seek starts a new one at the new offset.
type Player struct {
	// Pace is the playback clock: 1 is real time. Zero writes as fast as the
	// writer takes it, for tests.
	Pace float64

	open Opener

	mu     sync.Mutex
	pcm    []int16
	final  bool
	err    error
	speed  float64
	state  State
	pos    int // samples, in the clip's own time
	gen    int
	cancel context.CancelFunc
	levels [Levels]float64
	wg     sync.WaitGroup
}

// NewPlayer returns a player that opens each playback with open.
func NewPlayer(open Opener) *Player {
	return &Player{open: open, speed: 1, Pace: 1}
}

// Append adds audio. b is little-endian 16-bit samples.
func (p *Player) Append(b []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := 0; i+1 < len(b); i += 2 {
		p.pcm = append(p.pcm, int16(binary.LittleEndian.Uint16(b[i:])))
	}
}

// Finish says no more audio is coming. A non-nil err is a failed synthesis:
// what arrived still plays, then the state is Failed.
func (p *Player) Finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.final, p.err = true, err
	if err != nil && len(p.pcm) == 0 && p.state != Stopped {
		p.stopLocked()
		p.state = Failed
	}
}

// Play starts from the top when nothing was started, the clip ended or it was
// stopped, and resumes from the position when paused.
func (p *Player) Play() {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.state {
	case Buffering, Playing, Failed:
		return
	case Ended, Stopped:
		p.pos = 0 // Idle keeps a position set by Seek
	}
	p.startLocked()
}

// Pause stops the sound at once and keeps the position.
func (p *Player) Pause() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Active() {
		p.pos = p.heardLocked()
		p.stopLocked()
		p.state = Paused
	}
}

// Toggle pauses what plays and plays what does not.
func (p *Player) Toggle() {
	p.mu.Lock()
	active := p.state.Active()
	p.mu.Unlock()
	if active {
		p.Pause()
	} else {
		p.Play()
	}
}

// Stop stops the sound; Play then starts again from the top.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state == Failed || p.state == Ended {
		return
	}
	p.stopLocked()
	p.state = Stopped
}

// Seek moves to d. Playing continues from there; a finished or stopped clip
// waits, paused, at the new position.
func (p *Player) Seek(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pos = max(0, min(int(d.Seconds()*SampleRate), len(p.pcm)))
	switch {
	case p.state.Active():
		p.startLocked()
	case p.state == Ended || p.state == Stopped:
		p.state = Paused
	}
}

// SetSpeed changes the speed, from 0.5 to 3. The pitch stays.
func (p *Player) SetSpeed(f float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f = math.Max(0.5, math.Min(3, f))
	if f == p.speed {
		return
	}
	p.speed = f
	if p.state.Active() {
		p.pos = p.heardLocked()
		p.startLocked()
	}
}

// Close stops playback and waits for it to end.
func (p *Player) Close() {
	p.mu.Lock()
	p.stopLocked()
	if p.state.Active() {
		p.state = Stopped
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// Snapshot returns the current state.
func (p *Player) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.pos
	if p.state.Active() {
		pos = p.heardLocked()
	}
	lv := make([]float64, Levels)
	if p.state == Playing {
		copy(lv, p.levels[:])
	}
	return Snapshot{
		State: p.state, Pos: samplesToDur(pos), Dur: samplesToDur(len(p.pcm)),
		Final: p.final, Speed: p.speed, Levels: lv, Err: p.err,
	}
}

// heardLocked is the position of what is audible now: the write position less
// the time the sound trails the writes, in the clip's own time.
func (p *Player) heardLocked() int {
	if p.Pace <= 0 {
		return p.pos
	}
	return max(0, p.pos-int(outputLag.Seconds()*SampleRate*p.speed))
}

func samplesToDur(n int) time.Duration {
	return time.Duration(float64(n) / SampleRate * float64(time.Second))
}

func (p *Player) stopLocked() {
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	p.gen++
	p.levels = [Levels]float64{}
}

func (p *Player) startLocked() {
	p.stopLocked()
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.state = Buffering
	p.wg.Add(1)
	go p.feed(ctx, p.gen, p.pos)
}

// current reports whether the playback gen is still the live one, with the
// lock held.
func (p *Player) current(gen int) bool { return gen == p.gen }

func (p *Player) fail(gen int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current(gen) {
		p.err, p.state = err, Failed
	}
}

// sleep waits d, or until ctx ends. It reports whether the wait completed.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (p *Player) feed(ctx context.Context, gen, from int) {
	defer p.wg.Done()
	w, err := p.open(ctx)
	if err != nil {
		p.fail(gen, err)
		return
	}
	clean := false
	defer func() {
		if !clean {
			_ = w.Close() // killed by ctx: reap the helper
		}
	}()
	cur := from
	start := time.Now()
	var sent time.Duration
	for ctx.Err() == nil {
		p.mu.Lock()
		if !p.current(gen) {
			p.mu.Unlock()
			return
		}
		avail, fin, speed, ferr := len(p.pcm), p.final, p.speed, p.err
		if cur >= avail {
			if fin {
				p.mu.Unlock()
				clean = true
				werr := w.Close() // let the helper play out what it holds
				p.mu.Lock()
				if p.current(gen) {
					p.pos = len(p.pcm)
					switch {
					case ferr != nil:
						p.err, p.state = ferr, Failed
					case werr != nil && ctx.Err() == nil:
						p.err, p.state = werr, Failed
					default:
						p.state = Ended
					}
				}
				p.mu.Unlock()
				return
			}
			p.state = Buffering
			p.mu.Unlock()
			if !sleep(ctx, 25*time.Millisecond) {
				return
			}
			continue
		}
		seg := append([]int16(nil), p.pcm[cur:min(avail, cur+maxSegment)]...)
		p.mu.Unlock()

		out := Stretch(seg, speed)
		for off := 0; off < len(out); off += chunkSamples {
			end := min(off+chunkSamples, len(out))
			if p.Pace > 0 {
				ahead := time.Duration(float64(sent)/p.Pace) - lead - time.Since(start)
				if !sleep(ctx, ahead) {
					return
				}
			}
			if _, err := w.Write(pcmBytes(out[off:end])); err != nil {
				if ctx.Err() == nil {
					p.fail(gen, err)
				}
				return
			}
			sent += samplesToDur(end - off)
			lv := level(out[off:end])
			p.mu.Lock()
			if !p.current(gen) {
				p.mu.Unlock()
				return
			}
			p.state = Playing
			p.pos = cur + off*len(seg)/len(out)
			copy(p.levels[:], p.levels[1:])
			p.levels[Levels-1] = lv
			p.mu.Unlock()
		}
		cur += len(seg)
	}
}

func pcmBytes(s []int16) []byte {
	b := make([]byte, 2*len(s))
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	}
	return b
}

// level is the loudness of s from 0 to 1: its RMS, scaled so ordinary speech
// reads around the middle.
func level(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		f := float64(v) / 32768
		sum += f * f
	}
	return math.Min(1, math.Sqrt(sum/float64(len(s)))*4)
}
