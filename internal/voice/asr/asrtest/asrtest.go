// Package asrtest builds a tiny synthetic Whisper model in the ggml format,
// for tests in any package that need a model file without downloading the
// real one. The model has valid structure and English-only dimensions but
// random weights, so it transcribes nothing meaningful. Nothing in the
// program imports this package.
package asrtest

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"strings"
)

// Dimensions of the synthetic model.
const (
	State    = 8
	Heads    = 2
	Mels     = 8
	AudioCtx = 80
	TextCtx  = 16
)

const (
	ggmlMagic    = 0x67676d6c
	englishVocab = 51864
	typeF32      = 0
	typeF16      = 1
	typeQ5_1     = 7
)

// EnglishVocab is the vocabulary size of an English-only model.
const EnglishVocab = englishVocab

type ggmlBuilder struct {
	buf   bytes.Buffer
	rng   *rand.Rand
	skip  string // tensor name to leave out
	vocab int
}

func (g *ggmlBuilder) i32(v int) { _ = binary.Write(&g.buf, binary.LittleEndian, int32(v)) }

func (g *ggmlBuilder) header(vocab int) {
	g.i32(ggmlMagic)
	for _, v := range []int{vocab, AudioCtx, State, Heads, 1, TextCtx, State, Heads, 1, Mels, 1} {
		g.i32(v)
	}
	g.i32(Mels)
	g.i32(201)
	for i := 0; i < Mels*201; i++ {
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
	g.tensor(prefix+".weight", typeF32, State)
	g.tensor(prefix+".bias", typeF32, State)
}

// TinyModel returns the bytes of a synthetic English-only ggml model.
func TinyModel(skip string, vocab int) []byte {
	g := &ggmlBuilder{rng: rand.New(rand.NewSource(1)), skip: skip}
	g.header(vocab)
	d := State
	g.tensor("encoder.conv1.weight", typeF16, 3, Mels, d)
	g.tensor("encoder.conv1.bias", typeF32, d)
	g.tensor("encoder.conv2.weight", typeF16, 3, d, d)
	g.tensor("encoder.conv2.bias", typeF32, d)
	g.tensor("encoder.positional_embedding", typeF32, d, AudioCtx)
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
	g.tensor("decoder.positional_embedding", typeF32, d, TextCtx)
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
