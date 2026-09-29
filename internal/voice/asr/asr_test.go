package asr

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/voice/asr/asrtest"
)

func TestParseTinyModel(t *testing.T) {
	m, err := parse(asrtest.TinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	if m.hp.nAudioState != asrtest.State || len(m.enc.blocks) != 1 || len(m.dec.blocks) != 1 || len(m.vocab) != 5 {
		t.Fatalf("unexpected model: %v", m)
	}
	if !strings.Contains(m.String(), "vocab=51864") {
		t.Fatalf("String() = %q", m.String())
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	good := asrtest.TinyModel("", englishVocab)
	cases := map[string][]byte{
		"empty":       nil,
		"bad magic":   append([]byte{1, 2, 3, 4}, good[4:]...),
		"truncated":   good[:len(good)/2],
		"header only": good[:20],
	}
	for name, b := range cases {
		if _, err := parse(b); err == nil {
			t.Errorf("%s: parse accepted a bad file", name)
		}
	}
	// A multilingual vocabulary is valid ggml but not a model this decoder
	// understands.
	if _, err := parse(asrtest.TinyModel("", 51865)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("multilingual vocab: err = %v, want ErrUnsupported", err)
	}
	// A missing tensor names itself.
	_, err := parse(asrtest.TinyModel("decoder.ln.bias", englishVocab))
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "decoder.ln.bias") {
		t.Errorf("missing tensor: err = %v", err)
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.bin")
	if err := os.WriteFile(path, asrtest.TinyModel("", englishVocab), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "absent.bin")); err == nil {
		t.Fatal("Load of a missing file succeeded")
	}
}

func TestTranscribeIsDeterministicAndBounded(t *testing.T) {
	m, err := parse(asrtest.TinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, SampleRate) // one second
	for i := range pcm {
		pcm[i] = float32(math.Sin(2*math.Pi*220*float64(i)/SampleRate)) * 0.3
	}
	a, err := m.Transcribe(context.Background(), pcm)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Transcribe(context.Background(), pcm)
	if err != nil || a != b {
		t.Fatalf("transcription is not deterministic: %q vs %q (%v)", a, b, err)
	}
	if out, err := m.Transcribe(context.Background(), nil); err != nil || out != "" {
		t.Fatalf("empty audio = %q, %v", out, err)
	}
}

func TestTranscribeHonoursCancellation(t *testing.T) {
	m, err := parse(asrtest.TinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Transcribe(ctx, make([]float32, SampleRate)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestFloat16(t *testing.T) {
	cases := map[uint16]float32{0x3c00: 1, 0xc000: -2, 0x0000: 0, 0x3800: 0.5}
	for h, want := range cases {
		if got := float16(h); got != want {
			t.Errorf("float16(%#x) = %v, want %v", h, got, want)
		}
	}
	if got := float16(0x0001); got != float32(math.Ldexp(1, -24)) {
		t.Errorf("subnormal = %v", got)
	}
	if got := float16(0x8001); got >= 0 {
		t.Errorf("negative subnormal = %v", got)
	}
	if got := float16(0x7c00); !math.IsInf(float64(got), 1) {
		t.Errorf("inf = %v", got)
	}
}

func TestDequantQ51(t *testing.T) {
	var q [24]byte
	binary.LittleEndian.PutUint16(q[0:], 0x3c00) // d = 1
	binary.LittleEndian.PutUint16(q[2:], 0x4000) // m = 2
	binary.LittleEndian.PutUint32(q[4:], 0x00010001)
	q[8] = 0x53 // low nibble 3, high nibble 5
	y := make([]float32, 32)
	dequantQ51(q[:], y)
	// value 0: nibble 3 with 5th bit (qh bit 0) set -> 3+16, times 1, plus 2.
	if y[0] != 21 {
		t.Errorf("y[0] = %v, want 21", y[0])
	}
	// value 16: nibble 5 with 5th bit (qh bit 16) set -> 5+16, plus 2.
	if y[16] != 23 {
		t.Errorf("y[16] = %v, want 23", y[16])
	}
	// every other value is the minimum, since the nibbles are zero.
	if y[1] != 2 || y[17] != 2 {
		t.Errorf("y[1], y[17] = %v, %v, want 2, 2", y[1], y[17])
	}
}

func TestMelShapeAndRange(t *testing.T) {
	m, err := parse(asrtest.TinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 3200)
	for i := range pcm {
		pcm[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / SampleRate))
	}
	mel, frames := m.mel(pcm)
	if frames != 20 || len(mel) != asrtest.Mels*20 {
		t.Fatalf("frames = %d, len = %d", frames, len(mel))
	}
	for _, v := range mel {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			t.Fatal("non-finite mel value")
		}
	}
}

func TestTokenBudgetScalesWithAudio(t *testing.T) {
	if tokenBudget(0.5) >= tokenBudget(10) {
		t.Fatal("budget must grow with the audio")
	}
	if got := tokenBudget(3); got != 20 {
		t.Fatalf("tokenBudget(3) = %d, want 20", got)
	}
}

func TestRepeatedTail(t *testing.T) {
	cases := []struct {
		name string
		toks []int
		want int
	}{
		{"no repeat", []int{1, 2, 3, 4, 5}, 0},
		{"one token three times is speech", []int{9, 7, 7, 7}, 0},
		{"one token four times", []int{9, 7, 7, 7, 7}, 3},
		{"pair three times", []int{9, 1, 2, 1, 2, 1, 2}, 4},
		{"pair twice is speech", []int{9, 1, 2, 1, 2}, 0},
		{"triple three times", []int{5, 1, 2, 3, 1, 2, 3, 1, 2, 3}, 6},
		{"short", []int{1}, 0},
	}
	for _, c := range cases {
		if got := repeatedTail(c.toks); got != c.want {
			t.Errorf("%s: repeatedTail = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestTidy(t *testing.T) {
	cases := map[string]string{
		"  hello world  ":       "hello world",
		"":                      "",
		"(wind blowing)":        "",
		"[BLANK_AUDIO]":         "",
		"*music*":               "",
		"♪ ♪":                   "",
		"you":                   "",
		"You.":                  "",
		"Thank you.":            "",
		"thank you for the fix": "thank you for the fix",
		"open the (main) file":  "open the (main) file",
		"(sighs) run the tests": "(sighs) run the tests",
	}
	for in, want := range cases {
		if got := tidy(in); got != want {
			t.Errorf("tidy(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRealModel runs the shipped weights when they are already on disk. It is
// skipped in CI, which never downloads them.
func TestRealModel(t *testing.T) {
	path := os.Getenv("BELAI_VOICE_MODEL")
	wav := os.Getenv("BELAI_VOICE_WAV")
	if path == "" || wav == "" {
		t.Skip("set BELAI_VOICE_MODEL and BELAI_VOICE_WAV to run against real weights")
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, (len(b)-44)/2)
	for i := range pcm {
		pcm[i] = float32(int16(binary.LittleEndian.Uint16(b[44+i*2:]))) / 32768
	}
	got, err := m.Transcribe(context.Background(), pcm)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(got), "ask not what your country can do for you") {
		t.Fatalf("transcript = %q", got)
	}
}

func TestLoadBytesMatchesLoad(t *testing.T) {
	b := asrtest.TinyModel("", englishVocab)
	m, err := LoadBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.hp.nAudioState != asrtest.State {
		t.Fatalf("model = %v", m)
	}
	if _, err := LoadBytes(b[:len(b)/3]); err == nil {
		t.Fatal("LoadBytes accepted a truncated model")
	}
	if _, err := LoadBytes(nil); err == nil {
		t.Fatal("LoadBytes accepted nothing")
	}
}
