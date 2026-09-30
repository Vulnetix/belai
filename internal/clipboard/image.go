package clipboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/proc"
)

// ErrNoImage means the clipboard holds no image, or no program that can read
// one is installed. The caller falls back to a text paste.
var ErrNoImage = errors.New("no image on the clipboard")

// MaxImageBytes caps what ReadImage returns, so a huge clipboard cannot be
// pulled into memory. It matches the largest image imageguard admits.
const MaxImageBytes = 32 << 20

const readTimeout = 5 * time.Second

// imageReaders are the programs tried, in order, each with its fixed argument
// list. Nothing from the clipboard, the model or a settings file reaches an
// argv. wl-paste is Wayland, xclip is X11, pngpaste is macOS.
var imageReaders = func() [][]string {
	switch runtime.GOOS {
	case "darwin":
		return [][]string{{"pngpaste", "-"}}
	case "windows":
		return nil
	}
	var out [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		out = append(out, []string{"wl-paste", "--no-newline", "--type", "image/png"})
	}
	if os.Getenv("DISPLAY") != "" {
		out = append(out, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"})
	}
	return out
}

// ReadImage returns the image on the system clipboard as PNG bytes. It runs one
// of a fixed set of programs with the scrubbed environment in its own process
// group, waits at most five seconds, and reads at most MaxImageBytes. The
// bytes are untrusted until internal/imageguard has admitted them.
//
// It returns ErrNoImage when the clipboard holds no image or no reader is
// installed, and a different error only when a reader ran and failed oddly.
func ReadImage(ctx context.Context) ([]byte, error) {
	readers := imageReaders()
	if len(readers) == 0 {
		return nil, ErrNoImage
	}
	found := false
	for _, argv := range readers {
		bin, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		found = true
		data, err := runReader(ctx, bin, argv[1:])
		if err == nil && len(data) > 0 {
			return data, nil
		}
		if errors.Is(err, errTooBig) {
			return nil, err
		}
	}
	if !found {
		return nil, fmt.Errorf("%w (install one of: %s)", ErrNoImage, installHint(readers))
	}
	return nil, ErrNoImage
}

var errTooBig = fmt.Errorf("the clipboard image is over %d MiB", MaxImageBytes>>20)

func installHint(readers [][]string) string {
	names := make([]string, len(readers))
	for i, r := range readers {
		names[i] = r[0]
	}
	return strings.Join(names, ", ")
}

func runReader(ctx context.Context, bin string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	ec := exec.CommandContext(ctx, bin, args...)
	ec.Env = proc.ScrubbedEnv()
	proc.SetProcessGroup(ec)
	ec.WaitDelay = time.Second
	stdout, err := ec.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := ec.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, MaxImageBytes+1))
	waitErr := ec.Wait()
	if len(data) > MaxImageBytes {
		return nil, errTooBig
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		// The usual case: the clipboard holds text, so the reader exits
		// non-zero. That is "no image", not a failure.
		return nil, ErrNoImage
	}
	return data, nil
}
