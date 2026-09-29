package voice

import "math"

// Segmenter tunables. Frame counts are 20 ms frames so behaviour depends only
// on how much audio has been fed, never on a clock.
const (
	frameLen       = 320 // 20 ms at 16 kHz
	onsetFrames    = 3   // 60 ms of speech opens a segment
	hangoverFrames = 35  // 700 ms of quiet closes it
	preRollFrames  = 15  // 300 ms kept from before the onset
	minSpeech      = 10  // 200 ms of speech at least, or the segment is noise
	maxSegFrames   = 1250
	absFloor       = 0.012 // about -38 dBFS; never treat quieter audio as speech
	noiseRatio     = 3.0   // speech must be this far above the tracked noise floor
)

// Segmenter cuts a continuous 16 kHz mono stream into utterances with an
// energy gate: an adaptive noise floor, a short onset, a pre-roll so the first
// syllable is kept, and a hangover so a pause inside a sentence does not split
// it. It holds audio in memory only.
type Segmenter struct {
	carry    []int16
	floor    float64
	inSeg    bool
	run      int // consecutive speech frames while idle
	quiet    int // consecutive quiet frames while in a segment
	speech   int // speech frames in the current segment
	seg      []float32
	pre      [][]float32
	finished [][]float32
}

// NewSegmenter returns a Segmenter with an empty noise floor.
func NewSegmenter() *Segmenter { return &Segmenter{floor: absFloor / noiseRatio} }

// InSpeech reports whether a segment is open.
func (s *Segmenter) InSpeech() bool { return s.inSeg }

// Reset drops all buffered audio, including an open segment.
func (s *Segmenter) Reset() {
	s.carry, s.seg, s.pre, s.finished = nil, nil, nil, nil
	s.inSeg, s.run, s.quiet, s.speech = false, 0, 0, 0
}

// Feed adds samples and returns the utterances that ended within them.
func (s *Segmenter) Feed(pcm []int16) [][]float32 {
	s.carry = append(s.carry, pcm...)
	for len(s.carry) >= frameLen {
		s.frame(toFloat(s.carry[:frameLen]))
		s.carry = s.carry[frameLen:]
	}
	if len(s.carry) == 0 {
		s.carry = nil
	}
	out := s.finished
	s.finished = nil
	return out
}

// Flush closes an open segment and returns it (nil when there is nothing
// worth transcribing).
func (s *Segmenter) Flush() []float32 {
	var out []float32
	if s.inSeg && s.speech >= minSpeech {
		out = s.seg
	}
	s.Reset()
	return out
}

func (s *Segmenter) frame(f []float32) {
	e := rms(f)
	speech := e > absFloor && e > s.floor*noiseRatio
	if !speech {
		// The floor follows quiet audio down quickly and up slowly, so a
		// steady hum becomes the floor but a spoken word does not.
		if e < s.floor {
			s.floor = e*0.5 + s.floor*0.5
		} else {
			s.floor = s.floor*0.995 + e*0.005
		}
	}
	if !s.inSeg {
		s.pre = append(s.pre, f)
		if len(s.pre) > preRollFrames {
			s.pre = s.pre[1:]
		}
		if !speech {
			s.run = 0
			return
		}
		s.run++
		if s.run < onsetFrames {
			return
		}
		s.inSeg, s.quiet, s.speech = true, 0, s.run
		for _, p := range s.pre {
			s.seg = append(s.seg, p...)
		}
		s.pre = nil
		return
	}
	s.seg = append(s.seg, f...)
	if speech {
		s.quiet = 0
		s.speech++
	} else {
		s.quiet++
	}
	if s.quiet >= hangoverFrames || len(s.seg) >= maxSegFrames*frameLen {
		if s.speech >= minSpeech {
			s.finished = append(s.finished, s.seg)
		}
		s.seg, s.inSeg, s.run, s.quiet, s.speech = nil, false, 0, 0, 0
	}
}

// HasSpeech reports whether pcm holds at least 200 ms of speech-level audio.
// The engine applies it to every clip before recognition, since the model
// invents words for silence.
func HasSpeech(pcm []float32) bool {
	loud := 0
	for i := 0; i+frameLen <= len(pcm); i += frameLen {
		if rms(pcm[i:i+frameLen]) > absFloor {
			loud++
		}
	}
	return loud >= minSpeech
}

func toFloat(in []int16) []float32 {
	out := make([]float32, len(in))
	for i, v := range in {
		out[i] = float32(v) / 32768
	}
	return out
}

func rms(f []float32) float64 {
	if len(f) == 0 {
		return 0
	}
	var sum float64
	for _, v := range f {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(f)))
}
