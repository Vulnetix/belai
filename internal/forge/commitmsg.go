package forge

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/sanitize"
)

// maxCommitBodyRunes is the width a commit body line may have.
const maxCommitBodyRunes = 72

// MaxCommitMessageBytes bounds a whole commit message the workflow accepts.
const MaxCommitMessageBytes = 4096

// ConventionalTypes are the Conventional Commits types the workflow accepts.
var ConventionalTypes = []string{"feat", "fix", "docs", "style", "refactor", "perf", "test", "build", "ci", "chore", "revert"}

var headerRE = regexp.MustCompile(`^([a-z]+)(\([a-z0-9._/-]+\))?(!)?: (\S.*)$`)

// ValidateConventional checks a commit message against Conventional Commits
// 1.0.0 as the project writes it: a known lowercase type, an optional scope and
// "!", a description that starts lowercase, has no trailing period and keeps the
// header within 72 characters, a blank line before any body, and body lines of at
// most 72 characters (a line with no space, such as a URL, may be longer). It
// accepts text only: any control character other than a newline is refused.
func ValidateConventional(msg string) error {
	if msg == "" || strings.TrimSpace(msg) == "" {
		return errors.New("the message is empty")
	}
	if len(msg) > MaxCommitMessageBytes {
		return fmt.Errorf("the message is over %d bytes", MaxCommitMessageBytes)
	}
	for _, r := range msg {
		if r == '\n' {
			continue
		}
		if unicode.IsControl(r) {
			return errors.New("the message holds a control character")
		}
	}
	lines := strings.Split(msg, "\n")
	header := lines[0]
	if n := utf8.RuneCountInString(header); n > maxCommitHeaderRunes {
		return fmt.Errorf("the header is %d characters, at most %d", n, maxCommitHeaderRunes)
	}
	m := headerRE.FindStringSubmatch(header)
	if m == nil {
		return errors.New(`the header must read "type(scope)!: description" in lowercase`)
	}
	known := false
	for _, t := range ConventionalTypes {
		known = known || t == m[1]
	}
	if !known {
		return fmt.Errorf("%q is not a conventional commit type", m[1])
	}
	desc := m[4]
	if first, _ := utf8.DecodeRuneInString(desc); unicode.IsUpper(first) {
		return errors.New("the description must start in lowercase")
	}
	if strings.HasSuffix(strings.TrimSpace(desc), ".") {
		return errors.New("the description must not end with a period")
	}
	if len(lines) > 1 && lines[1] != "" {
		return errors.New("a blank line must follow the header")
	}
	for _, l := range lines[1:] {
		if utf8.RuneCountInString(l) > maxCommitBodyRunes && strings.ContainsAny(strings.TrimSpace(l), " \t") {
			return fmt.Errorf("a body line is over %d characters", maxCommitBodyRunes)
		}
	}
	return nil
}

// trailerRE matches a git trailer line of the kinds only the harness adds.
var trailerRE = regexp.MustCompile(`(?i)^\s*(co-authored-by|signed-off-by|session|finding|vulnerability|advisory)\s*:`)

// StripTrailers removes every trailer line a model or a user could write for
// the harness's own set (Co-authored-by, Signed-off-by, Session, Finding,
// Vulnerability, Advisory), so attribution.Message is the only source of them,
// then trims blank lines left at the end.
func StripTrailers(msg string) string {
	lines := strings.Split(msg, "\n")
	out := lines[:0:0]
	for _, l := range lines {
		if trailerRE.MatchString(l) {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), " \n\t")
}

// CleanDraft turns a drafting model's reply into a commit message the
// workflow may use, or an error. The reply is model text, so it is sanitised
// (the header as one line, the body as text), a surrounding code fence is
// removed, every trailer line is stripped, and the result must pass
// ValidateConventional. A caller that gets an error uses TaskCommitMessage.
func CleanDraft(raw string) (string, error) {
	s := strings.TrimSpace(sanitize.Text(raw))
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	s = StripTrailers(s)
	if s == "" {
		return "", errors.New("the draft is empty")
	}
	header, body, _ := strings.Cut(s, "\n")
	header = sanitize.Line(header, 0)
	body = strings.TrimSpace(body)
	msg := header
	if body != "" {
		msg += "\n\n" + body
	}
	if err := ValidateConventional(msg); err != nil {
		return "", err
	}
	return msg, nil
}

// maxStatLines bounds the line-count summary a draft is shown.
const maxStatLines = 50

// DiffStat composes one line per path from line counts alone: "path +A -D",
// "path binary" or "path new file". It never reads a file's text. The counts
// come from `git diff --numstat` against HEAD; a path git does not list is a
// new file. An unreadable diff reports every path as a new file, which is also
// what an unborn HEAD is.
func DiffStat(ctx context.Context, r Runner, root string, paths []string) string {
	if r == nil {
		r = ExecRunner
	}
	rel, _ := commitPathsInside(root, paths)
	if len(rel) > maxStatLines {
		rel = rel[:maxStatLines]
	}
	if len(rel) == 0 {
		return ""
	}
	counts := map[string]string{}
	argv := append([]string{"git", "diff", "--numstat", "--no-renames", "--no-ext-diff", "-z", "HEAD", "--"}, rel...)
	tctx, cancel := context.WithTimeout(ctx, WriteTimeout)
	defer cancel()
	if out, err := r(tctx, root, argv...); err == nil {
		for _, rec := range strings.Split(string(out), "\x00") {
			f := strings.SplitN(rec, "\t", 3)
			if len(f) != 3 {
				continue
			}
			if f[0] == "-" || f[1] == "-" {
				counts[f[2]] = "binary"
			} else if a, aerr := strconv.Atoi(f[0]); aerr == nil {
				if d, derr := strconv.Atoi(f[1]); derr == nil {
					counts[f[2]] = fmt.Sprintf("+%d -%d", a, d)
				}
			}
		}
	}
	var b strings.Builder
	for _, p := range rel {
		c := counts[p]
		if c == "" {
			c = "new file"
		}
		b.WriteString(sanitize.Line(p, 200) + " " + c + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// PathsCommitMessage is the deterministic message for a commit with no goal
// to name it: a type decided from the paths and a subject that names one path
// or counts them.
func PathsCommitMessage(paths []string) string {
	subject := "update changed files"
	switch len(paths) {
	case 0:
	case 1:
		subject = "update " + sanitize.Line(paths[0], 60)
	default:
		subject = fmt.Sprintf("update %d files", len(paths))
	}
	header := commitType("", paths) + ": " + subject
	var b strings.Builder
	b.WriteString(header)
	if len(paths) > 0 {
		b.WriteString("\n\n")
		for i, p := range paths {
			if i == maxCommitBodyPaths {
				break
			}
			b.WriteString(sanitize.Line(p, 200) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
