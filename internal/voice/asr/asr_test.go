package asr

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tiny model dimensions: small enough to build in memory, valid enough for
// validate and bind.
const (
	tState  = 8
	tHeads  = 2
	tMels   = 8
	tAudCtx = 80
	tTxtCtx = 16
)

type ggmlBuilder struct {
	buf   bytes.Buffer
	rng   *rand.Rand
	skip  string // tensor name to leave out
	vocab int
}

func (g *ggmlBuilder) i32(v int) { _ = binary.Write(&g.buf, binary.LittleEndian, int32(v)) }

func (g *ggmlBuilder) header(vocab int) {
	g.i32(ggmlMagic)
	for _, v := range []int{vocab, tAudCtx, tState, tHeads, 1, tTxtCtx, tState, tHeads, 1, tMels, 1} {
		g.i32(v)
	}
	g.i32(tMels)
	g.i32(201)
	for i := 0; i < tMels*201; i++ {
		_ = binary.Write(&g.buf, binary.LittleEndian, float32(g.rng.Float64()*0.01))
	}
	g.i32(5)
	for _, tok := range []string{"a", " hello", " world", ".", "!"} {
		g.i32(len(tok))
		g.buf.WriteString(tok)
	}
}

func (g *ggmlBuilder) tensor(name string, typ int, dims ...int) {
	if name == g.skip {
		return
	}
	n := 1
	for _, d := range dims {
		n *= d
	}
	g.i32(len(dims))
	g.i32(len(name))
	g.i32(typ)
	for _, d := range dims {
		g.i32(d)
	}
	g.buf.WriteString(name)
	switch typ {
	case typeF32:
		for i := 0; i < n; i++ {
			v := float32(g.rng.NormFloat64() * 0.05)
			if strings.HasSuffix(name, "ln.weight") || strings.HasSuffix(name, "_ln.weight") || strings.HasSuffix(name, "ln_post.weight") {
				v = 1
			}
			_ = binary.Write(&g.buf, binary.LittleEndian, v)
		}
	case typeF16:
		for i := 0; i < n; i++ {
			_ = binary.Write(&g.buf, binary.LittleEndian, uint16(0x2800+g.rng.Intn(0x200)))
		}
	case typeQ5_1:
		for blk := 0; blk < n/32; blk++ {
			var q [24]byte
			g.rng.Read(q[:])
			binary.LittleEndian.PutUint16(q[0:], 0x2c00) // scale
			binary.LittleEndian.PutUint16(q[2:], 0xa800) // minimum
			g.buf.Write(q[:])
		}
	}
}

func (g *ggmlBuilder) lin(prefix string, in, out int, bias bool, typ int) {
	g.tensor(prefix+".weight", typ, in, out)
	if bias {
		g.tensor(prefix+".bias", typeF32, out)
	}
}

func (g *ggmlBuilder) norm(prefix string) {
	g.tensor(prefix+".weight", typeF32, tState)
	g.tensor(prefix+".bias", typeF32, tState)
}

// tinyModel returns the bytes of a synthetic English-only ggml model.
func tinyModel(skip string, vocab int) []byte {
	g := &ggmlBuilder{rng: rand.New(rand.NewSource(1)), skip: skip}
	g.header(vocab)
	d := tState
	g.tensor("encoder.conv1.weight", typeF16, 3, tMels, d)
	g.tensor("encoder.conv1.bias", typeF32, d)
	g.tensor("encoder.conv2.weight", typeF16, 3, d, d)
	g.tensor("encoder.conv2.bias", typeF32, d)
	g.tensor("encoder.positional_embedding", typeF32, d, tAudCtx)
	p := "encoder.blocks.0."
	g.norm(p + "attn_ln")
	g.lin(p+"attn.query", d, d, true, typeQ5_1)
	g.lin(p+"attn.key", d, d, false, typeQ5_1)
	g.lin(p+"attn.value", d, d, true, typeQ5_1)
	g.lin(p+"attn.out", d, d, true, typeQ5_1)
	g.norm(p + "mlp_ln")
	g.lin(p+"mlp.0", d, 4*d, true, typeQ5_1)
	g.lin(p+"mlp.2", 4*d, d, true, typeQ5_1)
	g.norm("encoder.ln_post")
	g.tensor("decoder.token_embedding.weight", typeQ5_1, d, englishVocab)
	g.tensor("decoder.positional_embedding", typeF32, d, tTxtCtx)
	p = "decoder.blocks.0."
	g.norm(p + "attn_ln")
	g.lin(p+"attn.query", d, d, true, typeF32)
	g.lin(p+"attn.key", d, d, false, typeF32)
	g.lin(p+"attn.value", d, d, true, typeF32)
	g.lin(p+"attn.out", d, d, true, typeF32)
	g.norm(p + "cross_attn_ln")
	g.lin(p+"cross_attn.query", d, d, true, typeF32)
	g.lin(p+"cross_attn.key", d, d, false, typeF32)
	g.lin(p+"cross_attn.value", d, d, true, typeF32)
	g.lin(p+"cross_attn.out", d, d, true, typeF32)
	g.norm(p + "mlp_ln")
	g.lin(p+"mlp.0", d, 4*d, true, typeF32)
	g.lin(p+"mlp.2", 4*d, d, true, typeF32)
	g.norm("decoder.ln")
	return g.buf.Bytes()
}

func TestParseTinyModel(t *testing.T) {
	m, err := parse(tinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	if m.hp.nAudioState != tState || len(m.enc.blocks) != 1 || len(m.dec.blocks) != 1 || len(m.vocab) != 5 {
		t.Fatalf("unexpected model: %v", m)
	}
	if !strings.Contains(m.String(), "vocab=51864") {
		t.Fatalf("String() = %q", m.String())
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	good := tinyModel("", englishVocab)
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
	if _, err := parse(tinyModel("", 51865)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("multilingual vocab: err = %v, want ErrUnsupported", err)
	}
	// A missing tensor names itself.
	_, err := parse(tinyModel("decoder.ln.bias", englishVocab))
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "decoder.ln.bias") {
		t.Errorf("missing tensor: err = %v", err)
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.bin")
	if err := os.WriteFile(path, tinyModel("", englishVocab), 0o600); err != nil {
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
	m, err := parse(tinyModel("", englishVocab))
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
	m, err := parse(tinyModel("", englishVocab))
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
	m, err := parse(tinyModel("", englishVocab))
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]float32, 3200)
	for i := range pcm {
		pcm[i] = float32(math.Sin(2 * math.Pi * 440 * float64(i) / SampleRate))
	}
	mel, frames := m.mel(pcm)
	if frames != 20 || len(mel) != tMels*20 {
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
