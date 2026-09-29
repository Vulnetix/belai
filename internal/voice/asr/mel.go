package asr

import (
	"math"
	"sync"
)

const (
	fftSize = 400
	hopSize = 160
	fftBins = fftSize/2 + 1
)

// dftBasis builds the tables on first use.
var dftBasis = sync.OnceValues(dftTables)

// dftTables precomputes the Hann-windowed DFT basis for one 400-sample frame.
func dftTables() (cosT, sinT []float32) {
	cosT = make([]float32, fftSize*fftBins)
	sinT = make([]float32, fftSize*fftBins)
	for k := 0; k < fftBins; k++ {
		for i := 0; i < fftSize; i++ {
			win := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/fftSize)
			a := 2 * math.Pi * float64(k*i%fftSize) / fftSize
			cosT[k*fftSize+i] = float32(math.Cos(a) * win)
			sinT[k*fftSize+i] = float32(math.Sin(a) * win)
		}
	}
	return cosT, sinT
}

// mel returns channel-major log-mel features, [nMels][frames], with
// frames = len(samples)/hop. The audio is reflect-padded by half a frame, the
// log spectrum is floored 8 decades under its peak and scaled the way the
// model was trained.
func (m *Model) mel(samples []float32) ([]float32, int) {
	pad := fftSize / 2
	n := len(samples)
	p := make([]float32, n+2*pad)
	copy(p[pad:], samples)
	for i := 0; i < pad && i+1 < n; i++ {
		p[pad-1-i] = samples[i+1]
		p[pad+n+i] = samples[n-2-i]
	}
	dftCos, dftSin := dftBasis()
	T := n / hopSize
	nm := m.hp.nMels
	out := make([]float32, nm*T)
	parallel(T, func(lo, hi int) {
		pw := make([]float32, fftBins)
		for t := lo; t < hi; t++ {
			fr := p[t*hopSize : t*hopSize+fftSize]
			for k := 0; k < fftBins; k++ {
				re := dot(fr, dftCos[k*fftSize:(k+1)*fftSize])
				im := dot(fr, dftSin[k*fftSize:(k+1)*fftSize])
				pw[k] = re*re + im*im
			}
			for mm := 0; mm < nm; mm++ {
				v := dot(pw, m.filters[mm*m.nFFT:mm*m.nFFT+fftBins])
				if v < 1e-10 {
					v = 1e-10
				}
				out[mm*T+t] = float32(math.Log10(float64(v)))
			}
		}
	})
	mx := float32(math.Inf(-1))
	for _, v := range out {
		if v > mx {
			mx = v
		}
	}
	for i, v := range out {
		if v < mx-8 {
			v = mx - 8
		}
		out[i] = (v + 4) / 4
	}
	return out, T
}
