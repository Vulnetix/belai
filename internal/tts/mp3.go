package tts

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/hajimehoshi/go-mp3"
)

// maxDecodedBytes bounds the decoded audio of one piece: about twenty minutes
// of 24 kHz mono, far past the 12,000 characters one message may read.
const maxDecodedBytes = 64 << 20

// decodeMP3 turns the service's MP3 into 24 kHz 16-bit mono PCM. The bytes come
// off the network, so the decoder is treated as untrusted-input code: a panic
// becomes an error, the output is bounded, and anything that is not audio at a
// rate it can use is refused, never guessed at.
func decodeMP3(data []byte) (pcm []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			pcm, err = nil, fmt.Errorf("tts: could not decode the audio: %v", r)
		}
	}()
	dec, err := mp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("tts: could not decode the audio: %w", err)
	}
	// The decoder always returns 16-bit stereo, at the rate of the stream.
	stereo, err := io.ReadAll(io.LimitReader(dec, 2*maxDecodedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("tts: could not decode the audio: %w", err)
	}
	if len(stereo) > 2*maxDecodedBytes {
		return nil, errors.New("tts: decoded audio too large")
	}
	mono := make([]int16, len(stereo)/4)
	for i := range mono {
		l := int(int16(binary.LittleEndian.Uint16(stereo[4*i:])))
		r := int(int16(binary.LittleEndian.Uint16(stereo[4*i+2:])))
		mono[i] = int16((l + r) / 2)
	}
	if rate := dec.SampleRate(); rate != SampleRate {
		if rate < 8000 || rate > 96000 {
			return nil, fmt.Errorf("tts: the audio has a sample rate of %d Hz", rate)
		}
		mono = resample(mono, rate, SampleRate)
	}
	if len(mono) == 0 {
		return nil, errors.New("tts: no audio bytes were received")
	}
	out := make([]byte, 2*len(mono))
	for i, v := range mono {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
	}
	return out, nil
}

// resample converts from one rate to another by linear interpolation.
func resample(in []int16, from, to int) []int16 {
	if len(in) == 0 || from == to {
		return in
	}
	n := int(int64(len(in)) * int64(to) / int64(from))
	out := make([]int16, n)
	for i := range out {
		pos := float64(i) * float64(from) / float64(to)
		j := int(pos)
		frac := pos - float64(j)
		a := float64(in[min(j, len(in)-1)])
		b := float64(in[min(j+1, len(in)-1)])
		out[i] = int16(a + (b-a)*frac)
	}
	return out
}
