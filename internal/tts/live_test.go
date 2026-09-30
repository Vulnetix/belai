//go:build live

package tts

import (
	"context"
	"testing"
	"time"
)

// TestLiveEdge talks to the real read-aloud service with a fixed phrase. It is
// not part of `just check`: run it with `go test -tags live -run TestLiveEdge
// ./internal/tts` to see whether the service still accepts the protocol.
func TestLiveEdge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pcm, err := NewEdge().Synthesize(ctx, "Hello from belai.", DefaultVoice)
	if err != nil {
		t.Fatalf("the service refused the request: %v", err)
	}
	// 24 kHz 16-bit mono: a short phrase is at least half a second of audio.
	if secs := float64(len(pcm)) / float64(SampleRate*BytesPerSample); secs < 0.5 || secs > 10 {
		t.Fatalf("%d bytes is %.1f s of audio", len(pcm), secs)
	}
	t.Logf("got %d bytes, %.1f s", len(pcm), float64(len(pcm))/float64(SampleRate*BytesPerSample))
}
