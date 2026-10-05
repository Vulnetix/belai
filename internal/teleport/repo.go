package teleport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/sanitize"
)

// originGit is the part of the origin session's repository facts a teleport
// uses: where it was and what it had checked out. The backend holds it only for
// the length of the teleport. Every field is third-party text and is checked
// before it reaches git.
type originGit struct {
	Remote   string `json:"remote"`
	Host     string `json:"host"`
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Detached bool   `json:"detached"`
	Dirty    bool   `json:"dirty"`
}

func parseOriginGit(raw json.RawMessage) originGit {
	var g originGit
	if len(raw) == 0 || json.Unmarshal(raw, &g) != nil {
		return originGit{}
	}
	g.Remote = sanitize.Line(g.Remote, 200)
	g.Host = sanitize.Line(g.Host, 253)
	g.Branch = sanitize.Line(g.Branch, 200)
	g.Head = strings.ToLower(sanitize.Ident(g.Head, 40))
	return g
}

var (
	// shaShape is a commit id as git prints it, abbreviated or whole.
	shaShape = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	// refShape is a ref the user named with -teleport-ref. It may not start with
	// a dash, so git never reads it as an option.
	refShape = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/@+-]{0,199}$`)
)

// checkout is the directory the teleported session runs in.
type checkout struct {
	// Dir is the session's working directory.
	Dir string
	// Worktree is true when Dir is a worktree teleport created.
	Worktree bool
	// Root is the repository the worktree belongs to.
	Root    string
	Notices []string
}

// errUnreachable is the cause of a commit this repository does not have even
// after fetching origin, so the caller can tell it from a mismatched checkout.
var errUnreachable = errors.New("commit not in this repository")

type unreachableError struct{ msg string }

func (e unreachableError) Error() string        { return e.msg }
func (e unreachableError) Is(target error) bool { return target == errUnreachable }

// plan holds what the repository work needs.
type plan struct {
	workdir  string
	override string
	// force always makes a worktree, even when the checkout is already at the
	// commit: a replay applies a patch, which must never touch the checkout the
	// user is in.
	force    bool
	newID    string
	run      forge.Runner
	worktree string // the directory worktrees go under
	progress func(string)
}

func (p plan) git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := p.run(ctx, dir, append([]string{"git"}, args...)...)
	return strings.TrimSpace(string(out)), err
}

// requireRepo is the cheap check that runs first: the directory is in a git
// repository.
func (p plan) requireRepo() error {
	if _, ok := gitinfo.Detect(p.workdir); !ok {
		return errors.New("teleport needs a git repository: run belai -teleport from a checkout of the repository the session was in")
	}
	return nil
}

// prepare decides where the session runs. The directory must be a git
// repository and, when the origin was in one, the same repository. When the
// commit the origin had checked out is not what this checkout has, the session
// gets a worktree at that commit, so nothing in the checkout the user is in is
// moved.
func (p plan) prepare(ctx context.Context, og originGit) (checkout, error) {
	info, ok := gitinfo.Detect(p.workdir)
	if !ok {
		return checkout{}, errors.New("teleport needs a git repository: run belai -teleport from a checkout of the repository the session was in")
	}
	root := info.Root
	out := checkout{Dir: p.workdir, Root: root}

	if og.Remote != "" {
		local, _ := forge.ParseRemote(gitinfo.OriginURL(root))
		if !strings.EqualFold(local.Slug(), og.Remote) || (og.Host != "" && !strings.EqualFold(local.Host, og.Host)) {
			have := local.Slug()
			if have == "" {
				have = "a repository with no origin remote"
			}
			return out, fmt.Errorf("this checkout is %s, and the session was in %s; run belai -teleport from a checkout of %s",
				have, og.Remote, og.Remote)
		}
	} else {
		out.Notices = append(out.Notices, "the origin session had no remote, so its repository could not be matched to this one")
	}
	if og.Dirty {
		out.Notices = append(out.Notices, "the origin session had uncommitted changes; only its transcript was teleported, so they are not here")
	}

	want := og.Head
	if p.override != "" {
		if !refShape.MatchString(p.override) {
			return out, fmt.Errorf("-teleport-ref %q is not a ref name or commit", sanitize.Line(p.override, 60))
		}
		want = p.override
	} else if og.Head == "" {
		out.Notices = append(out.Notices, "the origin session reported no commit, so the session continues in this checkout as it is")
		return out, nil
	} else if !shaShape.MatchString(want) {
		return out, errors.New("the origin session reported a commit id that is not a commit id")
	}

	sha, err := p.resolve(ctx, root, want)
	if err != nil {
		p.say("fetching " + sanitize.Line(want, 60) + " from origin")
		p.fetch(ctx, root, og.Branch)
		if sha, err = p.resolve(ctx, root, want); err != nil {
			if p.override != "" {
				return out, fmt.Errorf("%s is not in this repository, even after fetching origin", sanitize.Line(p.override, 60))
			}
			return out, unreachableError{fmt.Sprintf("the origin session was at commit %s, which this repository does not have even after fetching origin; push that branch from the origin host and try again, or pick a ref with -teleport-ref", want)}
		}
	}
	head, err := p.git(ctx, root, "rev-parse", "HEAD")
	if err == nil && head == sha && !p.force {
		return out, nil
	}

	dir, branch, err := p.addWorktree(ctx, root, sha, og)
	if err != nil {
		return out, err
	}
	out.Dir, out.Worktree = dir, true
	if branch != "" {
		out.Notices = append(out.Notices, fmt.Sprintf("this checkout is not at the commit the session was at, so it continues in a worktree on branch %s", branch))
	} else {
		out.Notices = append(out.Notices, "this checkout is not at the commit the session was at, so it continues in a detached worktree")
	}
	return out, nil
}

func (p plan) say(s string) {
	if p.progress != nil {
		p.progress(s)
	}
}

// resolve returns the full commit id for want, or an error when git has no such
// commit. --end-of-options keeps a ref from being read as an option.
func (p plan) resolve(ctx context.Context, root, want string) (string, error) {
	sha, err := p.git(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", want+"^{commit}")
	if err != nil || !shaShape.MatchString(sha) && len(sha) != 64 {
		return "", errors.New("no such commit")
	}
	return sha, nil
}

// fetch brings origin's refs in, the origin session's branch first. Failure is
// not an error here: the caller looks for the commit again and refuses if it is
// still missing.
func (p plan) fetch(ctx context.Context, root, branch string) {
	if branch != "" && refShape.MatchString(branch) {
		if _, err := p.git(ctx, root, "fetch", "--no-tags", "--quiet", "origin", "--", branch); err == nil {
			return
		}
	}
	_, _ = p.git(ctx, root, "fetch", "--no-tags", "--quiet", "origin")
}

// addWorktree makes a worktree at sha under the worktrees directory, on a new
// branch named for the origin's when that name is usable, else detached.
func (p plan) addWorktree(ctx context.Context, root, sha string, og originGit) (dir, branch string, err error) {
	base := filepath.Join(p.worktree, config.WorkdirKey(root))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", "", fmt.Errorf("create the worktrees directory: %w", err)
	}
	dir = filepath.Join(base, "tp-"+p.newID[:8])
	argv := []string{"worktree", "add"}
	if name := "teleport/" + slug(og.Branch, 120) + "-" + p.newID[:8]; og.Branch != "" && !og.Detached &&
		forge.ValidBranchName(ctx, p.run, root, name) == nil && !forge.BranchExists(ctx, p.run, root, name) {
		branch = name
		argv = append(argv, "-b", branch)
	} else {
		argv = append(argv, "--detach")
	}
	argv = append(argv, "--", dir, sha)
	if _, err := p.git(ctx, root, argv...); err != nil {
		return "", "", fmt.Errorf("create the worktree: %s", forge.CleanErr(err))
	}
	return dir, branch, nil
}

// slug reduces a branch name to the characters a new branch name can safely
// carry, folding anything else (including a slash) to a dash.
func slug(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if max > 0 && b.Len() >= max {
			break
		}
	}
	return strings.Trim(b.String(), "-.")
}

// remove takes a worktree this plan made back out, best effort.
func (p plan) remove(ctx context.Context, root, dir string) {
	_, _ = p.git(ctx, root, "worktree", "remove", "--force", "--", dir)
}
