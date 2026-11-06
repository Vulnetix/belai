package headless

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// ReadPrompt reads a prompt from r, at most limit bytes. It refuses an empty
// prompt, one over the limit, invalid UTF-8 and a NUL byte, then cleans the
// text with sanitize.Text. The prompt is the user's own words, like -prompt
// and a typed line, so it takes the same admission as those and is not
// classified here. It reads at most limit+1 bytes, so an endless stream
// cannot fill memory.
func ReadPrompt(r io.Reader, limit int) (string, error) {
	if limit <= 0 {
		return "", errors.New("stdin: the prompt limit must be positive")
	}
	b, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return "", fmt.Errorf("stdin: %w", err)
	}
	if len(b) > limit {
		return "", fmt.Errorf("stdin: the prompt is larger than %d bytes (headless.max_stdin_bytes)", limit)
	}
	if strings.IndexByte(string(b), 0) >= 0 {
		return "", errors.New("stdin: the prompt holds a NUL byte")
	}
	if !utf8.Valid(b) {
		return "", errors.New("stdin: the prompt is not valid UTF-8")
	}
	text := strings.TrimSpace(sanitize.Text(string(b)))
	if text == "" {
		return "", errors.New("stdin: the prompt is empty")
	}
	return text, nil
}

// StdinIsPrompt reports whether standard input looks like a prompt someone
// piped in: a pipe or a regular file. A terminal, /dev/null and a socket are
// not, so an inherited descriptor never makes a bare `belai` wait for input.
func StdinIsPrompt(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	m := fi.Mode()
	return m&os.ModeNamedPipe != 0 || m.IsRegular()
}
