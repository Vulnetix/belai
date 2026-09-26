// Package agentscan discovers credentials and provider definitions in other
// coding agents' on-disk configuration. It is strictly read-only: it opens a
// fixed list of absolute paths under a supplied home directory, never globs,
// never follows symlinks out of home, and never executes anything. Nothing is
// scanned implicitly — callers invoke Scan on an explicit user action.
package agentscan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/belai/internal/config"
)

// maxFileSize caps every file read at 1 MiB.
const maxFileSize = 1 << 20

// Found is one credential (or one explained absence) discovered in another
// agent's on-disk configuration.
type Found struct {
	Agent    string                  // "pi", "codex", "goose", …
	Provider string                  // belai provider name, or the custom name to create
	Field    string                  // "api_key", "oauth_token"
	Location string                  // absolute path it came from
	EnvKey   string                  // set when the source is itself an env reference
	Profile  *config.ProviderProfile // non-nil when this needs a custom provider
	Models   []config.ProviderModel
	Note     string // why it is not importable, or a warning about dropped shape
	value    string // unexported, like credentials.Value
}

// Importable reports whether the finding has a secret or a profile worth
// importing.
func (f Found) Importable() bool {
	return f.value != "" || f.Profile != nil
}

// Reveal returns the raw discovered value. It is deliberately not part of any
// String representation.
func (f Found) Reveal() string { return f.value }

// Mask renders a redacted hint: first 3 characters, an ellipsis, last 4. A
// value shorter than 8 characters renders as just "…".
func (f Found) Mask() string {
	if len(f.value) < 8 {
		return "…"
	}
	return f.value[:3] + "…" + f.value[len(f.value)-4:]
}

// String never prints the value.
func (f Found) String() string {
	return fmt.Sprintf("agentscan.Found{Agent:%q, Provider:%q, Field:%q, Location:%q, value:%q}", f.Agent, f.Provider, f.Field, f.Location, f.Mask())
}

// GoString also never prints the value; these end up in test failure output.
func (f Found) GoString() string { return f.String() }

// Scan reads every known agent location under home and returns what it found,
// in a stable order. Unreadable or unparseable files are skipped with a Note;
// Scan never returns an error.
func Scan(home string) []Found {
	var out []Found
	out = append(out, scanPi(home)...)
	out = append(out, scanCodex(home)...)
	out = append(out, scanClaudeCode(home)...)
	out = append(out, scanGoose(home)...)
	out = append(out, scanOpenCode(home)...)
	out = append(out, scanCopilot(home)...)
	out = append(out, scanOther(home)...)
	return out
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// readCapped reads path, refusing any file larger than maxFileSize.
func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("file exceeds %d bytes", maxFileSize)
	}
	return data, nil
}

// configDir resolves XDG_CONFIG_HOME, falling back to ~/.config.
func configDir(home string) string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

// dataDir resolves XDG_DATA_HOME, falling back to ~/.local/share.
func dataDir(home string) string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	return filepath.Join(home, ".local", "share")
}

// dirExists reports whether a directory (not a file) exists at path.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// note returns a non-importable Found carrying a reason.
func note(agent, path, reason string) Found {
	return Found{Agent: agent, Location: path, Note: reason}
}

// sortFounds imposes a stable order over a scan's findings.
func sortFounds(f []Found) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Provider != f[j].Provider {
			return f[i].Provider < f[j].Provider
		}
		return f[i].Field < f[j].Field
	})
}
