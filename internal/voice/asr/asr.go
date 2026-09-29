// Package asr is a pure-Go speech recogniser for OpenAI's Whisper tiny.en,
// read from the ggml file format (float32, float16 and q5_1 tensors). It
// links no C code, so the release stays a single cross-compiled binary.
//
// A Model is immutable after Load and safe for concurrent Transcribe calls.
// Input is 16 kHz mono float32 in [-1, 1]. The encoder runs on a window sized
// to the audio (rather than always 30 seconds), which keeps a short dictated
// phrase to a fraction of a second on a laptop.
//
// The decoder is greedy with the guards a dictation feature needs: a token
// budget tied to the audio length, a stop on a repeated n-gram, and removal of
// output that is only a sound annotation such as "(wind blowing)".
package asr

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// SampleRate is the only input rate the model accepts.
const SampleRate = 16000

const (
	maxWindowSamples = 30 * SampleRate
	minWindowSamples = SampleRate
	tailSamples      = SampleRate / 2
	frameSamples     = 320 // one encoder frame: 2 mel hops of 160 samples
)

// Special token ids of the English-only vocabulary.
const (
	tokEOT          = 50256
	tokSOT          = 50257
	tokNoTimestamps = 50362
	tokSpace        = 220
	englishVocab    = 51864
)

// Model is a loaded Whisper tiny.en.
type Model struct {
	hp      hparams
	filters []float32
	nFFT    int
	vocab   [][]byte
	enc     encoder
	dec     decoder
}

// ErrUnsupported reports a model file that is valid ggml but not an
// English-only Whisper the decoder understands.
var ErrUnsupported = errors.New("unsupported whisper model")

// Load reads and validates a ggml Whisper file.
func Load(path string) (*Model, error) {
	return loadFile(path)
}

// Transcribe returns the recognised text for pcm. Audio longer than the
// model's 30 second window is transcribed in consecutive windows and joined.
// An empty string means the audio held no speech.
func (m *Model) Transcribe(ctx context.Context, pcm []float32) (string, error) {
	if len(pcm) == 0 {
		return "", nil
	}
	var parts []string
	for start := 0; start < len(pcm); start += maxWindowSamples {
		end := start + maxWindowSamples
		if end > len(pcm) {
			end = len(pcm)
		}
		text, err := m.transcribeWindow(ctx, pcm[start:end])
		if err != nil {
			return "", err
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " "), nil
}

func (m *Model) transcribeWindow(ctx context.Context, pcm []float32) (string, error) {
	seconds := float64(len(pcm)) / SampleRate
	s := make([]float32, 0, len(pcm)+tailSamples+frameSamples)
	s = append(s, pcm...)
	s = append(s, make([]float32, tailSamples)...)
	if len(s) < minWindowSamples {
		s = append(s, make([]float32, minWindowSamples-len(s))...)
	}
	s = s[:len(s)/frameSamples*frameSamples]
	if len(s) > maxWindowSamples {
		s = s[:maxWindowSamples]
	}
	mel, frames := m.mel(s)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	enc, te, err := m.encode(ctx, mel, frames)
	if err != nil {
		return "", err
	}
	toks, err := m.decode(ctx, enc, te, tokenBudget(seconds))
	if err != nil {
		return "", err
	}
	return tidy(m.detokenize(toks)), nil
}

// tokenBudget bounds the decoder by the audio length: speech runs at about
// three to four tokens a second, so four plus a small floor never clips it.
func tokenBudget(seconds float64) int {
	return int(seconds*4) + 8
}

func (m *Model) detokenize(toks []int) string {
	var sb strings.Builder
	for _, t := range toks {
		if t >= 0 && t < len(m.vocab) {
			sb.Write(m.vocab[t])
		}
	}
	return sb.String()
}

func (m *Model) String() string {
	return fmt.Sprintf("whisper(vocab=%d state=%d layers=%d)", m.hp.nVocab, m.hp.nAudioState, m.hp.nAudioLayer)
}
