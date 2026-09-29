package asr

import (
	"math"
	"runtime"
	"sync"
)

// lin is a dense layer: w is [out][in] row-major, b may be nil.
type lin struct {
	w       []float32
	b       []float32
	in, out int
}

type norm struct{ w, b []float32 }

type conv1d struct {
	w       []float32 // [out][in][3]
	b       []float32
	in, out int
}

// parallel splits [0, n) across the available cores.
func parallel(n int, f func(lo, hi int)) {
	w := runtime.GOMAXPROCS(0)
	if w > n {
		w = n
	}
	if w < 1 {
		return
	}
	chunk := (n + w - 1) / w
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += chunk {
		hi := lo + chunk
		if hi > n {
			hi = n
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			f(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
}

func dot(a, b []float32) float32 {
	n := len(a)
	b = b[:n]
	var s0, s1, s2, s3 float32
	i := 0
	for ; i+4 <= n; i += 4 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
	}
	for ; i < n; i++ {
		s0 += a[i] * b[i]
	}
	return s0 + s1 + s2 + s3
}

// apply computes y = x·Wᵀ + b for T rows of x. Many rows split by row; one
// row (the decoder's step) splits by output unit.
func (l lin) apply(x []float32, T int) []float32 {
	y := make([]float32, T*l.out)
	row := func(t, lo, hi int) {
		xr := x[t*l.in : (t+1)*l.in]
		yr := y[t*l.out : (t+1)*l.out]
		for o := lo; o < hi; o++ {
			v := dot(xr, l.w[o*l.in:(o+1)*l.in])
			if l.b != nil {
				v += l.b[o]
			}
			yr[o] = v
		}
	}
	if T >= runtime.GOMAXPROCS(0) {
		parallel(T, func(lo, hi int) {
			for t := lo; t < hi; t++ {
				row(t, 0, l.out)
			}
		})
		return y
	}
	for t := 0; t < T; t++ {
		parallel(l.out, func(lo, hi int) { row(t, lo, hi) })
	}
	return y
}

func (n norm) apply(x []float32, T, d int) []float32 {
	y := make([]float32, len(x))
	for t := 0; t < T; t++ {
		xr := x[t*d : (t+1)*d]
		var mean float32
		for _, v := range xr {
			mean += v
		}
		mean /= float32(d)
		var vr float32
		for _, v := range xr {
			vr += (v - mean) * (v - mean)
		}
		inv := float32(1 / math.Sqrt(float64(vr/float32(d))+1e-5))
		for i, v := range xr {
			y[t*d+i] = (v-mean)*inv*n.w[i] + n.b[i]
		}
	}
	return y
}

func gelu(x []float32) {
	for i, v := range x {
		x[i] = 0.5 * v * (1 + float32(math.Erf(float64(v)/math.Sqrt2)))
	}
}

func addInto(dst, src []float32) {
	for i := range dst {
		dst[i] += src[i]
	}
}

// apply runs a kernel-3, pad-1 convolution over c.in channels of T samples
// (channel-major), returning c.out channels of the strided length.
func (c conv1d) apply(in []float32, T, stride int) ([]float32, int) {
	To := (T+2-3)/stride + 1
	out := make([]float32, c.out*To)
	parallel(c.out, func(lo, hi int) {
		for o := lo; o < hi; o++ {
			ws := c.w[o*c.in*3 : (o+1)*c.in*3]
			for t := 0; t < To; t++ {
				s := c.b[o]
				for ch := 0; ch < c.in; ch++ {
					for k := 0; k < 3; k++ {
						ti := t*stride + k - 1
						if ti >= 0 && ti < T {
							s += ws[ch*3+k] * in[ch*T+ti]
						}
					}
				}
				out[o*To+t] = s
			}
		}
	})
	return out, To
}

// attention is multi-head scaled dot-product attention with no mask: the
// encoder sees the whole clip and the decoder's key cache holds only the past.
func attention(q []float32, Tq int, k, v []float32, Tk, d, heads int) []float32 {
	dh := d / heads
	scale := float32(1 / math.Sqrt(float64(dh)))
	out := make([]float32, Tq*d)
	parallel(Tq*heads, func(lo, hi int) {
		sc := make([]float32, Tk)
		for idx := lo; idx < hi; idx++ {
			t, h := idx/heads, idx%heads
			qh := q[t*d+h*dh : t*d+(h+1)*dh]
			maxv := float32(math.Inf(-1))
			for j := 0; j < Tk; j++ {
				s := dot(qh, k[j*d+h*dh:j*d+(h+1)*dh]) * scale
				sc[j] = s
				if s > maxv {
					maxv = s
				}
			}
			var sum float32
			for j := 0; j < Tk; j++ {
				e := float32(math.Exp(float64(sc[j] - maxv)))
				sc[j] = e
				sum += e
			}
			inv := 1 / sum
			o := out[t*d+h*dh : t*d+(h+1)*dh]
			for j := 0; j < Tk; j++ {
				w := sc[j] * inv
				vj := v[j*d+h*dh : j*d+(h+1)*dh]
				for i := range o {
					o[i] += w * vj[i]
				}
			}
		}
	})
	return out
}
