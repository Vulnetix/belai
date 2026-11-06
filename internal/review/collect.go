package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/workdiff"
)

// Collector gathers the change a review covers. Every git call goes through
// [workdiff.Git] (read-only, hooks and fsmonitor off, no prompts, scrubbed
// environment), and a pull request is read through the forge provider's
// read-only DiffPR.
type Collector struct {
	// Dir is the working directory of the repository.
	Dir string
	// Hide reports a repository-relative slash path a Read deny rule covers.
	Hide func(rel string) bool
	// Limits bound the diff.
	Limits Limits
	// Provider resolves the forge provider for Dir; nil uses the origin remote
	// and the forge CLIs on PATH.
	Provider func(ctx context.Context) (forge.Provider, error)
	// Git runs a read-only git command; nil uses [workdiff.Git].
	Git func(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Collected is a change ready to be reviewed.
type Collected struct {
	Target Target
	Diff   Diff
	// Repo is the repository key; HeadSHA the full commit id the review covers;
	// Digest tells two states of a working tree apart.
	Repo    string
	HeadSHA string
	Digest  string
	// BaseSHA is the merge base of a branch review, empty otherwise.
	BaseSHA string
	// PR is the pull request number for KindPR.
	PR int
	// PRTitle is the pull request's title as the forge gave it, cleaned for the
	// user's screen. It is never put in a prompt.
	PRTitle string
	// Provider is the forge kind for KindPR ("github", "gitlab", ...).
	ProviderKind string
}

var refRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,199}$`)

// ValidRef reports whether s is a ref name this package will pass to git: it
// starts with a letter or digit, holds only letters, digits and . _ / @ + -,
// has no "..", "@{" or trailing slash or ".lock". git is still asked to
// resolve it; this only keeps an option or a revision expression out.
func ValidRef(s string) bool {
	return refRE.MatchString(s) && !strings.Contains(s, "..") && !strings.Contains(s, "@{") &&
		!strings.HasSuffix(s, "/") && !strings.HasSuffix(s, ".lock")
}

func (c Collector) git(ctx context.Context, args ...string) (string, error) {
	g := c.Git
	if g == nil {
		g = workdiff.Git
	}
	out, err := g(ctx, c.Dir, args...)
	return strings.TrimSpace(string(out)), err
}

// resolve turns a ref into a full commit id.
func (c Collector) resolve(ctx context.Context, ref string) (string, error) {
	if !ValidRef(ref) {
		return "", fmt.Errorf("%q is not a branch or ref name this review will pass to git", ref)
	}
	out, err := c.git(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || !isSHA(out) {
		return "", fmt.Errorf("%s does not name a commit in this repository", ref)
	}
	return out, nil
}

func isSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// defaultBase finds the ref a branch is compared with when none is named: the
// remote's default branch, then main or master.
func (c Collector) defaultBase(ctx context.Context) (string, error) {
	if out, err := c.git(ctx, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(out, "refs/remotes/"); name != out && ValidRef(name) {
			if sha, err := c.resolve(ctx, name); err == nil {
				return sha, nil
			}
		}
	}
	for _, name := range []string{"origin/main", "origin/master", "main", "master"} {
		if sha, err := c.resolve(ctx, name); err == nil {
			return sha, nil
		}
	}
	return "", errors.New("no base to compare with: name one with -base (or /review --base REF)")
}

// Collect reads the change t names.
func (c Collector) Collect(ctx context.Context, t Target) (Collected, error) {
	out := Collected{Target: t, Repo: repoKey(c.Dir)}
	switch t.Kind {
	case KindWorkdir, "":
		out.Target.Kind = KindWorkdir
		return c.workdir(ctx, out)
	case KindBranch:
		return c.branch(ctx, out)
	case KindPR:
		return c.pullRequest(ctx, out)
	}
	return out, fmt.Errorf("unknown review target %q", t.Kind)
}

func (c Collector) workdir(ctx context.Context, out Collected) (Collected, error) {
	snap := workdiff.Collect(ctx, c.Dir, c.Hide)
	if snap.Unavailable != "" {
		return out, errors.New(snap.Unavailable)
	}
	out.Diff = FromSnapshot(snap, c.Limits)
	if sha, err := c.git(ctx, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil && isSHA(sha) {
		out.HeadSHA = sha
	}
	h := sha256.New()
	for _, f := range snap.Files {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00", f.Path, f.Status, f.Added, f.Removed)
		h.Write([]byte(f.Change.New))
		h.Write([]byte{0})
	}
	out.Digest = hex.EncodeToString(h.Sum(nil))[:12]
	if len(snap.Files) == 0 {
		return out, errors.New("the working tree has no changes to review")
	}
	return out, nil
}

func (c Collector) branch(ctx context.Context, out Collected) (Collected, error) {
	ref := out.Target.Ref
	if ref == "" {
		ref = "HEAD"
	}
	head, err := c.resolve(ctx, ref)
	if err != nil {
		return out, err
	}
	var base string
	if out.Target.Base == "" {
		base, err = c.defaultBase(ctx)
	} else {
		base, err = c.resolve(ctx, out.Target.Base)
	}
	if err != nil {
		return out, err
	}
	mb, err := c.git(ctx, "merge-base", base, head)
	if err != nil || !isSHA(mb) {
		return out, errors.New("the branch and its base share no history")
	}
	text, err := c.git(ctx, "diff", "--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "-U3", mb, head)
	if err != nil {
		return out, errors.New("git could not produce the branch diff")
	}
	out.HeadSHA, out.BaseSHA = head, mb
	out.Diff = ParseUnified(text, c.Limits, c.hide)
	if len(out.Diff.Files) == 0 {
		return out, errors.New("the branch has no changes against its base")
	}
	return out, nil
}

func (c Collector) hide(path string) bool { return c.Hide != nil && c.Hide(path) }

func (c Collector) provider(ctx context.Context) (forge.Provider, error) {
	if c.Provider != nil {
		return c.Provider(ctx)
	}
	return ProviderFor(c.Dir)
}

// ProviderFor returns the forge provider for the repository at dir, from its
// origin remote and the forge CLI on PATH, or why there is none.
func ProviderFor(dir string) (forge.Provider, error) {
	rem, ok := forge.ParseRemote(gitinfo.OriginURL(dir))
	if !ok {
		return nil, errors.New("no origin remote, so no pull request to read")
	}
	p, reason := forge.For(rem, forge.ExecRunner, exec.LookPath)
	if p == nil {
		return nil, errors.New(reason)
	}
	return p, nil
}

func (c Collector) pullRequest(ctx context.Context, out Collected) (Collected, error) {
	p, err := c.provider(ctx)
	if err != nil {
		return out, err
	}
	number := 0
	switch ref := strings.TrimPrefix(strings.TrimSpace(out.Target.Ref), "#"); ref {
	case "", "current":
		branch, berr := c.git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
		if berr != nil || !ValidRef(branch) {
			return out, errors.New("HEAD is not on a branch, so name the pull request by number")
		}
		pr, perr := p.PRForBranch(ctx, c.Dir, branch)
		if perr != nil {
			return out, fmt.Errorf("could not look up the pull request: %s", forge.CleanErr(perr))
		}
		if pr == nil {
			return out, fmt.Errorf("branch %s has no open %s", branch, p.Noun())
		}
		number, out.PRTitle = pr.Number, pr.Title
	default:
		n, nerr := strconv.Atoi(ref)
		if nerr != nil || n <= 0 || n > 1<<31-1 {
			return out, fmt.Errorf("%q is not a pull request number", out.Target.Ref)
		}
		number = n
	}
	pd, err := p.DiffPR(ctx, c.Dir, number)
	if err != nil {
		return out, fmt.Errorf("could not read the %s diff: %s", p.Noun(), forge.CleanErr(err))
	}
	out.PR, out.ProviderKind = number, p.Kind()
	out.Target.Ref = strconv.Itoa(number)
	out.HeadSHA = pd.HeadSHA
	out.Diff = ParseUnified(pd.Diff, c.Limits, c.hide)
	if pd.Truncated {
		out.Diff.Truncated = true
	}
	if len(out.Diff.Files) == 0 {
		return out, fmt.Errorf("the %s has no changes", p.Noun())
	}
	// The reviewer has a read tool for GitHub and GitLab pull requests. For the
	// other forges it reads the diff from the checkout, which must therefore be
	// at the pull request's head.
	if k := p.Kind(); k == forge.KindGitea || k == forge.KindForgejo {
		head, herr := c.resolve(ctx, "HEAD")
		if out.HeadSHA == "" || herr != nil || head != out.HeadSHA {
			return out, fmt.Errorf("a %s review reads the diff from the checkout: check out the pull request's head first", p.Label())
		}
		base, berr := c.defaultBase(ctx)
		if berr != nil {
			return out, berr
		}
		if mb, merr := c.git(ctx, "merge-base", base, head); merr == nil && isSHA(mb) {
			out.BaseSHA = mb
		} else {
			return out, errors.New("the checkout and the default branch share no history")
		}
	}
	return out, nil
}

// repoKey names a repository for the store without naming it: a hash of the
// origin's owner/repo, or of the directory when there is no origin.
func repoKey(dir string) string {
	id := dir
	if rem, ok := forge.ParseRemote(gitinfo.OriginURL(dir)); ok && rem.Slug() != "" {
		id = rem.Host + "/" + rem.Slug()
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:16]
}
