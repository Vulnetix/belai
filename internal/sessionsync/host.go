package sessionsync

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/session"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// HostID returns this install's stable host id, minting it on first use. It
// lives in its own file under dir (Belai's global state directory) rather than
// in state.json, which the TUI rewrites wholesale from its own copy.
func HostID(dir string) (string, error) {
	path := filepath.Join(dir, "sync", "host-id")
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); uuidPattern.MatchString(id) {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	id, err := session.NewID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// Hostname returns os.Hostname reduced to identifier characters, capped at 64,
// so a hostile or odd hostname cannot carry markup to the website.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return identifier(h, 64)
}

func identifier(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			break
		}
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '_') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
