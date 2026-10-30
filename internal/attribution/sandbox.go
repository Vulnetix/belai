package attribution

import (
	"bufio"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
)

// FileEnv names the file a Pix sandbox writes with the trailer lines that do not change inside it
// (the credited person and Pix's sign-off). It is written by the sandbox Worker at launch
// (website sandbox-worker/src/launch.ts, GIT_ATTRIBUTION_FILE). Outside a Pix sandbox the file does
// not exist, and belai adds nothing: a developer's own commits are not signed off by a bot.
const FileEnv = "PIX_ATTRIBUTION_FILE"

// DefaultPath is where the sandbox writes the file.
func DefaultPath() string {
	if p := os.Getenv(FileEnv); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".pix", "attribution")
}

// Load reads the sandbox's trailer file. ok is false outside a Pix sandbox. A co-author that is one
// of the bots is no person, so Human stays nil and the other bot is credited as usual.
func Load(path string) (Context, bool) {
	if path == "" {
		return Context{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Context{}, false
	}
	defer f.Close()

	c := Context{Bot: Pix}
	signed := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, "Signed-off-by: "); ok {
			signed = true
			if a, err := mail.ParseAddress(v); err == nil && a.Address == Bots[Vulnetix].Email {
				c.Bot = Vulnetix
			}
		}
		if v, ok := strings.CutPrefix(line, "Co-authored-by: "); ok {
			a, err := mail.ParseAddress(v)
			if err != nil || a.Address == Bots[Pix].Email || a.Address == Bots[Vulnetix].Email {
				continue
			}
			c.Human = &Human{Name: a.Name, Email: a.Address}
		}
	}
	if sc.Err() != nil || !signed {
		return Context{}, false
	}

	return c, true
}

// ForSession is the context for work done in sessionID inside a Pix sandbox, with the vulnerability
// ids the session touched. ok is false outside a sandbox.
func ForSession(sessionID string, vulnIDs ...string) (Context, bool) {
	c, ok := Load(DefaultPath())
	if !ok {
		return Context{}, false
	}
	c.SessionID = sessionID
	c.VulnIDs = vulnIDs

	return c, true
}

// Message adds the trailers to a commit message written in sessionID, and returns it unchanged
// outside a Pix sandbox.
func Message(msg, sessionID string, vulnIDs ...string) string {
	c, ok := ForSession(sessionID, vulnIDs...)
	if !ok {
		return msg
	}

	return Append(msg, c, false)
}

// Markdown adds the link lines (Links) to a pull request body or a comment written in sessionID,
// and returns it unchanged outside a Pix sandbox.
func Markdown(text, sessionID string, vulnIDs ...string) string {
	c, ok := ForSession(sessionID, vulnIDs...)
	if !ok {
		return text
	}

	return Append(text, c, true)
}

// Missing reports whether a commit message lacks the sign-off c asks for.
func Missing(msg string, c Context) bool {
	want := "Signed-off-by: " + person(Bots[c.Bot])
	for _, l := range strings.Split(msg, "\n") {
		if strings.TrimSpace(l) == want {
			return false
		}
	}

	return true
}

// ShellTrailerArgs renders the trailer lines as `--trailer 'k: v'` arguments for a shell command.
func ShellTrailerArgs(c Context) string {
	var b strings.Builder
	for _, l := range Trailers(c) {
		b.WriteString(" --trailer '")
		b.WriteString(strings.ReplaceAll(l, "'", `'\''`))
		b.WriteString("'")
	}

	return strings.TrimLeft(b.String(), " ")
}
