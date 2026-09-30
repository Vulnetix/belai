package tts

import (
	"encoding/binary"
	"math"
	"os"
	"testing"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/hello.mp3")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeMP3GivesMonoPCMAtTheEngineRate(t *testing.T) {
	pcm, err := decodeMP3(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm)%BytesPerSample != 0 {
		t.Fatalf("%d bytes is not whole samples", len(pcm))
	}
	secs := float64(len(pcm)) / (SampleRate * BytesPerSample)
	if secs < 1.5 || secs > 2.5 {
		t.Fatalf("%.2f s decoded from a 1.9 s clip", secs)
	}
	var sum float64
	var peak int
	for i := 0; i+1 < len(pcm); i += 2 {
		v := int(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += float64(v) * float64(v)
		if a := int(math.Abs(float64(v))); a > peak {
			peak = a
		}
	}
	if rms := math.Sqrt(sum / float64(len(pcm)/2)); rms < 100 || peak < 1000 {
		t.Fatalf("decoded audio is silent: rms %.0f peak %d", rms, peak)
	}
}

func TestDecodeMP3RefusesWhatIsNotAudio(t *testing.T) {
	good := fixture(t)
	cases := map[string][]byte{
		"empty":      nil,
		"text":       []byte("this is not an mp3 file at all, just words"),
		"zeros":      make([]byte, 4096),
		"json error": []byte(`{"error":"blocked"}`),
	}
	for name, data := range cases {
		if pcm, err := decodeMP3(data); err == nil && len(pcm) > 0 {
			t.Errorf("%s decoded to %d bytes", name, len(pcm))
		}
	}
	_ = good
}

func TestDecodeMP3SurvivesCorruptionWithoutPanicking(t *testing.T) {
	good := fixture(t)
	for _, damage := range []func([]byte){
		func(b []byte) { // flip bytes through the whole stream
			for i := 0; i < len(b); i += 7 {
				b[i] ^= 0xFF
			}
		},
		func(b []byte) { // smash the frame headers near the start
			for i := 0; i < 64 && i < len(b); i++ {
				b[i] = 0xFF
			}
		},
		func(b []byte) { // truncate by zeroing the tail
			for i := len(b) / 2; i < len(b); i++ {
				b[i] = 0
			}
		},
	} {
		data := append([]byte(nil), good...)
		damage(data)
		// Must return (an error or some audio) rather than panic or hang.
		_, _ = decodeMP3(data)
	}
	for n := 0; n < len(good); n += len(good)/17 + 1 {
		_, _ = decodeMP3(good[:n]) // every truncation
	}
}

func TestDecodeMP3HandlesTrailingGarbageAndPadding(t *testing.T) {
	good := fixture(t)
	want, err := decodeMP3(good)
	if err != nil {
		t.Fatal(err)
	}
	padded := append(append([]byte(nil), good...), make([]byte, 100)...)
	got, err := decodeMP3(padded)
	if err == nil && len(got) < len(want) {
		t.Fatalf("padding lost audio: %d of %d bytes", len(got), len(want))
	}
}

func TestResampleChangesLengthByTheRatio(t *testing.T) {
	in := make([]int16, 4800)
	for i := range in {
		in[i] = int16(i % 1000)
	}
	if got := resample(in, 48000, 24000); len(got) != 2400 {
		t.Fatalf("48k to 24k gave %d samples", len(got))
	}
	if got := resample(in, 12000, 24000); len(got) != 9600 {
		t.Fatalf("12k to 24k gave %d samples", len(got))
	}
	if got := resample(in, 24000, 24000); len(got) != len(in) || &got[0] != &in[0] {
		t.Fatal("the same rate must return the input")
	}
	if got := resample(nil, 48000, 24000); len(got) != 0 {
		t.Fatal("nil in, samples out")
	}
}
