package rc

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/repos"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/trustgate"
)

// A Software Factory worker or crew request may name the GitHub repository it
// is for: Dispatch.Clone, with or without a Cwd. With agents.clone on (the
// default) and without `belai rc -no-clone`, the daemon advertises canClone.
// Such a request adopts a checkout this host already has, or clones
// <WorktreesDir>/repos/<owner>/<name> once, and runs on the designated branch
// in the checkout or in a worktree of it (adopt.go). Cloning goes through the
// repository engine `belai repo sync` uses (internal/repos: git as an argv,
// hooks off, no prompts, https and ssh only). The credential is the GitHub
// CLI's own, through its git credential helper, as for a push or a pull
// request: no token is stored, put in a URL or passed as an argument. A public
// repository fetches without gh. The directory is trusted for the directory
// only, offered with its remote, advertised on the next heartbeat, and the
// workers start there. A failure refuses the request with the reason.

// cloneTimeout bounds one clone request, the default branch lookup included.
const cloneTimeout = 15 * time.Minute

// cloneOwnerRE and cloneNameRE are the owner and name a clone request may
// carry: GitHub's own characters, never a leading dash or a dot segment.
var (
	cloneOwnerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)
	cloneNameRE  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,99}$`)
)

// Cloner clones or reuses owner/name at ref ("" is the remote's default
// branch) and returns the checkout, trusted for the directory. The error text
// is the refusal reason.
type Cloner func(ctx context.Context, owner, name, ref string) (string, error)

// ParseClone checks a clone request and returns its owner and name, or false.
func ParseClone(c *sessionsync.DispatchClone) (owner, name string, ok bool) {
	if c == nil {
		return "", "", false
	}
	owner, name, found := strings.Cut(strings.TrimSuffix(strings.TrimSpace(c.Repo), ".git"), "/")
	if !found || !cloneOwnerRE.MatchString(owner) || !cloneNameRE.MatchString(name) || name == "." || name == ".." {
		return "", "", false
	}
	if c.Ref != "" && !validBranch(c.Ref) {
		return "", "", false
	}
	return strings.ToLower(owner), strings.ToLower(name), true
}

// cloneSettingOn reports whether the user's agents.clone is on. It reads the
// global settings and fails closed.
func cloneSettingOn() bool {
	s, err := config.LoadGlobal()
	return err == nil && s.AgentsCloneEnabled()
}

// canClone is what the daemon advertises as canClone.
func (d *Daemon) canClone() bool {
	return !d.o.NoClone && d.o.CloneEnabled != nil && d.o.CloneEnabled()
}

// cloneThenStart answers a worker or crew request that names its repository:
// it adopts or clones a checkout and prepares the directory for the designated
// branch (one request at a time), offers it and starts the workers there, or
// refuses with the reason.
func (d *Daemon) cloneThenStart(ctx context.Context, r sessionsync.Dispatch, ack func(ctx context.Context, id, status, sid, reason string)) {
	refuse := func(reason string) {
		d.logf("refused %s %s%s for %s: %s", r.Kind, r.Profile, r.Crew, cloneLabel(r.Clone), reason)
		ack(ctx, r.ID, sessionsync.DispatchRefused, "", reason)
	}
	cwd, why, reason := d.cloneDir(ctx, r.Clone, r.Cwd)
	if reason != "" {
		refuse(reason)
		return
	}
	r.Cwd = cwd
	report, reason := d.startWorkers(r)
	if reason != "" {
		refuse(reason)
		return
	}
	d.logf("started %s %s%s in %s", r.Kind, r.Profile, r.Crew, r.Cwd)
	// clip keeps the end, so the workers' own report survives a long reason.
	ack(ctx, r.ID, sessionsync.DispatchStarted, "", clip(why+"\n"+report))
}

// cloneDir returns the offered directory a clone request's workers run in
// (adopt.go): the adopted checkout itself, or a worktree of it for the
// designated branch. hint is the request's cwd, "" for none. The second result
// says what was chosen and why; the third is the refusal reason, "" on
// success.
func (d *Daemon) cloneDir(ctx context.Context, c *sessionsync.DispatchClone, hint string) (string, string, string) {
	if !d.canClone() {
		return "", "", "this host does not clone repositories (agents.clone is off or belai rc runs with -no-clone)"
	}
	owner, name, ok := ParseClone(c)
	if !ok {
		return "", "", "that is not a repository to clone"
	}
	slug := owner + "/" + name
	if hint != "" {
		// Only an offered directory is a hint; anything else is ignored.
		if p, ok := Allowed(d.dirs(), hint); ok {
			hint = p
		} else {
			hint = ""
		}
	}
	select {
	case d.cloneSlot <- struct{}{}:
	case <-ctx.Done():
		return "", "", "belai rc is stopping"
	}
	defer func() { <-d.cloneSlot }()
	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	res, err := d.adopt.adopt(cctx, adoptRequest{Owner: owner, Name: name, Ref: c.Ref, Hint: hint, Offered: d.dirs(), CanClone: true})
	if err != nil {
		return "", "", clip(err.Error())
	}
	if res.Cloned && d.offerDir(Dir{Path: res.Clone, Name: filepath.Base(res.Clone), Source: SourceTrusted}) {
		d.logf("cloned %s into %s and offered it", slug, res.Clone)
	}
	d.offerDir(Dir{Path: res.Path, Name: filepath.Base(res.Path), Source: SourceTrusted})
	why := sanitize.Line(res.Why, 600)
	d.logf("%s: %s", slug, why)
	return res.Path, why, ""
}

// cloneLabel names a clone request for the log, cleaned.
func cloneLabel(c *sessionsync.DispatchClone) string {
	if owner, name, ok := ParseClone(c); ok {
		return owner + "/" + name
	}
	return "an invalid repository"
}

// cloneItemName is the repository's name in the repos engine: owner-name,
// lower case, at most 64 characters.
func cloneItemName(owner, name string) string {
	n := strings.ToLower(owner + "-" + strings.TrimLeft(name, "._"))
	if len(n) > 64 {
		n = n[:64]
	}
	return strings.TrimRight(n, ".-_")
}

// cloneRepo is the daemon's Cloner: <WorktreesDir>/repos/<owner>/<name> through
// the repos engine, with the GitHub CLI's credential when it is signed in, then
// trusted for the directory.
func cloneRepo(ctx context.Context, owner, name, ref string) (string, error) {
	root, err := config.WorktreesDir()
	if err != nil {
		return "", err
	}
	opts := repos.Options{ReposDir: filepath.Join(root, "repos"), GitHubCLI: true}
	return cloneWith(ctx, owner, name, ref, opts, func(dir string) error { return trustgate.Grant(dir, nil) })
}

// cloneWith is cloneRepo with its engine options and trust grant replaceable
// in tests.
func cloneWith(ctx context.Context, owner, name, ref string, opts repos.Options, trust func(string) error) (string, error) {
	r := config.GitRepo{
		Name: cloneItemName(owner, name), URL: "https://github.com/" + owner + "/" + name + ".git",
		Visibility: "public", Auth: "none", Dir: owner + "/" + name,
		Refs: []config.GitRef{{Kind: config.GitRefBranch, Name: "main"}},
	}
	if ref == "" {
		b, err := repos.DefaultBranch(ctx, r, opts)
		if err != nil {
			return "", err
		}
		ref = b
	}
	r.Refs[0].Name = ref
	res, err := repos.Sync(ctx, r, opts)
	if err != nil {
		return "", err
	}
	if res.Path == "" {
		return "", errors.New("the clone has no path")
	}
	if err := trust(res.Path); err != nil {
		return "", err
	}
	return res.Path, nil
}
