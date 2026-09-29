package asr

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
)

const ggmlMagic = 0x67676d6c

// ggml tensor storage types this loader reads.
const (
	typeF32  = 0
	typeF16  = 1
	typeQ5_1 = 7
)

// maxTensorElems bounds one tensor so a corrupt header cannot ask for a
// huge allocation.
const maxTensorElems = 1 << 28

type hparams struct {
	nVocab, nAudioCtx, nAudioState, nAudioHead, nAudioLayer int
	nTextCtx, nTextState, nTextHead, nTextLayer, nMels      int
	ftype                                                   int
}

type tensor struct {
	dims []int
	data []float32
}

type reader struct {
	b   []byte
	p   int
	err error
}

func (r *reader) i32() int {
	if r.err != nil || r.p+4 > len(r.b) {
		r.fail("truncated file")
		return 0
	}
	v := int32(binary.LittleEndian.Uint32(r.b[r.p:]))
	r.p += 4
	return int(v)
}

func (r *reader) take(n int) []byte {
	if r.err != nil || n < 0 || r.p+n > len(r.b) {
		r.fail("truncated file")
		return nil
	}
	s := r.b[r.p : r.p+n]
	r.p += n
	return s
}

func (r *reader) fail(msg string) {
	if r.err == nil {
		r.err = fmt.Errorf("ggml: %s", msg)
	}
}

func loadFile(path string) (*Model, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(b)
}

func parse(b []byte) (*Model, error) {
	r := &reader{b: b}
	if uint32(r.i32()) != ggmlMagic {
		return nil, fmt.Errorf("ggml: not a ggml file")
	}
	m := &Model{}
	hp := &m.hp
	hp.nVocab, hp.nAudioCtx, hp.nAudioState = r.i32(), r.i32(), r.i32()
	hp.nAudioHead, hp.nAudioLayer = r.i32(), r.i32()
	hp.nTextCtx, hp.nTextState, hp.nTextHead, hp.nTextLayer = r.i32(), r.i32(), r.i32(), r.i32()
	hp.nMels, hp.ftype = r.i32(), r.i32()
	nMel, nFFT := r.i32(), r.i32()
	if r.err != nil {
		return nil, r.err
	}
	if err := hp.validate(nMel, nFFT); err != nil {
		return nil, err
	}
	m.nFFT = nFFT
	m.filters = make([]float32, nMel*nFFT)
	raw := r.take(len(m.filters) * 4)
	if r.err != nil {
		return nil, r.err
	}
	for i := range m.filters {
		m.filters[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	nv := r.i32()
	if nv < 0 || nv > hp.nVocab {
		return nil, fmt.Errorf("ggml: bad vocabulary size %d", nv)
	}
	m.vocab = make([][]byte, 0, nv)
	for i := 0; i < nv && r.err == nil; i++ {
		l := r.i32()
		if l < 0 || l > 1024 {
			return nil, fmt.Errorf("ggml: bad token length %d", l)
		}
		m.vocab = append(m.vocab, append([]byte(nil), r.take(l)...))
	}
	if r.err != nil {
		return nil, r.err
	}
	tensors := map[string]*tensor{}
	for r.p < len(b) {
		name, t, err := readTensor(r)
		if err != nil {
			return nil, err
		}
		tensors[name] = t
	}
	if err := m.bind(tensors); err != nil {
		return nil, err
	}
	return m, nil
}

func (hp *hparams) validate(nMel, nFFT int) error {
	if hp.nVocab != englishVocab {
		return fmt.Errorf("%w: vocabulary %d (only English-only models, %d, are supported)", ErrUnsupported, hp.nVocab, englishVocab)
	}
	if hp.nAudioState <= 0 || hp.nAudioState != hp.nTextState || hp.nAudioState > 2048 {
		return fmt.Errorf("%w: state width %d/%d", ErrUnsupported, hp.nAudioState, hp.nTextState)
	}
	if hp.nAudioHead <= 0 || hp.nAudioState%hp.nAudioHead != 0 || hp.nTextHead <= 0 || hp.nTextState%hp.nTextHead != 0 {
		return fmt.Errorf("%w: head count", ErrUnsupported)
	}
	if hp.nAudioLayer <= 0 || hp.nAudioLayer > 64 || hp.nTextLayer <= 0 || hp.nTextLayer > 64 {
		return fmt.Errorf("%w: layer count", ErrUnsupported)
	}
	if hp.nAudioCtx <= 0 || hp.nAudioCtx > 1500 || hp.nTextCtx <= 2 || hp.nTextCtx > 448 {
		return fmt.Errorf("%w: context length", ErrUnsupported)
	}
	if hp.nMels <= 0 || hp.nMels > 128 || nMel != hp.nMels || nFFT != 201 {
		return fmt.Errorf("%w: mel filter bank %dx%d", ErrUnsupported, nMel, nFFT)
	}
	return nil
}

func readTensor(r *reader) (string, *tensor, error) {
	nd, nl, tt := r.i32(), r.i32(), r.i32()
	if r.err != nil {
		return "", nil, r.err
	}
	if nd < 1 || nd > 4 || nl < 1 || nl > 256 {
		return "", nil, fmt.Errorf("ggml: bad tensor header")
	}
	dims := make([]int, nd)
	n := 1
	for i := range dims {
		dims[i] = r.i32()
		if dims[i] < 1 || n > maxTensorElems/dims[i] {
			return "", nil, fmt.Errorf("ggml: bad tensor shape")
		}
		n *= dims[i]
	}
	name := string(r.take(nl))
	out, err := decodeTensor(r, tt, n)
	if err != nil {
		return "", nil, fmt.Errorf("tensor %s: %w", name, err)
	}
	return name, &tensor{dims: dims, data: out}, nil
}

func decodeTensor(r *reader, tt, n int) ([]float32, error) {
	out := make([]float32, n)
	switch tt {
	case typeF32:
		raw := r.take(n * 4)
		if r.err != nil {
			return nil, r.err
		}
		for i := range out {
			out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
		}
	case typeF16:
		raw := r.take(n * 2)
		if r.err != nil {
			return nil, r.err
		}
		for i := range out {
			out[i] = float16(binary.LittleEndian.Uint16(raw[i*2:]))
		}
	case typeQ5_1:
		if n%32 != 0 {
			return nil, fmt.Errorf("q5_1 size %d is not a multiple of 32", n)
		}
		raw := r.take(n / 32 * 24)
		if r.err != nil {
			return nil, r.err
		}
		for blk := 0; blk < n/32; blk++ {
			dequantQ51(raw[blk*24:blk*24+24], out[blk*32:blk*32+32])
		}
	default:
		return nil, fmt.Errorf("%w: storage type %d", ErrUnsupported, tt)
	}
	return out, nil
}

// float16 widens an IEEE half to float32.
func float16(h uint16) float32 {
	sign := uint32(h>>15) & 1
	exp := int(h>>10) & 0x1f
	man := uint32(h & 0x3ff)
	switch exp {
	case 0:
		v := float32(man) * float32(math.Ldexp(1, -24))
		if sign == 1 {
			v = -v
		}
		return v
	case 31:
		return math.Float32frombits(sign<<31 | 0xff<<23 | man<<13)
	}
	return math.Float32frombits(sign<<31 | uint32(exp-15+127)<<23 | man<<13)
}

// dequantQ51 expands one 24-byte q5_1 block (scale, minimum, 5th bits, 32
// nibbles) into 32 floats.
func dequantQ51(q []byte, y []float32) {
	d := float16(binary.LittleEndian.Uint16(q))
	mn := float16(binary.LittleEndian.Uint16(q[2:]))
	qh := binary.LittleEndian.Uint32(q[4:])
	qs := q[8:24]
	for j := 0; j < 16; j++ {
		xh0 := byte((qh >> uint(j)) << 4 & 0x10)
		xh1 := byte((qh >> uint(j+12)) & 0x10)
		y[j] = float32((qs[j]&0xf)|xh0)*d + mn
		y[j+16] = float32((qs[j]>>4)|xh1)*d + mn
	}
}
