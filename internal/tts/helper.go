package tts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// Opener starts one playback: it returns a writer that takes raw PCM. Closing
// the writer lets what was written finish playing; cancelling ctx stops the
// sound at once.
type Opener func(ctx context.Context) (io.WriteCloser, error)

type helperSpec struct {
	name string
	args []string
}

// helpers lists the playback programs tried, best first. Each reads raw 24 kHz
// 16-bit mono PCM on stdin. The argument lists are fixed here: nothing a model
// or a setting says reaches a program's argv.
func helpers() []helperSpec {
	ffplay := helperSpec{"ffplay", []string{"-nodisp", "-autoexit", "-loglevel", "quiet", "-f", "s16le", "-ar", "24000", "-ac", "1", "-i", "pipe:0"}}
	sox := helperSpec{"play", []string{"-q", "-t", "raw", "-r", "24000", "-e", "signed", "-b", "16", "-c", "1", "-"}}
	switch runtime.GOOS {
	case "linux":
		return []helperSpec{
			{"paplay", []string{"--raw", "--rate=24000", "--channels=1", "--format=s16le", "--latency-msec=100"}},
			{"aplay", []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "24000", "-c", "1"}},
			ffplay, sox,
		}
	case "windows":
		return []helperSpec{ffplay}
	default:
		return []helperSpec{ffplay, sox}
	}
}

// ErrNoPlayer means no playback program is installed.
var ErrNoPlayer = errors.New("no audio player found: install one of paplay, aplay, ffplay or sox play")

// NewExecOpener finds the first installed playback program and returns an
// Opener for it, with the program's name. It is the audio counterpart of the
// microphone helper in internal/voice: a fixed argv, the scrubbed environment
// and its own process group, outside the OS sandbox and never reachable from a
// tool call.
func NewExecOpener() (Opener, string, error) {
	for _, h := range helpers() {
		if path, err := exec.LookPath(h.name); err == nil {
			return execOpener(path, h.args), h.name, nil
		}
	}
	return nil, "", ErrNoPlayer
}

func execOpener(path string, args []string) Opener {
	return func(ctx context.Context) (io.WriteCloser, error) {
		cmd := exec.CommandContext(ctx, path, args...)
		cmd.Env = proc.ScrubbedEnv()
		proc.SetProcessGroup(cmd)
		var errTail tailBuf
		cmd.Stderr = &errTail
		in, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return &procWriter{in: in, cmd: cmd, errTail: &errTail}, nil
	}
}

// procWriter feeds a helper's stdin. Close ends the input and waits briefly
// for the helper to finish what it was given.
type procWriter struct {
	in      io.WriteCloser
	cmd     *exec.Cmd
	errTail *tailBuf
	once    sync.Once
	waitErr error
}

func (w *procWriter) Write(b []byte) (int, error) { return w.in.Write(b) }

func (w *procWriter) Close() error {
	w.once.Do(func() {
		_ = w.in.Close()
		done := make(chan error, 1)
		go func() { done <- w.cmd.Wait() }()
		select {
		case w.waitErr = <-done:
		case <-time.After(10 * time.Second):
			_ = w.cmd.Process.Kill()
			w.waitErr = <-done
		}
	})
	if w.waitErr != nil {
		if tail := w.errTail.String(); tail != "" {
			return errors.New(tail)
		}
	}
	return nil
}

// tailBuf keeps the last bytes a helper wrote to stderr, for an error message.
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 512 {
		t.b = t.b[len(t.b)-512:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(bytes.Map(func(r rune) rune {
		if r == '\n' || r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, bytes.TrimSpace(t.b)))
}
