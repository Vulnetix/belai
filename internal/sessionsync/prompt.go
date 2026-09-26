package sessionsync

import (
	"errors"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/sanitize"
)

// MaxPromptBytes matches the server's cap on a web prompt.
const MaxPromptBytes = 32 << 10

// ErrTokenCredential explains why a token login cannot sync: the console
// accepts the CLI's ApiKey (or a session JWT), not an opaque API token.
var ErrTokenCredential = errors.New("the Vulnetix CLI is logged in with an API token, which the console does not accept; run `vulnetix auth login` in a browser to sync sessions")

// UsableCredential reports whether an Authorization header value can reach
// the console: an ApiKey, or a Bearer that is shaped like a JWT.
func UsableCredential(header string) error {
	switch {
	case strings.HasPrefix(header, "ApiKey "):
		return nil
	case strings.HasPrefix(header, "Bearer ") && strings.Count(header, ".") == 2:
		return nil
	case strings.HasPrefix(header, "Bearer "):
		return ErrTokenCredential
	default:
		return errors.New("no usable Vulnetix credential")
	}
}

// CleanPrompt makes a web prompt safe to put in the transcript and the
// terminal: harness delimiter markup is removed, control runes other than
// newline and tab (terminal escape sequences included) and bidi overrides are
// dropped, line endings are normalised, and it is capped at MaxPromptBytes.
// The result is then admitted exactly like a typed prompt.
func CleanPrompt(s string) string {
	s = sanitize.Sanitize(strings.ReplaceAll(s, "\r\n", "\n"))
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r == unicode.ReplacementChar || unicode.IsControl(r) || isBidi(r) {
			continue
		}
		if b.Len()+len(string(r)) > MaxPromptBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// isBidi reports the explicit bidirectional formatting runes, which can make
// the text a reader sees differ from the text the model receives.
func isBidi(r rune) bool {
	switch {
	case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0x061C:
		return true
	}
	return false
}
