package voice

import (
	"math"
	"sync/atomic"
)

// Level is how loud the last stretch of microphone audio was, in dBFS (0 is a
// full-scale signal, -100 is silence). RMS follows the average, Peak the
// loudest sample. A microphone that is muted, unplugged or pointed at the
// wrong input reads near -100 whatever you do.
type Level struct {
	RMS, Peak float64
}

// Silent is the Level of no audio at all.
var Silent = Level{RMS: floorDB, Peak: floorDB}

const floorDB = -100.0

// SpeechDB is the level, in dBFS, above which the engine treats audio as
// speech. It is the energy gate's absolute floor.
var SpeechDB = toDB(absFloor)

// chunkLevels returns the linear RMS and peak (0..1) of a chunk.
func chunkLevels(pcm []int16) (rms, peak float64) {
	if len(pcm) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range pcm {
		f := math.Abs(float64(v)) / 32768
		sum += f * f
		if f > peak {
			peak = f
		}
	}
	return math.Sqrt(sum / float64(len(pcm))), peak
}

func toDB(x float64) float64 {
	if x <= 1e-5 {
		return floorDB
	}
	return 20 * math.Log10(x)
}

// levelBox holds the latest Level for readers on other goroutines.
type levelBox struct{ v atomic.Pointer[Level] }

func (b *levelBox) set(l Level) { b.v.Store(&l) }

func (b *levelBox) get() Level {
	if p := b.v.Load(); p != nil {
		return *p
	}
	return Silent
}
