package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Key is a resolved project-directory name: "<basename>-<8 hex sha256>". It
// is a distinct type from a workdir so the two can never be swapped at a call
// site. A workdir addresses a session by re-deriving its key on every call; a
// Key addresses it directly.
type Key string

// KeyFor derives the Key for an absolute working-directory path.
func KeyFor(workdir string) (Key, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return "", fmt.Errorf("resolve workdir: %w", err)
	}
	return Key(WorkdirKey(abs)), nil
}

// String returns the key's directory name.
func (k Key) String() string { return string(k) }

// Project returns the project basename without the trailing hash:
// "signet-275e7780" -> "signet".
func (k Key) Project() string {
	s := string(k)
	if i := strings.LastIndexByte(s, '-'); i > 0 {
		return s[:i]
	}
	return s
}
