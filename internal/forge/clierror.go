package forge

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/vaultenv"
)

// Bounds of the stderr a CLIError keeps.
const (
	// maxStderrBytes is the tail of stderr a CLIError keeps. A push's cause is
	// in its last lines; the progress before them is not needed.
	maxStderrBytes = 4 << 10
	// maxStderrLine caps one kept stderr line, in runes. It is wider than
	// maxCleanLen so a remote's refusal with a long branch and path survives
	// for ClassifyPushError.
	maxStderrLine = 400
)

// upToDate is the line git push prints when no ref needed sending.
const upToDate = "Everything up-to-date"

// CLIError is a CLI call (git, gh, glab, tea) that exited non-zero or timed
// out. Error() is "<Argv0>: <Summary>", the one line a note shows; Stderr is
// the bounded tail of what the CLI printed, for ClassifyPushError.
//
// Both Summary and Stderr are cleaned line by line (Clean's rules) and
// redacted: URL credentials, forge token shapes, the values of the
// environment handed to the child and every vault value are replaced before
// the text is kept, so neither can carry a credential to a note or a log.
type CLIError struct {
	Argv0 string
	// Summary is the line that says why: a rejected ref, a remote error, a
	// fatal or error line, a refusal, else the last line that is not a hint.
	Summary string
	// Stderr is the cleaned tail, one line per line, at most 4 KiB.
	Stderr string
	// UpToDate is true when git printed "Everything up-to-date". git prints it
	// only after every ref was accepted or already current, so a push that
	// exits non-zero with it set can be checked against the remote ref
	// instead of being reported as failed.
	UpToDate bool
	err      error
}

func (e *CLIError) Error() string { return e.Argv0 + ": " + e.Summary }

// Unwrap is the error exec returned (an *exec.ExitError, usually).
func (e *CLIError) Unwrap() error { return e.err }

// AsCLIError returns the *CLIError err wraps, or nil.
func AsCLIError(err error) *CLIError {
	var ce *CLIError
	if errors.As(err, &ce) {
		return ce
	}
	return nil
}

// errText is the lower-cased text a needle is matched against: a CLIError's
// stderr tail and the error's own text, so a wrapper's words count too.
func errText(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	if ce := AsCLIError(err); ce != nil && ce.Stderr != "" {
		msg = strings.ToLower(ce.Stderr) + "\n" + msg
	}
	return msg
}

// errHas reports whether err's stderr or text holds needle (lower case).
func errHas(err error, needle string) bool { return strings.Contains(errText(err), needle) }

func newCLIError(ctx context.Context, argv0 string, stderr []byte, runErr error, env []string) *CLIError {
	lines := stderrLines(stderr, env)
	e := &CLIError{Argv0: argv0, Stderr: strings.Join(lines, "\n"), err: runErr}
	for _, l := range lines {
		if l == upToDate {
			e.UpToDate = true
		}
	}
	e.Summary = summarize(lines)
	switch {
	case ctx.Err() != nil:
		e.Summary = "timed out"
	case e.Summary == "" && e.UpToDate:
		e.Summary = Clean(runErr.Error()) + ` after "` + upToDate + `"`
	case e.Summary == "":
		e.Summary = Clean(runErr.Error())
	}
	return e
}

// stderrLines is the cleaned, redacted tail of stderr, blank lines dropped. A
// carriage return ends a line too, so progress rewrites are separate lines.
func stderrLines(stderr []byte, env []string) []string {
	if len(stderr) > maxStderrBytes {
		stderr = stderr[len(stderr)-maxStderrBytes:]
		// The cut line is partial: a token it held could escape redaction.
		if i := strings.IndexAny(string(stderr), "\r\n"); i >= 0 {
			stderr = stderr[i+1:]
		} else {
			stderr = nil
		}
	}
	raw := strings.FieldsFunc(string(stderr), func(r rune) bool { return r == '\n' || r == '\r' })
	lines := make([]string, 0, len(raw))
	total := 0
	for _, l := range raw {
		l = sanitize.Clip(redact(l, env), maxStderrLine)
		if l == "" {
			continue
		}
		lines = append(lines, l)
		total += len(l) + 1
	}
	// Redaction can lengthen a line ([vault:NAME]); keep the tail within bounds.
	for total > maxStderrBytes && len(lines) > 0 {
		total -= len(lines[0]) + 1
		lines = lines[1:]
	}
	return lines
}

// credentialName reports whether an environment name holds a credential
// (GH_TOKEN, BELAI_FORGE_TOKEN_<HOST>, a password): its value is redacted,
// while an identity such as GIT_AUTHOR_EMAIL stays readable.
func credentialName(k string) bool {
	k = strings.ToUpper(k)
	for _, w := range []string{"TOKEN", "PASSWORD", "SECRET", "KEY", "CREDENTIAL"} {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var (
	// urlUserinfo is the credential part of a URL: https://user:token@host.
	urlUserinfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@]+@`)
	// tokenShape is a GitHub or GitLab token wherever it appears.
	tokenShape = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,})`)
)

// redact removes credentials from one line of CLI output: vault values,
// credential values handed to the child, URL userinfo and token shapes.
func redact(s string, env []string) string {
	s = vaultenv.Default.Scrub(s)
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && len(v) >= 8 && credentialName(k) {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	s = urlUserinfo.ReplaceAllString(s, "${1}[redacted]@")
	return tokenShape.ReplaceAllString(s, "[redacted]")
}

// summaryTiers rank the lines that say why a CLI failed. The first line of
// the best tier present is the summary. A rejected ref names its reason in
// parentheses, so it outranks the generic "error: failed to push some refs";
// a remote's plain lines (progress, a pull request link) rank last.
var summaryTiers = [][]string{
	{"! [rejected]", "! [remote rejected]", "remote: error", "refusing"},
	{"fatal:", "error:"},
	{"remote:"},
}

// summarize picks the summary line from cleaned stderr lines; "" when none
// says anything but hints and "Everything up-to-date".
func summarize(lines []string) string {
	for _, tier := range summaryTiers {
		for _, l := range lines {
			low := strings.ToLower(l)
			for _, p := range tier {
				if strings.HasPrefix(low, p) && (p != "remote:" || strings.TrimSpace(l[len(p):]) != "") {
					return Clean(l)
				}
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		if l == upToDate || strings.HasPrefix(strings.ToLower(l), "hint:") {
			continue
		}
		return Clean(l)
	}
	return ""
}
