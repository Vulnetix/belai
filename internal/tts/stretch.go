package tts

import "math"

// Stretch returns in played at speed times as fast, keeping the pitch: a
// waveform-similarity overlap-add. Speed 1 returns the input unchanged. The
// speed is limited to 0.5 through 3.
func Stretch(in []int16, speed float64) []int16 {
	speed = math.Max(0.5, math.Min(3, speed))
	if speed == 1 || len(in) == 0 {
		return in
	}
	const (
		frame  = 960 // 40 ms
		hop    = frame / 2
		seek   = 240 // search this far either side of the nominal start
		step   = 4   // correlation stride
		search = 2   // candidate spacing
	)
	if len(in) < frame*2 {
		return naive(in, speed)
	}
	win := make([]float64, frame)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(frame))
	}
	outLen := int(float64(len(in)) / speed)
	out := make([]float64, outLen+frame)
	norm := make([]float64, outLen+frame)
	anaHop := float64(hop) * speed
	prev := 0
	for k := 0; ; k++ {
		start := 0
		if k > 0 {
			nominal := int(float64(k) * anaHop)
			target := prev + hop // what would naturally follow the last frame
			best, bestScore := nominal, math.Inf(-1)
			for d := -seek; d <= seek; d += search {
				c := nominal + d
				if c < 0 || c+frame > len(in) || target+hop > len(in) {
					continue
				}
				var dot, e float64
				for i := 0; i < hop; i += step {
					a, b := float64(in[c+i]), float64(in[target+i])
					dot += a * b
					e += a * a
				}
				if s := dot / math.Sqrt(e+1); s > bestScore {
					best, bestScore = c, s
				}
			}
			start = best
		}
		pos := k * hop
		if start+frame > len(in) || pos+frame > len(out) {
			break
		}
		for i := 0; i < frame; i++ {
			out[pos+i] += win[i] * float64(in[start+i])
			norm[pos+i] += win[i]
		}
		prev = start
	}
	res := make([]int16, outLen)
	for i := range res {
		v := out[i]
		if norm[i] > 1e-3 {
			v /= norm[i]
		}
		res[i] = int16(math.Max(-32768, math.Min(32767, math.Round(v))))
	}
	return res
}

// naive resamples very short input, where overlap-add has nothing to work with.
func naive(in []int16, speed float64) []int16 {
	n := int(float64(len(in)) / speed)
	out := make([]int16, n)
	for i := range out {
		out[i] = in[min(int(float64(i)*speed), len(in)-1)]
	}
	return out
}
