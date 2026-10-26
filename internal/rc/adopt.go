package rc

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/repos"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/trustgate"
)

// A Software Factory worker or crew request that carries Dispatch.Clone runs in
// a checkout of that repository which this host adopts, and on the request's
// designated branch (Clone.Ref; empty is the default branch). The rules, in
// order (docs/remote-control.md#cloning-a-repository-for-the-software-factory):
//
//  1. Adopt. The candidates are the offered directories whose origin remote is
//     the repository. A primary checkout (its .git is a directory) wins over a
//     linked worktree; among primaries the request's cwd wins when it is one.
//     Then Belai's own clone at <WorktreesDir>/repos/<owner>/<name>, offered or
//     not. Then the main repository of an offered linked worktree. With none,
//     the default branch is cloned once into Belai's clone and later requests
//     adopt it.
//  2. Fetch the default and the designated branch from origin.
//  3. Decide (decideAdopt, pure). The clone itself is used only when the
//     designated branch is the default branch, the clone has it checked out
//     with no change to a tracked file, and it is level with origin or can be
//     fast-forwarded. Everything else runs in a worktree of the clone under
//     <WorktreesDir>/<config.WorkdirKey(clone)>/rc-<branch>[-n]: a clean
//     worktree that already has the branch is reused, the default branch is a
//     detached worktree (it is never checked out twice), and another branch is
//     the local branch, else a branch tracking origin's, else a new branch from
//     the default. A local branch is only ever fast-forwarded, never reset.
//  4. Trust the directory, offer it, start the workers there.
//
// Git is the repos engine's: hooks off, no prompts, the scrubbed environment,
// https and ssh only, and the GitHub CLI's credential for a fetch.

// rel is how a checkout's commit stands against the commit it follows.
type rel int

const (
	relNone     rel = iota // not compared (nothing to compare with)
	relSame                // the same commit
	relBehind              // an ancestor: it can be fast-forwarded
	relAhead               // it has commits the other lacks
	relDiverged            // both have commits the other lacks
)

// adoptWorktree is a linked worktree of the clone, as the decision sees it.
type adoptWorktree struct {
	Path string
	// Branch is the checked out branch, "" when HEAD is detached.
	Branch string
	Dirty  bool
	// Rel is HEAD against origin's designated branch for a worktree on that
	// branch, and against the detached target (detachTarget) for one of ours
	// that is detached.
	Rel rel
	// Ours is a worktree this adoption made: <Base>/rc-<slug>[-n].
	Ours bool
}

// adoptFacts are what the executor read from the clone, after the fetch.
type adoptFacts struct {
	Clone      string
	Default    string // origin's default branch
	Designated string // the branch the workers run on
	Branch     string // the branch the clone has checked out, "" when detached
	Dirty      bool   // a tracked file in the clone has a change
	Rel        rel    // the clone's HEAD against origin/<Default>, when Branch == Default
	Local      bool   // refs/heads/<Designated> exists
	LocalRel   rel    // that branch against origin/<Designated>, when both exist
	Remote     bool   // origin has <Designated>
	Worktrees  []adoptWorktree
	Base       string   // <WorktreesDir>/<WorkdirKey(Clone)>
	Taken      []string // names already present in Base
	// Stale is why origin could not be fetched ("" when it was): the facts are
	// the refs as last fetched.
	Stale string
	// Fallback: Designated is not the default branch, is not here (neither a
	// local branch nor a fetched one) and origin could not be asked, so the
	// workers run at the default branch's last fetched tip instead.
	Fallback bool
}

type adoptAction int

const (
	actUseClone adoptAction = iota + 1
	actReuse
	actNew
)

// adoptPlan is what the decision chose.
type adoptPlan struct {
	Action adoptAction
	// Path is the directory the workers run in.
	Path string
	// FastForward is the ref the directory moves forward to first, "" for none:
	// a fast-forward merge on a branch, a detached checkout when Detach is set.
	FastForward string
	// Detach makes (or keeps) the worktree's HEAD detached at Start.
	Detach bool
	// Branch is the branch a new worktree checks out (not detached).
	Branch string
	// Start is a new worktree's commit: the detached target, or the base of a
	// new branch.
	Start string
	// NewBranch creates Branch at Start; Track sets its upstream to Start.
	NewBranch, Track bool
	// Why says, in plain words, what was chosen and why.
	Why string
	// Err, when set, refuses the request.
	Err string
}

const (
	remoteRef = "refs/remotes/origin/"
	headsRef  = "refs/heads/"
	// maxRCWorktrees bounds rc-<slug>-n for one branch.
	maxRCWorktrees = 32
)

// branchSlug folds a branch to one path segment.
func branchSlug(b string) string {
	return plainSegment(strings.ReplaceAll(b, "/", "-"))
}

// rcName is the n-th worktree name for slug: rc-<slug>, rc-<slug>-2, ...
func rcName(slug string, n int) string {
	if n <= 1 {
		return "rc-" + slug
	}
	return "rc-" + slug + "-" + strconv.Itoa(n)
}

// isRCName reports whether name is rc-<slug> or rc-<slug>-<n>.
func isRCName(name, slug string) bool {
	if name == "rc-"+slug {
		return true
	}
	n, ok := strings.CutPrefix(name, "rc-"+slug+"-")
	if !ok {
		return false
	}
	i, err := strconv.Atoi(n)
	return err == nil && i >= 2 && strconv.Itoa(i) == n
}

// detachTarget is the commit a detached worktree for f sits on: origin's
// default branch for the default branch, else the designated branch at
// origin's tip when the local branch is level or behind it (or absent), else
// the local branch.
func detachTarget(f adoptFacts) string {
	if f.Designated == f.Default || f.Fallback {
		return remoteRef + f.Default
	}
	if f.Remote && (!f.Local || f.LocalRel == relSame || f.LocalRel == relBehind) {
		return remoteRef + f.Designated
	}
	return headsRef + f.Designated
}

// decideAdopt chooses the directory the workers run in. It is pure: every
// fact comes in f.
func decideAdopt(f adoptFacts) adoptPlan {
	def, want := f.Default, f.Designated
	clone := f.Clone
	var reason string
	switch {
	case want != def:
		reason = fmt.Sprintf("the branch is %s, not the default %s", want, def)
	case f.Branch != def && f.Branch == "":
		reason = fmt.Sprintf("%s has a detached HEAD, not %s", clone, def)
	case f.Branch != def:
		reason = fmt.Sprintf("%s is on %s, not %s", clone, f.Branch, def)
	case f.Dirty:
		reason = fmt.Sprintf("%s is dirty", def)
	case f.Rel == relSame:
		return adoptPlan{Action: actUseClone, Path: clone, Why: fmt.Sprintf("%s is clean and level with origin, so the clone is used as it is", def)}
	case f.Rel == relBehind:
		return adoptPlan{Action: actUseClone, Path: clone, FastForward: remoteRef + def,
			Why: fmt.Sprintf("%s is clean and behind origin, so the clone is fast-forwarded and used", def)}
	case f.Rel == relAhead:
		reason = fmt.Sprintf("%s has local commits that are not on origin", def)
	case f.Rel == relDiverged:
		reason = fmt.Sprintf("%s has diverged from origin", def)
	default:
		reason = fmt.Sprintf("%s could not be compared with origin", def)
	}
	slug := branchSlug(want)
	if f.Fallback {
		slug = branchSlug(def)
		reason = fmt.Sprintf("%s is not here and could not be fetched, so the workers run at %s as last fetched", want, def)
	}
	detached := func(why string) adoptPlan {
		for _, wt := range f.Worktrees {
			if wt.Ours && wt.Branch == "" && !wt.Dirty && (wt.Rel == relSame || wt.Rel == relBehind) {
				p := adoptPlan{Action: actReuse, Path: wt.Path, Detach: true, Why: why + ", so the detached worktree at " + wt.Path + " is reused"}
				if wt.Rel == relBehind {
					p.FastForward = detachTarget(f)
					p.Why += " and moved to origin's tip"
				}
				return p
			}
		}
		path, ok := freeName(f, slug)
		if !ok {
			return adoptPlan{Err: "there are too many worktrees for " + want + " under " + f.Base}
		}
		return adoptPlan{Action: actNew, Path: path, Detach: true, Start: detachTarget(f), Why: why + ", so a detached worktree at " + path}
	}
	if want == def || f.Fallback {
		return detached(reason)
	}
	busy := ""
	if f.Branch == want {
		busy = clone
	}
	for _, wt := range f.Worktrees {
		if wt.Branch != want {
			continue
		}
		if wt.Dirty {
			busy = wt.Path
			continue
		}
		p := adoptPlan{Action: actReuse, Path: wt.Path, Why: reason + "; the worktree at " + wt.Path + " has it checked out and is clean, so it is reused"}
		if f.Remote && wt.Rel == relBehind {
			p.FastForward = remoteRef + want
			p.Why += " and fast-forwarded"
		}
		return p
	}
	if busy != "" {
		// A branch is checked out in one worktree only, and that one is the
		// user's clone or has changes: work beside it at the branch's tip.
		return detached(reason + "; " + want + " is checked out at " + busy)
	}
	path, ok := freeName(f, slug)
	if !ok {
		return adoptPlan{Err: "there are too many worktrees for " + want + " under " + f.Base}
	}
	switch {
	case f.Local:
		p := adoptPlan{Action: actNew, Path: path, Branch: want, Why: reason + ", so a worktree at " + path + " on the local branch"}
		switch {
		case f.Remote && f.LocalRel == relBehind:
			p.FastForward = remoteRef + want
			p.Why += ", fast-forwarded to origin"
		case f.Remote && (f.LocalRel == relAhead || f.LocalRel == relDiverged):
			p.Why += ", left as it is (it has commits that are not on origin)"
		}
		return p
	case f.Remote:
		return adoptPlan{Action: actNew, Path: path, Branch: want, Start: remoteRef + want, NewBranch: true, Track: true,
			Why: reason + ", so a worktree at " + path + " tracking origin's " + want}
	default:
		return adoptPlan{Action: actNew, Path: path, Branch: want, Start: remoteRef + def, NewBranch: true,
			Why: reason + "; origin has no " + want + ", so a worktree at " + path + " with a new branch from " + def}
	}
}

// freeName is the first rc-<slug>[-n] path under f.Base that is not taken.
func freeName(f adoptFacts, slug string) (string, bool) {
	taken := map[string]bool{}
	for _, n := range f.Taken {
		taken[n] = true
	}
	for n := 1; n <= maxRCWorktrees; n++ {
		if name := rcName(slug, n); !taken[name] {
			return filepath.Join(f.Base, name), true
		}
	}
	return "", false
}

// adoptCand is an offered directory whose origin is the repository.
type adoptCand struct {
	Path    string
	Primary bool
}

// pickClone chooses the checkout to adopt: a primary offered checkout (hint
// first when it is one), then Belai's clone, then the main repository of an
// offered linked worktree. It returns "" when there is none.
func pickClone(offered []adoptCand, hint, belai string, mains []string) (string, string) {
	for _, c := range offered {
		if c.Primary && hint != "" && c.Path == hint {
			return c.Path, "the request's directory"
		}
	}
	for _, c := range offered {
		if c.Primary {
			return c.Path, "an offered checkout"
		}
	}
	if belai != "" {
		return belai, "Belai's clone"
	}
	if len(mains) > 0 {
		return mains[0], "the repository an offered worktree belongs to"
	}
	return "", ""
}

// adopter finds or clones the checkout for a clone request and prepares the
// directory the workers run in.
type adopter struct {
	// opts are the repos engine's options; tests point the url at a local
	// repository through them.
	opts repos.Options
	// root is the worktrees directory (config.WorktreesDir).
	root func() (string, error)
	// trust grants trust for one directory (trustgate.Grant).
	trust func(dir string) error
	// clone clones owner/name at its default branch and returns the checkout
	// (the daemon's Cloner).
	clone Cloner
}

func newAdopter(clone Cloner) *adopter {
	return &adopter{
		opts:  repos.Options{GitHubCLI: true},
		root:  config.WorktreesDir,
		trust: func(dir string) error { return trustgate.Grant(dir, nil) },
		clone: clone,
	}
}

// adoptRequest is one clone request, checked.
type adoptRequest struct {
	Owner, Name string
	// Ref is the designated branch, "" for the default.
	Ref string
	// Hint is the request's cwd, normalised, or "".
	Hint     string
	Offered  []Dir
	CanClone bool
}

// adoptResult is where the workers run and why.
type adoptResult struct {
	Path string
	// Clone is the adopted checkout; Cloned is true when this request made it.
	Clone  string
	Cloned bool
	Why    string
}

func (a *adopter) git(ctx context.Context, dir string, args ...string) (string, error) {
	return repos.Git(ctx, a.opts, dir, args...)
}

// isPrimary reports whether dir is a repository's main checkout: its .git is a
// directory, not a file pointing at another repository.
func isPrimary(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// remoteIs reports whether dir is a checkout root whose origin is slug.
func remoteIs(dir, slug string) bool {
	info, ok := gitinfo.Detect(dir)
	if !ok {
		return false
	}
	if r, err := Normalize(info.Root); err != nil || r != dir {
		return false
	}
	rd := dirGit(sessionsync.RCDir{Path: dir})
	return rd.Remote != "" && strings.EqualFold(rd.Remote, slug)
}

// mainOf is the main checkout of the linked worktree at dir, or "".
func (a *adopter) mainOf(ctx context.Context, dir string) string {
	c, err := a.git(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil || c == "" {
		return ""
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	c = resolvePath(c)
	if filepath.Base(c) != ".git" {
		return ""
	}
	m, err := Normalize(filepath.Dir(c))
	if err != nil || !isPrimary(m) {
		return ""
	}
	return m
}

// adopt picks or clones the checkout, fetches, decides and prepares the
// directory. The error text is the refusal reason.
func (a *adopter) adopt(ctx context.Context, q adoptRequest) (adoptResult, error) {
	slug := q.Owner + "/" + q.Name
	root, err := a.root()
	if err != nil {
		return adoptResult{}, fmt.Errorf("the worktrees directory: %v", err)
	}
	root = filepath.Clean(root)
	var offered []adoptCand
	var linked []string
	for _, d := range q.Offered {
		if !remoteIs(d.Path, slug) {
			continue
		}
		if isPrimary(d.Path) {
			offered = append(offered, adoptCand{Path: d.Path, Primary: true})
		} else {
			linked = append(linked, d.Path)
		}
	}
	belai := ""
	if p, err := Normalize(filepath.Join(root, "repos", q.Owner, q.Name)); err == nil && isPrimary(p) && remoteIs(p, slug) {
		belai = p
	}
	var mains []string
	if len(offered) == 0 && belai == "" {
		for _, l := range linked {
			if m := a.mainOf(ctx, l); m != "" && remoteIs(m, slug) {
				mains = append(mains, m)
			}
		}
	}
	res := adoptResult{}
	clone, from := pickClone(offered, q.Hint, belai, mains)
	if clone == "" {
		if !q.CanClone {
			return res, fmt.Errorf("this host has no checkout of %s and does not clone repositories (agents.clone is off or belai rc runs with -no-clone)", slug)
		}
		p, err := a.clone(ctx, q.Owner, q.Name, "")
		if err != nil {
			return res, fmt.Errorf("could not clone %s: %v", slug, err)
		}
		if clone, err = Normalize(p); err != nil {
			return res, fmt.Errorf("could not clone %s: %v", slug, err)
		}
		res.Cloned, from = true, "a new clone"
	}
	res.Clone = clone
	facts, err := a.read(ctx, clone, q, root)
	if err != nil {
		return res, err
	}
	plan := decideAdopt(facts)
	if plan.Err != "" {
		return res, errors.New(plan.Err)
	}
	if err := a.apply(ctx, root, facts, plan); err != nil {
		return res, err
	}
	path, err := Normalize(plan.Path)
	if err != nil {
		return res, fmt.Errorf("the directory %s: %v", plan.Path, err)
	}
	if err := a.trust(path); err != nil {
		return res, fmt.Errorf("trust %s: %v", path, err)
	}
	verb := "adopted"
	if res.Cloned {
		verb = "cloned"
	}
	res.Path = path
	res.Why = fmt.Sprintf("%s %s (%s); ", verb, clone, from)
	if facts.Stale != "" {
		res.Why += "could not fetch origin (" + facts.Stale + "); using the last fetched refs; "
	}
	res.Why += plan.Why
	return res, nil
}

// fetchReason is a fetch failure in a few plain words for the why line: git's
// last line, without the "fatal:" prefix, cleaned and clipped.
func fetchReason(err error) string {
	msg := forge.CleanErr(err)
	if i := strings.LastIndex(msg, ": fatal: "); i >= 0 {
		msg = msg[i+len(": fatal: "):]
	}
	msg = strings.TrimPrefix(strings.TrimPrefix(msg, "fetch: "), "fatal: ")
	if msg == "" {
		msg = "no answer"
	}
	return sanitize.Clip(msg, 100)
}

// localDefault guesses origin's default branch from what was last fetched,
// when origin cannot be asked: the clone's own branch, then main, then
// master, whichever has a fetched origin branch. "" when none has.
func (a *adopter) localDefault(ctx context.Context, clone string, has func(string) bool) string {
	cands := []string{}
	if b, err := a.git(ctx, clone, "symbolic-ref", "-q", "--short", "HEAD"); err == nil && validBranch(b) {
		cands = append(cands, b)
	}
	for _, b := range append(cands, "main", "master") {
		if has(remoteRef + b) {
			return b
		}
	}
	return ""
}

// read fetches and gathers the facts the decision needs.
func (a *adopter) read(ctx context.Context, clone string, q adoptRequest, root string) (adoptFacts, error) {
	f := adoptFacts{Clone: clone}
	url := gitinfo.OriginURL(clone)
	has := func(ref string) bool {
		_, err := a.git(ctx, clone, "rev-parse", "--verify", "-q", ref+"^{commit}")
		return err == nil
	}
	// A failed fetch never refuses a request on a checkout this host already
	// has: the decision is made on the refs as they were last fetched, and the
	// why line says so.
	stale := func(err error) {
		if f.Stale == "" {
			f.Stale = fetchReason(err)
		}
	}
	def := originHead(clone)
	if !validBranch(def) {
		r := config.GitRepo{Name: cloneItemName(q.Owner, q.Name), URL: "https://github.com/" + q.Owner + "/" + q.Name + ".git",
			Visibility: "public", Auth: "none", Dir: q.Owner + "/" + q.Name,
			Refs: []config.GitRef{{Kind: config.GitRefBranch, Name: "main"}}}
		b, err := repos.DefaultBranch(ctx, r, a.opts)
		if err != nil {
			stale(err)
			b = a.localDefault(ctx, clone, has)
			if b == "" {
				return f, fmt.Errorf("could not read the default branch of %s/%s, and %s has no fetched one: %v", q.Owner, q.Name, clone, err)
			}
		}
		def = b
	}
	if !validBranch(def) {
		return f, errors.New("origin's default branch has an odd name")
	}
	want := q.Ref
	if want == "" {
		want = def
	}
	f.Default, f.Designated = def, want
	if f.Stale == "" {
		if err := repos.Fetch(ctx, a.opts, clone, url, def); err != nil {
			stale(err)
		}
	}
	if !has(remoteRef + def) {
		if f.Stale != "" {
			return f, fmt.Errorf("could not fetch %s into %s (%s), and it was never fetched there", def, clone, f.Stale)
		}
		return f, fmt.Errorf("%s has no %s after the fetch", clone, remoteRef+def)
	}
	if f.Stale == "" && originHead(clone) == "" && within(resolvePath(filepath.Join(root, "repos")), clone) {
		// Belai's own clone (repos.Sync does not record origin's HEAD): record it,
		// so the next request reads the default branch without asking origin.
		_, _ = a.git(ctx, clone, "symbolic-ref", "refs/remotes/origin/HEAD", remoteRef+def)
	}
	f.Remote = true
	if want != def {
		fetched := false
		if f.Stale == "" {
			err := repos.Fetch(ctx, a.opts, clone, url, want)
			switch {
			case err == nil:
				fetched = true
			case strings.Contains(err.Error(), "couldn't find remote ref"):
				// Origin answered: it has no such branch.
			default:
				stale(err)
			}
		}
		switch {
		case fetched:
			f.Remote = true
		case f.Stale != "":
			// Origin did not answer: what was last fetched stands.
			f.Remote = has(remoteRef + want)
		default:
			f.Remote = false
		}
	}
	if b, err := a.git(ctx, clone, "symbolic-ref", "-q", "--short", "HEAD"); err == nil {
		f.Branch = b
	}
	st, err := a.git(ctx, clone, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return f, fmt.Errorf("could not read the status of %s: %v", clone, err)
	}
	f.Dirty = st != ""
	if f.Branch == def {
		f.Rel = a.compare(ctx, clone, "HEAD", remoteRef+def)
	}
	if _, err := a.git(ctx, clone, "rev-parse", "--verify", "-q", headsRef+want); err == nil {
		f.Local = true
		if f.Remote {
			f.LocalRel = a.compare(ctx, clone, headsRef+want, remoteRef+want)
		}
	}
	// A branch that is nowhere here and could not be asked about may well be
	// on origin: never invent it. Work at the default branch's last fetched
	// tip instead.
	f.Fallback = want != def && f.Stale != "" && !f.Local && !f.Remote
	f.Base = filepath.Join(root, config.WorkdirKey(clone))
	if des, err := os.ReadDir(f.Base); err == nil {
		for _, de := range des {
			f.Taken = append(f.Taken, de.Name())
		}
	}
	out, err := a.git(ctx, clone, "worktree", "list", "--porcelain")
	if err != nil {
		return f, fmt.Errorf("could not list the worktrees of %s: %v", clone, err)
	}
	slug := branchSlug(want)
	if f.Fallback {
		slug = branchSlug(def)
	}
	target := detachTarget(f)
	rbase := resolvePath(f.Base)
	for i, wt := range forge.ParseWorktrees(out) {
		if i == 0 || wt.Bare || wt.Prunable {
			continue // the first entry is the main worktree, the clone itself
		}
		p := resolvePath(wt.Path)
		if p == clone {
			continue
		}
		w := adoptWorktree{Path: p, Branch: wt.Branch}
		if wt.Detached {
			w.Branch = ""
		}
		w.Ours = filepath.Dir(p) == rbase && isRCName(filepath.Base(p), slug)
		switch {
		case w.Branch == want && w.Branch != "":
			if f.Remote {
				w.Rel = a.compare(ctx, p, "HEAD", remoteRef+want)
			}
		case w.Branch == "" && w.Ours:
			w.Rel = a.compare(ctx, p, "HEAD", target)
		default:
			continue
		}
		s, err := a.git(ctx, p, "status", "--porcelain", "--untracked-files=no")
		w.Dirty = err != nil || s != ""
		f.Worktrees = append(f.Worktrees, w)
	}
	return f, nil
}

// compare says how a stands against b in dir.
func (a *adopter) compare(ctx context.Context, dir, x, y string) rel {
	xs, err1 := a.git(ctx, dir, "rev-parse", "--verify", "-q", x+"^{commit}")
	ys, err2 := a.git(ctx, dir, "rev-parse", "--verify", "-q", y+"^{commit}")
	if err1 != nil || err2 != nil {
		return relNone
	}
	if xs == ys {
		return relSame
	}
	if _, err := a.git(ctx, dir, "merge-base", "--is-ancestor", xs, ys); err == nil {
		return relBehind
	}
	if _, err := a.git(ctx, dir, "merge-base", "--is-ancestor", ys, xs); err == nil {
		return relAhead
	}
	return relDiverged
}

// apply carries out the plan.
func (a *adopter) apply(ctx context.Context, root string, f adoptFacts, p adoptPlan) error {
	fail := func(what string, err error) error {
		return fmt.Errorf("%s: %s", what, sanitize.Clip(forge.CleanErr(err), 200))
	}
	switch p.Action {
	case actUseClone, actReuse:
		if p.FastForward == "" {
			return nil
		}
		if p.Detach {
			if _, err := a.git(ctx, p.Path, "checkout", "-q", "--detach", p.FastForward, "--"); err != nil {
				return fail("could not move the worktree at "+p.Path, err)
			}
			return nil
		}
		if _, err := a.git(ctx, p.Path, "merge", "-q", "--ff-only", p.FastForward); err != nil {
			return fail("could not fast-forward "+p.Path, err)
		}
		return nil
	case actNew:
	default:
		return errors.New("nothing was chosen")
	}
	if err := a.makeBase(root, f.Base, p.Path); err != nil {
		return err
	}
	var args []string
	switch {
	case p.Detach:
		args = []string{"worktree", "add", "--detach", p.Path, p.Start}
	case p.NewBranch && p.Track:
		args = []string{"worktree", "add", "--track", "-b", p.Branch, p.Path, p.Start}
	case p.NewBranch:
		args = []string{"worktree", "add", "--no-track", "-b", p.Branch, p.Path, p.Start}
	default:
		args = []string{"worktree", "add", p.Path, p.Branch}
	}
	if _, err := a.git(ctx, f.Clone, args...); err != nil {
		return fail("git refused to make the worktree at "+p.Path, err)
	}
	if p.FastForward != "" {
		if _, err := a.git(ctx, p.Path, "merge", "-q", "--ff-only", p.FastForward); err != nil {
			return fail("could not fast-forward "+p.Path, err)
		}
	}
	return nil
}

// makeBase creates base under root and checks that neither it nor the target
// is a symbolic link and that both stay under root, so a worktree never lands
// where a link points.
func (a *adopter) makeBase(root, base, target string) error {
	if !within(root, base) || filepath.Dir(target) != base {
		return errors.New("the worktree would be outside the worktrees directory")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return fmt.Errorf("create the worktrees directory: %v", err)
	}
	cur := root
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return errors.New("the worktree would be outside the worktrees directory")
	}
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; a worktree is never made through one", cur)
		}
		if cur == target {
			return fmt.Errorf("%s already exists", target)
		}
	}
	if !within(resolvePath(root), resolvePath(base)) {
		return errors.New("the worktree would be outside the worktrees directory")
	}
	return nil
}
