package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/netguard"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/shellsafe"
)

// Format names the deterministic check an argument passes before its tool
// runs. What a string may safely contain depends on where it is going: a path
// is not a URL is not a shell command is not a glob. A property that declares
// its Format is checked by the matching function here, so the right check is
// stated by the tool's schema instead of remembered inside each tool. The
// Format is not sent to the model. An empty Format applies no format check.
type Format string

const (
	// FormatPath is a filesystem path (lexical checks only; confinement is done
	// by the tool's path resolution).
	FormatPath Format = "path"
	// FormatURL is a URL the harness will fetch on the model's behalf.
	FormatURL Format = "url"
	// FormatIdent is an identifier: ASCII letters, digits, dot, underscore, hyphen.
	FormatIdent Format = "ident"
	// FormatLine is a single line of text with no control characters.
	FormatLine Format = "line"
	// FormatCommand is a shell command line: bounded, valid UTF-8, no control
	// bytes other than tab and newline. Its structure is judged by shellsafe.
	FormatCommand Format = "command"
	// FormatGlob is a glob pattern.
	FormatGlob Format = "glob"
	// FormatRegex is a regular expression.
	FormatRegex Format = "regex"
)

// maxPatternBytes bounds glob and regex arguments.
const maxPatternBytes = 4096

// CheckFormat validates a string argument against its declared Format. An
// empty value passes: whether it is required is the tool's concern.
func CheckFormat(f Format, v string) error {
	if v == "" || f == "" {
		return nil
	}
	switch f {
	case FormatPath:
		return sanitize.PathText(v)
	case FormatURL:
		_, err := netguard.CheckURL(v, netguard.Fetch)
		return err
	case FormatIdent:
		if sanitize.Ident(v, 0) != v {
			return fmt.Errorf("only letters, digits, dot, underscore and hyphen are allowed")
		}
	case FormatLine:
		if strings.ContainsAny(v, "\n\r") || sanitize.Text(v) != v {
			return fmt.Errorf("must be a single line without control characters")
		}
	case FormatCommand:
		return shellsafe.Clean(v)
	case FormatGlob, FormatRegex:
		if len(v) > maxPatternBytes || !utf8.ValidString(v) || strings.Contains(v, "\x00") {
			return fmt.Errorf("must be valid UTF-8 without NUL and at most %d bytes", maxPatternBytes)
		}
	default:
		return fmt.Errorf("unknown format %q", f)
	}
	return nil
}
