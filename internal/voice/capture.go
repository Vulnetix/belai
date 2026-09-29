package voice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/vulnetix/belai/internal/proc"
)

// Source opens the microphone. Start begins capture and returns 16 kHz mono
// signed 16-bit samples; cancelling ctx stops capture and closes the channel.
// After the channel closes, Err says why capture ended (nil for a stop the
// caller asked for).
type Source interface {
	Start(ctx context.Context) (Stream, error)
}

// Stream is one capture session.
type Stream struct {
	C   <-chan []int16
	Err func() error
}

// ErrNoCapture is returned when no audio capture helper is installed.
var ErrNoCapture = errors.New("no audio capture helper found: install one of parecord (PulseAudio or PipeWire), arecord (alsa-utils), ffmpeg or sox")

// ErrBadDevice is returned for a device name that is not a plain identifier.
var ErrBadDevice = errors.New("voice device must be letters, digits and . _ : , @ = -, and must not start with -")

var deviceRE = regexp.MustCompile(`^[A-Za-z0-9._:,@][A-Za-z0-9._:,@=-]{0,127}$`)

// captureHelper is a program that writes raw PCM to stdout. Belai runs the
// first one it finds; the audio path after that is Go.
type captureHelper struct {
	bin  string
	args func(device string) []string
}

func helpers() []captureHelper {
	list := []captureHelper{
		{"parecord", func(dev string) []string {
			a := []string{"--raw", "--format=s16le", "--rate=16000", "--channels=1", "--latency-msec=50"}
			if dev != "" {
				a = append(a, "--device="+dev)
			}
			// No file argument: parecord then writes the stream to standard output.
			// A "-" here is not stdout, it is a file named "-" in the working
			// directory, which would put the recording on disk.
			return a
		}},
		{"arecord", func(dev string) []string {
			a := []string{"-q", "-t", "raw", "-f", "S16_LE", "-r", "16000", "-c", "1"}
			if dev != "" {
				a = append(a, "-D", dev)
			}
			return a
		}},
		{"ffmpeg", ffmpegArgs},
		{"rec", func(string) []string {
			return []string{"-q", "-r", "16000", "-c", "1", "-b", "16", "-e", "signed-integer", "-t", "raw", "-"}
		}},
	}
	if runtime.GOOS != "linux" {
		// Only ffmpeg and sox know the native audio APIs elsewhere.
		list = []captureHelper{list[2], list[3]}
	}
	return list
}

func ffmpegArgs(dev string) []string {
	a := []string{"-hide_banner", "-loglevel", "error"}
	switch runtime.GOOS {
	case "darwin":
		if dev == "" {
			dev = "default"
		}
		a = append(a, "-f", "avfoundation", "-i", ":"+dev)
	case "windows":
		a = append(a, "-f", "dshow", "-i", "audio="+dev)
	default:
		if dev == "" {
			dev = "default"
		}
		a = append(a, "-f", "pulse", "-i", dev)
	}
	return append(a, "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
}

// ExecSource captures through a helper program.
type ExecSource struct {
	bin  string
	args []string
}

// NewExecSource picks the first installed capture helper. device is optional
// and is passed to the helper as the input device; it must be a plain
// identifier, so it can never smuggle in another option.
func NewExecSource(device string) (*ExecSource, error) {
	return newExecSource(device, exec.LookPath)
}

func newExecSource(device string, look func(string) (string, error)) (*ExecSource, error) {
	if device != "" && !deviceRE.MatchString(device) {
		return nil, ErrBadDevice
	}
	if runtime.GOOS == "windows" && device == "" {
		return nil, errors.New("voice.device is required on Windows (the DirectShow audio device name)")
	}
	for _, h := range helpers() {
		if p, err := look(h.bin); err == nil {
			return &ExecSource{bin: p, args: h.args(device)}, nil
		}
	}
	return nil, ErrNoCapture
}

// Name is the helper program's base name, for status lines.
func (s *ExecSource) Name() string {
	i := strings.LastIndexAny(s.bin, `/\`)
	return s.bin[i+1:]
}

// Start launches the helper with the scrubbed environment in its own process
// group, outside the sandbox. It is reachable only from the voice engine, never
// from a tool call.
func (s *ExecSource) Start(ctx context.Context) (Stream, error) {
	cmd := exec.CommandContext(ctx, s.bin, s.args...)
	// The helper runs in a private directory that is removed afterwards. It
	// is given no file to write, but a mistake in an argument must never put
	// a recording in the project or on disk beyond this session.
	dir, derr := os.MkdirTemp("", "belai-voice-*")
	if derr != nil {
		dir = os.TempDir()
	}
	cmd.Dir = dir
	cmd.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(cmd)
	stderr := &tailWriter{max: 240}
	cmd.Stderr = stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		if derr == nil {
			_ = os.RemoveAll(dir)
		}
		return Stream{}, err
	}
	if err := cmd.Start(); err != nil {
		if derr == nil {
			_ = os.RemoveAll(dir)
		}
		return Stream{}, fmt.Errorf("start %s: %w", s.Name(), err)
	}
	ch := make(chan []int16, 64)
	var (
		mu      sync.Mutex
		waitErr error
	)
	go func() {
		defer close(ch)
		buf := make([]byte, 3200)
		var odd []byte
		for {
			n, rerr := out.Read(buf)
			if n > 0 {
				odd = append(odd, buf[:n]...)
				pairs := len(odd) / 2 * 2
				samples := make([]int16, pairs/2)
				for i := range samples {
					samples[i] = int16(uint16(odd[2*i]) | uint16(odd[2*i+1])<<8)
				}
				odd = append(odd[:0], odd[pairs:]...)
				if len(samples) > 0 {
					select {
					case ch <- samples:
					case <-ctx.Done():
					}
				}
			}
			if rerr != nil {
				break
			}
		}
		err := cmd.Wait()
		if derr == nil {
			_ = os.RemoveAll(dir)
		}
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() == nil && err != nil {
			waitErr = fmt.Errorf("%s stopped: %w%s", s.Name(), err, stderr.suffix())
		} else if ctx.Err() == nil {
			waitErr = fmt.Errorf("%s ended unexpectedly%s", s.Name(), stderr.suffix())
		}
	}()
	return Stream{C: ch, Err: func() error {
		mu.Lock()
		defer mu.Unlock()
		return waitErr
	}}, nil
}

// tailWriter keeps the last max bytes written to it, for an error message.
type tailWriter struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

func (w *tailWriter) suffix() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, string(w.buf))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	return ": " + s
}
