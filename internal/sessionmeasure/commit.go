package sessionmeasure

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/session"
)

// EntryCommit is the session entry type of one commit the harness made.
const EntryCommit = "commit"

// Trailer is one key: value line of a commit message's trailer block, such as
// Signed-off-by or Co-authored-by.
type Trailer struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Commit is a commit as the session records it. Names and emails are as git
// stored them, so the server can tell the bot's commits from a person's.
type Commit struct {
	SHA            string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
	Trailers       []Trailer
	Branch         string
}

// maxTrailers caps the trailers one commit entry carries.
const maxTrailers = 32

// Meta is the entry's meta, the wire shape vdb-site reads:
//
//	{"sha":"<40 hex>","author_name":"Pix [bot]","author_email":"security@vulnetix.com",
//	 "committer_name":"Pix [bot]","committer_email":"security@vulnetix.com",
//	 "trailers":[{"key":"Signed-off-by","value":"Pix [bot] <security@vulnetix.com>"}],
//	 "branch":"belai/k-0363e1"}
func (c Commit) Meta() map[string]any {
	tr := make([]map[string]string, 0, len(c.Trailers))
	for _, t := range c.Trailers {
		tr = append(tr, map[string]string{"key": t.Key, "value": t.Value})
	}
	return map[string]any{
		"sha":             c.SHA,
		"author_name":     c.AuthorName,
		"author_email":    c.AuthorEmail,
		"committer_name":  c.CommitterName,
		"committer_email": c.CommitterEmail,
		"trailers":        tr,
		"branch":          c.Branch,
	}
}

// Entry is the commit as a session entry.
func (c Commit) Entry() session.Entry {
	return session.Entry{Type: EntryCommit, Role: "system", Meta: c.Meta()}
}

// Git runs git with args in the repository and returns its standard output.
// Callers adapt their own runner to it.
type Git func(ctx context.Context, args ...string) (string, error)

// logFormat prints the five identity fields, a record separator, then the
// message's trailers one per line.
const logFormat = "--format=%H%x1f%an%x1f%ae%x1f%cn%x1f%ce%x1e%(trailers:only,unfold)"

var (
	shaRE        = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	trailerKeyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
)

// Read reads the commit rev names, recording branch as the branch it was made
// on (a detached or unknown branch is ""). It fails when git cannot name the
// commit or prints something that is not a commit id.
func Read(ctx context.Context, git Git, rev, branch string) (Commit, error) {
	out, err := git(ctx, "log", "-1", logFormat, rev)
	if err != nil {
		return Commit{}, err
	}
	return ParseLog(out, branch)
}

// ParseLog parses the output of `git log -1` with logFormat.
func ParseLog(out, branch string) (Commit, error) {
	head, trailers, _ := strings.Cut(out, "\x1e")
	f := strings.Split(strings.TrimSpace(head), "\x1f")
	if len(f) != 5 || !shaRE.MatchString(strings.ToLower(strings.TrimSpace(f[0]))) {
		return Commit{}, errors.New("sessionmeasure: git did not print a commit")
	}
	c := Commit{
		SHA:            strings.ToLower(strings.TrimSpace(f[0])),
		AuthorName:     clean(f[1], 200),
		AuthorEmail:    clean(f[2], 200),
		CommitterName:  clean(f[3], 200),
		CommitterEmail: clean(f[4], 200),
		Branch:         clean(branch, 200),
	}
	for _, line := range strings.Split(trailers, "\n") {
		key, val, ok := strings.Cut(line, ":")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !ok || val == "" || !trailerKeyRE.MatchString(key) {
			continue
		}
		if len(c.Trailers) >= maxTrailers {
			break
		}
		c.Trailers = append(c.Trailers, Trailer{Key: key, Value: clean(val, 300)})
	}
	return c, nil
}

// clean makes text from a commit safe to store: markup removed, one line,
// capped.
func clean(s string, n int) string { return sanitize.Line(s, n) }

// ReadHead reads the commit HEAD names in dir and the branch it is on. It is
// what a caller runs right after it made a commit. The branch is "" on a
// detached HEAD. run runs a command in dir and returns its standard output,
// the shape of forge.Runner.
func ReadHead(ctx context.Context, run func(ctx context.Context, dir string, argv ...string) ([]byte, error), dir string) (Commit, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	git := func(ctx context.Context, args ...string) (string, error) {
		out, err := run(ctx, dir, append([]string{"git"}, args...)...)
		return string(out), err
	}
	branch, _ := git(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	return Read(ctx, git, "HEAD", strings.TrimSpace(branch))
}
