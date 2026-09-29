package sessionsync

import (
	"errors"
	"strings"
	"unicode/utf8"

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
// terminal: it goes through sanitize.Text (delimiter markup, terminal escape
// sequences, control, bidi and zero-width runes and invalid UTF-8 are removed,
// line endings are normalised) and is capped at MaxPromptBytes.
// The result is then admitted exactly like a typed prompt.
func CleanPrompt(s string) string {
	s = sanitize.Text(s)
	if len(s) > MaxPromptBytes {
		cut := MaxPromptBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return strings.TrimSpace(s)
}
