package tts

import (
	"math"
	"testing"
)

func sine(hz float64, seconds float64) []int16 {
	n := int(seconds * SampleRate)
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(12000 * math.Sin(2*math.Pi*hz*float64(i)/SampleRate))
	}
	return out
}

func crossings(s []int16) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			n++
		}
	}
	return n
}

func TestStretchSpeedOneIsIdentity(t *testing.T) {
	in := sine(440, 0.2)
	if out := Stretch(in, 1); &out[0] != &in[0] {
		t.Fatal("speed 1 copied or changed the input")
	}
}

func TestStretchShortensAndLengthensByTheRatio(t *testing.T) {
	in := sine(220, 3)
	for _, speed := range []float64{0.75, 1.25, 1.5, 2} {
		out := Stretch(in, speed)
		want := float64(len(in)) / speed
		if got := float64(len(out)); math.Abs(got-want) > 1 {
			t.Errorf("speed %v: %d samples, want %.0f", speed, len(out), want)
		}
	}
}

func TestStretchKeepsThePitch(t *testing.T) {
	in := sine(220, 3)
	base := float64(crossings(in)) / float64(len(in))
	for _, speed := range []float64{0.75, 1.5, 2} {
		out := Stretch(in, speed)
		got := float64(crossings(out)) / float64(len(out))
		if math.Abs(got-base)/base > 0.08 {
			t.Errorf("speed %v moved the pitch: %.5f crossings per sample, was %.5f", speed, got, base)
		}
	}
}

func TestStretchDoesNotClipOrGoSilent(t *testing.T) {
	out := Stretch(sine(330, 2), 1.5)
	var peak int
	var energy float64
	for _, v := range out {
		if a := int(math.Abs(float64(v))); a > peak {
			peak = a
		}
		energy += float64(v) * float64(v)
	}
	if peak > 13000 || peak < 9000 || energy == 0 {
		t.Fatalf("peak %d (input peaks at 12000): overlap-add changed the level", peak)
	}
}

func TestStretchHandlesShortAndEmptyInput(t *testing.T) {
	if out := Stretch(nil, 2); len(out) != 0 {
		t.Fatal("empty input")
	}
	short := sine(440, 0.01)
	if out := Stretch(short, 2); len(out) == 0 || len(out) > len(short) {
		t.Fatalf("short input gave %d samples from %d", len(out), len(short))
	}
	if out := Stretch(sine(440, 1), 99); len(out) == 0 {
		t.Fatal("an out-of-range speed was not clamped")
	}
}
