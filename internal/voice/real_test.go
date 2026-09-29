package voice

import (
	"context"
	"encoding/binary"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/voice/asr"
)

// wavSource plays a recording into the engine as a microphone would: silence,
// the clip in 100 ms chunks, then silence.
type wavSource struct{ pcm []int16 }

func (w wavSource) Start(ctx context.Context) (Stream, error) {
	ch := make(chan []int16, 8)
	go func() {
		defer close(ch)
		send := func(c []int16) bool {
			select {
			case ch <- c:
				return true
			case <-ctx.Done():
				return false
			}
		}
		all := append(append(make([]int16, 16000), w.pcm...), make([]int16, 16000*2)...)
		for i := 0; i < len(all); i += 1600 {
			if !send(all[i:min(i+1600, len(all))]) {
				return
			}
		}
		<-ctx.Done()
	}()
	return Stream{C: ch, Err: func() error { return nil }}, nil
}

// TestEngineWithRealSpeech runs a real recording through the whole pipeline:
// segmenter, gate and Whisper. It is skipped unless the weights and a 16 kHz
// mono 16-bit WAV are supplied, since CI never downloads the model.
func TestEngineWithRealSpeech(t *testing.T) {
	model, wav := os.Getenv("BELAI_VOICE_MODEL"), os.Getenv("BELAI_VOICE_WAV")
	if model == "" || wav == "" {
		t.Skip("set BELAI_VOICE_MODEL and BELAI_VOICE_WAV to run against real weights")
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	pcm := make([]int16, (len(b)-44)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(b[44+i*2:]))
	}
	e := New(context.Background(), Config{
		Source: wavSource{pcm: pcm}, Mode: ModeListen,
		Load: func(context.Context) (Recognizer, error) { return asr.Load(model) },
	})
	defer e.Close()
	e.SetReady(true)
	e.SetEnabled(true)
	timeout := time.After(60 * time.Second)
	var got []string
	for {
		select {
		case ev := <-e.Events():
			switch ev.Kind {
			case EventError:
				t.Fatalf("engine error: %v", ev.Err)
			case EventTranscript:
				got = append(got, ev.Text)
				// The recording has a long dramatic pause that ends an utterance, and a
				// two-second fragment may recognise as nothing, so the check is that the
				// first and last phrases both came through the pipeline.
				if joined := strings.ToLower(strings.Join(got, " ")); strings.Contains(joined, "ask what you can do for your country") {
					if !strings.Contains(joined, "fellow americans") {
						t.Fatalf("transcript = %q", joined)
					}
					return
				}
			}
		case <-timeout:
			t.Fatalf("no full transcript in time; got %q", got)
		}
	}
}
