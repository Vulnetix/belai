// Package tts reads text aloud: it prepares a message for speech, synthesises
// it with an Engine, keeps the audio in a local cache and plays it through a
// helper program, with position, seek and speed.
//
// The only engine is Edge's read-aloud service (edge.go), which is an
// unofficial Microsoft endpoint: the text leaves the machine, so the TUI asks
// once before first use and the feature is off until the user turns it on. The
// Engine interface keeps the player, the cache and the card independent of it,
// so a local model can replace it without touching them.
//
// Audio is 24 kHz, 16-bit, mono PCM throughout. Nothing here reaches a model,
// the session record, telemetry or sync. The cache is the one place audio
// touches disk, under the user's cache directory with hashed file names.
package tts

import "context"

// Audio format of every engine and of the cache.
const (
	SampleRate     = 24000
	BytesPerSample = 2
)

// Engine turns one piece of prepared text into PCM. A piece is already small
// enough for one request (see Prepare).
type Engine interface {
	// Name identifies the engine in cache keys.
	Name() string
	// Synthesize returns 24 kHz 16-bit mono little-endian PCM for text.
	Synthesize(ctx context.Context, text, voice string) ([]byte, error)
}
