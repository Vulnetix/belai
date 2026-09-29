package asr

import (
	"context"
	"fmt"
	"math"
)

type encBlock struct {
	attnLn, mlpLn norm
	q, k, v, o    lin
	m0, m2        lin
}

type encoder struct {
	conv1, conv2 conv1d
	pos          []float32
	blocks       []encBlock
	post         norm
}

type decBlock struct {
	attnLn, crossLn, mlpLn norm
	q, k, v, o             lin
	cq, ck, cv, co         lin
	m0, m2                 lin
}

type decoder struct {
	emb    []float32
	pos    []float32
	blocks []decBlock
	ln     norm
}

// binder looks tensors up by name and shape and remembers the first failure,
// so a wrong file is reported once with the tensor that did not fit.
type binder struct {
	t   map[string]*tensor
	err error
}

func (b *binder) get(name string, n int) []float32 {
	if b.err != nil {
		return nil
	}
	t := b.t[name]
	if t == nil {
		b.err = fmt.Errorf("%w: missing tensor %s", ErrUnsupported, name)
		return nil
	}
	if len(t.data) != n {
		b.err = fmt.Errorf("%w: tensor %s has %d values, want %d", ErrUnsupported, name, len(t.data), n)
		return nil
	}
	return t.data
}

func (b *binder) lin(prefix string, in, out int, bias bool) lin {
	l := lin{in: in, out: out, w: b.get(prefix+".weight", in*out)}
	if bias {
		l.b = b.get(prefix+".bias", out)
	}
	return l
}

func (b *binder) norm(prefix string, d int) norm {
	return norm{w: b.get(prefix+".weight", d), b: b.get(prefix+".bias", d)}
}

func (b *binder) conv(prefix string, in, out int) conv1d {
	return conv1d{in: in, out: out, w: b.get(prefix+".weight", out*in*3), b: b.get(prefix+".bias", out)}
}

func (m *Model) bind(tensors map[string]*tensor) error {
	b := &binder{t: tensors}
	hp := m.hp
	d := hp.nAudioState
	m.enc.conv1 = b.conv("encoder.conv1", hp.nMels, d)
	m.enc.conv2 = b.conv("encoder.conv2", d, d)
	m.enc.pos = b.get("encoder.positional_embedding", hp.nAudioCtx*d)
	for l := 0; l < hp.nAudioLayer; l++ {
		p := fmt.Sprintf("encoder.blocks.%d.", l)
		m.enc.blocks = append(m.enc.blocks, encBlock{
			attnLn: b.norm(p+"attn_ln", d),
			q:      b.lin(p+"attn.query", d, d, true),
			k:      b.lin(p+"attn.key", d, d, false),
			v:      b.lin(p+"attn.value", d, d, true),
			o:      b.lin(p+"attn.out", d, d, true),
			mlpLn:  b.norm(p+"mlp_ln", d),
			m0:     b.lin(p+"mlp.0", d, 4*d, true),
			m2:     b.lin(p+"mlp.2", 4*d, d, true),
		})
	}
	m.enc.post = b.norm("encoder.ln_post", d)
	m.dec.emb = b.get("decoder.token_embedding.weight", hp.nVocab*d)
	m.dec.pos = b.get("decoder.positional_embedding", hp.nTextCtx*d)
	for l := 0; l < hp.nTextLayer; l++ {
		p := fmt.Sprintf("decoder.blocks.%d.", l)
		m.dec.blocks = append(m.dec.blocks, decBlock{
			attnLn:  b.norm(p+"attn_ln", d),
			q:       b.lin(p+"attn.query", d, d, true),
			k:       b.lin(p+"attn.key", d, d, false),
			v:       b.lin(p+"attn.value", d, d, true),
			o:       b.lin(p+"attn.out", d, d, true),
			crossLn: b.norm(p+"cross_attn_ln", d),
			cq:      b.lin(p+"cross_attn.query", d, d, true),
			ck:      b.lin(p+"cross_attn.key", d, d, false),
			cv:      b.lin(p+"cross_attn.value", d, d, true),
			co:      b.lin(p+"cross_attn.out", d, d, true),
			mlpLn:   b.norm(p+"mlp_ln", d),
			m0:      b.lin(p+"mlp.0", d, 4*d, true),
			m2:      b.lin(p+"mlp.2", 4*d, d, true),
		})
	}
	m.dec.ln = b.norm("decoder.ln", d)
	return b.err
}

// encode turns log-mel features into the encoder states, [te][state].
func (m *Model) encode(ctx context.Context, mel []float32, frames int) ([]float32, int, error) {
	d := m.hp.nAudioState
	x, t1 := m.enc.conv1.apply(mel, frames, 1)
	gelu(x)
	x, te := m.enc.conv2.apply(x, t1, 2)
	gelu(x)
	if te < 1 || te > m.hp.nAudioCtx {
		return nil, 0, fmt.Errorf("asr: %d encoder frames outside 1..%d", te, m.hp.nAudioCtx)
	}
	h := make([]float32, te*d)
	for t := 0; t < te; t++ {
		for c := 0; c < d; c++ {
			h[t*d+c] = x[c*te+t] + m.enc.pos[t*d+c]
		}
	}
	for _, blk := range m.enc.blocks {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		n := blk.attnLn.apply(h, te, d)
		a := attention(blk.q.apply(n, te), te, blk.k.apply(n, te), blk.v.apply(n, te), te, d, m.hp.nAudioHead)
		addInto(h, blk.o.apply(a, te))
		n = blk.mlpLn.apply(h, te, d)
		f := blk.m0.apply(n, te)
		gelu(f)
		addInto(h, blk.m2.apply(f, te))
	}
	return m.enc.post.apply(h, te, d), te, nil
}

// decode runs greedy decoding against the encoder states and returns the
// text tokens, without the end marker.
func (m *Model) decode(ctx context.Context, enc []float32, te, budget int) ([]int, error) {
	d := m.hp.nTextState
	nl := len(m.dec.blocks)
	ck := make([][]float32, nl)
	cv := make([][]float32, nl)
	for l, blk := range m.dec.blocks {
		ck[l] = blk.ck.apply(enc, te)
		cv[l] = blk.cv.apply(enc, te)
	}
	kc := make([][]float32, nl)
	vc := make([][]float32, nl)
	step := func(tok, pos int) []float32 {
		x := make([]float32, d)
		for i := range x {
			x[i] = m.dec.emb[tok*d+i] + m.dec.pos[pos*d+i]
		}
		for l, blk := range m.dec.blocks {
			n := blk.attnLn.apply(x, 1, d)
			kc[l] = append(kc[l], blk.k.apply(n, 1)...)
			vc[l] = append(vc[l], blk.v.apply(n, 1)...)
			a := attention(blk.q.apply(n, 1), 1, kc[l], vc[l], pos+1, d, m.hp.nTextHead)
			addInto(x, blk.o.apply(a, 1))
			n = blk.crossLn.apply(x, 1, d)
			a = attention(blk.cq.apply(n, 1), 1, ck[l], cv[l], te, d, m.hp.nTextHead)
			addInto(x, blk.co.apply(a, 1))
			n = blk.mlpLn.apply(x, 1, d)
			f := blk.m0.apply(n, 1)
			gelu(f)
			addInto(x, blk.m2.apply(f, 1))
		}
		x = m.dec.ln.apply(x, 1, d)
		logits := make([]float32, m.hp.nVocab)
		parallel(m.hp.nVocab, func(lo, hi int) {
			for v := lo; v < hi; v++ {
				logits[v] = dot(x, m.dec.emb[v*d:(v+1)*d])
			}
		})
		return logits
	}
	prompt := []int{tokSOT, tokNoTimestamps}
	var logits []float32
	for i, t := range prompt {
		logits = step(t, i)
	}
	pos := len(prompt)
	limit := m.hp.nTextCtx - pos
	if budget > limit {
		budget = limit
	}
	var out []int
	for len(out) < budget {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for v := tokSOT; v < m.hp.nVocab; v++ {
			logits[v] = float32(math.Inf(-1))
		}
		if len(out) == 0 {
			logits[tokEOT] = float32(math.Inf(-1))
			logits[tokSpace] = float32(math.Inf(-1))
		}
		best := 0
		for v := range logits {
			if logits[v] > logits[best] {
				best = v
			}
		}
		if best == tokEOT {
			break
		}
		out = append(out, best)
		if n := repeatedTail(out); n > 0 {
			out = out[:len(out)-n]
			break
		}
		logits = step(best, pos)
		pos++
	}
	return out, nil
}

// repeatedTail reports how many trailing tokens are a needless repeat: the
// same n-gram seen four times running for one token, or three times running
// for two to six. It returns the number to drop so one copy remains, or 0.
func repeatedTail(toks []int) int {
	for n := 1; n <= 6; n++ {
		reps := 3
		if n == 1 {
			reps = 4
		}
		if len(toks) < n*reps {
			continue
		}
		same := true
		for r := 1; r < reps && same; r++ {
			for i := 0; i < n; i++ {
				if toks[len(toks)-1-i] != toks[len(toks)-1-i-r*n] {
					same = false
					break
				}
			}
		}
		if same {
			return n * (reps - 1)
		}
	}
	return 0
}
