package rc

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
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
// carry: GitHub's own characters, never a leading dash or a dot segment. On
// another forge each segment of a group path is an owner.
var (
	cloneOwnerRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,99}$`)
	cloneSegRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	cloneNameRE  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]{0,99}$`)
	cloneHostRE  = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?(:[0-9]{1,5})?$`)
)

// CloneTarget is the repository a clone request names: Owner and Name, and
// for a forge other than github.com its Host, Forge kind and https URL.
type CloneTarget struct {
	Host, Forge, Owner, Name, URL string
}

// Slug is owner/name.
func (t CloneTarget) Slug() string { return t.Owner + "/" + t.Name }

// Label names the target for logs and refusals: owner/name on github.com,
// host/owner/name elsewhere.
func (t CloneTarget) Label() string {
	if t.Host == "" {
		return t.Slug()
	}
	return t.Host + "/" + t.Slug()
}

// cloneURL is the https URL the clone fetches.
func (t CloneTarget) cloneURL() string {
	if t.URL != "" {
		return t.URL
	}
	return "https://github.com/" + t.Owner + "/" + t.Name + ".git"
}

// reposDir is where the checkout lives under <WorktreesDir>/repos:
// owner/name for github.com, host/owner/name elsewhere.
func (t CloneTarget) reposDir() string {
	if t.Host == "" {
		return filepath.Join(t.Owner, t.Name)
	}
	return filepath.Join(strings.ReplaceAll(t.Host, ":", "_"), filepath.FromSlash(t.Owner), t.Name)
}

// Cloner clones or reuses a target at ref ("" is the remote's default
// branch) and returns the checkout, trusted for the directory. The error text
// is the refusal reason.
type Cloner func(ctx context.Context, t CloneTarget, ref string) (string, error)

// ParseClone checks a clone request and returns its owner and name, or false.
func ParseClone(c *sessionsync.DispatchClone) (owner, name string, ok bool) {
	t, ok := ParseCloneTarget(c)
	return t.Owner, t.Name, ok
}

// ParseCloneTarget checks a clone request and returns its target, or false.
// A request for another forge must name a kind Belai knows and an https URL
// that is exactly that host's owner/name; anything else is refused.
func ParseCloneTarget(c *sessionsync.DispatchClone) (CloneTarget, bool) {
	if c == nil {
		return CloneTarget{}, false
	}
	if c.Ref != "" && !validBranch(c.Ref) {
		return CloneTarget{}, false
	}
	repo := strings.TrimSuffix(strings.TrimSpace(c.Repo), ".git")
	if c.Host == "" {
		if c.Forge != "" || c.CloneURL != "" {
			return CloneTarget{}, false
		}
		owner, name, found := strings.Cut(repo, "/")
		if !found || !cloneOwnerRE.MatchString(owner) || !cloneNameRE.MatchString(name) || name == "." || name == ".." {
			return CloneTarget{}, false
		}
		return CloneTarget{Owner: strings.ToLower(owner), Name: strings.ToLower(name)}, true
	}
	host := strings.ToLower(c.Host)
	switch c.Forge {
	case forge.HostGHES, forge.HostGitea, forge.HostForgejo, forge.HostGitLab:
	default:
		return CloneTarget{}, false
	}
	if !cloneHostRE.MatchString(host) || host == "github.com" {
		return CloneTarget{}, false
	}
	i := strings.LastIndexByte(repo, '/')
	if i <= 0 {
		return CloneTarget{}, false
	}
	owner, name := repo[:i], repo[i+1:]
	segs := strings.Split(owner, "/")
	if len(segs) > 8 || !cloneNameRE.MatchString(name) || name == "." || name == ".." {
		return CloneTarget{}, false
	}
	for _, s := range segs {
		if !cloneSegRE.MatchString(s) || strings.Contains(s, "..") {
			return CloneTarget{}, false
		}
	}
	t := CloneTarget{Host: host, Forge: c.Forge, Owner: strings.ToLower(owner), Name: strings.ToLower(name)}
	want := "https://" + host + "/" + t.Owner + "/" + t.Name + ".git"
	if !strings.EqualFold(c.CloneURL, want) {
		return CloneTarget{}, false
	}
	t.URL = want
	return t, true
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
	t, ok := ParseCloneTarget(c)
	if !ok {
		return "", "", "that is not a repository to clone"
	}
	slug := t.Label()
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
	res, err := d.adopt.adopt(cctx, adoptRequest{Target: t, Owner: t.Owner, Name: t.Name, Ref: c.Ref, Hint: hint, Offered: d.dirs(), CanClone: true})
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
	if t, ok := ParseCloneTarget(c); ok {
		return t.Label()
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

// cloneRepo is the daemon's Cloner: <WorktreesDir>/repos/<owner>/<name>
// (host/owner/name off github.com) through the repos engine, then trusted for
// the directory. The credential is the GitHub CLI's own on github.com and
// GHES, and on Gitea, Forgejo and GitLab the token forge.ForgeToken finds for
// the host, read from the environment by git's credential helper.
func cloneRepo(ctx context.Context, t CloneTarget, ref string) (string, error) {
	root, err := config.WorktreesDir()
	if err != nil {
		return "", err
	}
	return cloneWith(ctx, t, ref, cloneOptions(t, filepath.Join(root, "repos")), func(dir string) error { return trustgate.Grant(dir, nil) })
}

// cloneOptions are the repos engine's options for a target.
func cloneOptions(t CloneTarget, reposDir string) repos.Options {
	opts := repos.Options{ReposDir: reposDir, GitHubCLI: true}
	switch t.Forge {
	case forge.HostGitea, forge.HostForgejo, forge.HostGitLab:
		kind := map[string]string{forge.HostGitea: forge.KindGitea, forge.HostForgejo: forge.KindForgejo, forge.HostGitLab: forge.KindGitLab}[t.Forge]
		if _, from := forge.ForgeToken(kind, strings.Split(t.Host, ":")[0]); from != "" {
			opts.GitHubCLI, opts.TokenEnv = false, from
		}
	}
	return opts
}

// cloneWith is cloneRepo with its engine options and trust grant replaceable
// in tests.
func cloneWith(ctx context.Context, t CloneTarget, ref string, opts repos.Options, trust func(string) error) (string, error) {
	itemName := cloneItemName(t.Owner, t.Name)
	if t.Host != "" {
		itemName = cloneItemName(strings.ReplaceAll(t.Host, ":", "-")+"-"+strings.ReplaceAll(t.Owner, "/", "-"), t.Name)
	}
	r := config.GitRepo{
		Name: itemName, URL: t.cloneURL(),
		Visibility: "public", Auth: "none", Dir: filepath.ToSlash(t.reposDir()),
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
