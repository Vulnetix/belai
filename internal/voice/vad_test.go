package voice

import (
	"math"
	"testing"
)

// tone returns n samples of a 300 Hz sine at the given amplitude (0..1).
func tone(n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * 32767 * math.Sin(2*math.Pi*300*float64(i)/16000))
	}
	return out
}

func silence(n int) []int16 { return make([]int16, n) }

func ms(n int) int { return n * 16 }

func TestSegmenterCutsAnUtteranceAtTheHangover(t *testing.T) {
	s := NewSegmenter()
	if got := s.Feed(silence(ms(300))); len(got) != 0 {
		t.Fatalf("segment from silence: %d", len(got))
	}
	if got := s.Feed(tone(ms(600), 0.3)); len(got) != 0 || !s.InSpeech() {
		t.Fatalf("speech open: segments=%d inSpeech=%v", len(got), s.InSpeech())
	}
	if got := s.Feed(silence(ms(400))); len(got) != 0 {
		t.Fatalf("a 400 ms pause split the utterance")
	}
	got := s.Feed(silence(ms(400)))
	if len(got) != 1 {
		t.Fatalf("segments after the hangover: %d, want 1", len(got))
	}
	if s.InSpeech() {
		t.Fatal("still in speech after the segment closed")
	}
	// 600 ms of speech, the pre-roll before it, and the hangover after it.
	if n := len(got[0]); n < ms(600) || n > ms(1800) {
		t.Fatalf("segment is %d samples", n)
	}
	if !HasSpeech(got[0]) {
		t.Fatal("a real segment failed the speech check")
	}
}

func TestSegmenterKeepsAPauseInsideASentence(t *testing.T) {
	s := NewSegmenter()
	var all [][]float32
	for _, chunk := range [][]int16{tone(ms(400), 0.3), silence(ms(300)), tone(ms(400), 0.3), silence(ms(900))} {
		all = append(all, s.Feed(chunk)...)
	}
	if len(all) != 1 {
		t.Fatalf("segments = %d, want 1 across a 300 ms pause", len(all))
	}
}

func TestSegmenterIgnoresQuietAndBriefNoise(t *testing.T) {
	s := NewSegmenter()
	if got := s.Feed(tone(ms(3000), 0.004)); len(got) != 0 || s.InSpeech() {
		t.Fatal("room noise below the floor opened a segment")
	}
	// A 40 ms click is shorter than the onset.
	s.Feed(tone(ms(40), 0.5))
	if got := s.Feed(silence(ms(1500))); len(got) != 0 {
		t.Fatal("a click became an utterance")
	}
	// Speech shorter than the minimum is dropped when it closes.
	s.Feed(tone(ms(100), 0.4))
	if got := s.Feed(silence(ms(1500))); len(got) != 0 {
		t.Fatal("100 ms of sound became an utterance")
	}
}

func TestSegmenterHandlesOddChunkSizes(t *testing.T) {
	s := NewSegmenter()
	audio := append(append(silence(ms(200)), tone(ms(500), 0.3)...), silence(ms(1000))...)
	var got [][]float32
	for i := 0; i < len(audio); i += 137 {
		end := min(i+137, len(audio))
		got = append(got, s.Feed(audio[i:end])...)
	}
	if len(got) != 1 {
		t.Fatalf("segments = %d, want 1 when fed in 137-sample chunks", len(got))
	}
}

func TestSegmenterCapsALongSegment(t *testing.T) {
	s := NewSegmenter()
	got := s.Feed(tone(16000*30, 0.3))
	if len(got) == 0 {
		t.Fatal("a 30 s monologue produced no segment")
	}
	if len(got[0]) > maxSegFrames*frameLen+frameLen {
		t.Fatalf("first segment is %d samples, over the cap", len(got[0]))
	}
}

func TestSegmenterFlushAndReset(t *testing.T) {
	s := NewSegmenter()
	s.Feed(tone(ms(600), 0.3))
	if out := s.Flush(); len(out) == 0 {
		t.Fatal("Flush dropped an open segment")
	}
	if s.InSpeech() {
		t.Fatal("Flush left the segment open")
	}
	if s.Flush() != nil {
		t.Fatal("second Flush returned audio")
	}
	s.Feed(tone(ms(600), 0.3))
	s.Reset()
	if s.InSpeech() || s.Flush() != nil {
		t.Fatal("Reset kept audio")
	}
}

func TestHasSpeech(t *testing.T) {
	if HasSpeech(toFloat(silence(ms(2000)))) {
		t.Fatal("silence has speech")
	}
	if HasSpeech(toFloat(tone(ms(2000), 0.003))) {
		t.Fatal("faint hiss has speech")
	}
	if HasSpeech(toFloat(tone(ms(60), 0.4))) {
		t.Fatal("60 ms has speech")
	}
	if !HasSpeech(toFloat(tone(ms(400), 0.3))) {
		t.Fatal("400 ms of tone has no speech")
	}
	if HasSpeech(nil) {
		t.Fatal("nil has speech")
	}
}

func TestRMS(t *testing.T) {
	if rms(nil) != 0 {
		t.Fatal("rms(nil)")
	}
	if got := rms([]float32{1, -1, 1, -1}); got != 1 {
		t.Fatalf("rms = %v", got)
	}
}
