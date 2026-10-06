package gitsync

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/forge"
)

// Checks counts a pull request's CI checks by outcome.
type Checks struct {
	Pass    int `json:"pass"`
	Fail    int `json:"fail"`
	Pending int `json:"pending"`
}

// PR is the pull or merge request for the session's branch.
type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	// State is the provider's, lower-case: open, closed, merged, opened.
	State  string `json:"state"`
	URL    string `json:"url"`
	Draft  bool   `json:"draft,omitempty"`
	Checks Checks `json:"checks"`
}

// SyncState is the sync switch and the last attempt, inside Info.
type SyncState struct {
	Enabled bool    `json:"enabled"`
	Last    *Result `json:"last,omitempty"`
}

// Info is what the website shows for a session in a git repository. Every
// string is cleaned third-party text; the remote URL has no credentials.
type Info struct {
	Root     string `json:"root"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	// Worktree is set when the session runs in a linked worktree, not the
	// repository's main checkout; Worktrees counts every checkout.
	Worktree  bool `json:"worktree,omitempty"`
	Worktrees int  `json:"worktrees,omitempty"`
	// Remote is owner/repo, Host its server and Provider github, gitlab or generic.
	Remote   string `json:"remote,omitempty"`
	Host     string `json:"host,omitempty"`
	Provider string `json:"provider,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	Ahead    int    `json:"ahead,omitempty"`
	Behind   int    `json:"behind,omitempty"`
	// Base is origin's default branch (origin/main) and AheadBase/BehindBase
	// how far HEAD is from it.
	Base       string `json:"base,omitempty"`
	AheadBase  int    `json:"aheadBase,omitempty"`
	BehindBase int    `json:"behindBase,omitempty"`
	// Dirty: tracked files have uncommitted changes.
	Dirty bool `json:"dirty,omitempty"`
	PR    *PR  `json:"pr,omitempty"`
	// PRNote says why there is no PR to show (no gh, no remote, detached).
	PRNote string    `json:"prNote,omitempty"`
	Sync   SyncState `json:"sync"`
	At     int64     `json:"at"`
}

// InfoEvery is how often Info re-reads the repository and the provider; a
// call inside the window returns the last reading with the current sync state.
const InfoEvery = 30 * time.Second

// Info describes the repository for the website, or nil when dir is not in
// one. force skips the InfoEvery window (a turn just ended, a sync just ran).
func (h *Hygiene) Info(ctx context.Context, force bool) *Info {
	h.mu.Lock()
	cached, at := h.info, h.infoAt
	h.mu.Unlock()
	if cached != nil && !force && h.now().Sub(at) < InfoEvery {
		return h.withSync(cached)
	}
	snap := forge.Probe(ctx, h.run, exec.LookPath, h.dir)
	if snap.Root == "" {
		return nil
	}
	in := &Info{
		Root: snap.Root, Branch: snap.Branch, Detached: snap.Branch == "",
		Subject: snap.LastSubject, Remote: snap.Remote.Slug(), Host: snap.Remote.Host, Provider: snap.Remote.Kind,
		Upstream: snap.Upstream, Ahead: snap.Ahead, Behind: snap.Behind,
		Worktrees: len(snap.Worktrees), PRNote: snap.PRErr, At: h.now().UnixMilli(),
	}
	if in.PRNote == "" && snap.PR == nil {
		in.PRNote = snap.Reason
	}
	if sha, err := h.git(ctx, forge.ReadTimeout, "rev-parse", "--short=12", "HEAD"); err == nil {
		in.Head = sha
	}
	gitDir, _ := h.git(ctx, forge.ReadTimeout, "rev-parse", "--absolute-git-dir")
	common, _ := h.git(ctx, forge.ReadTimeout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	in.Worktree = gitDir != "" && common != "" && filepath.Clean(gitDir) != filepath.Clean(common)
	if dirty, err := h.git(ctx, forge.ReadTimeout, "status", "--porcelain", "--untracked-files=no"); err == nil {
		in.Dirty = dirty != ""
	}
	if base := h.defaultBase(ctx); base != "" {
		in.Base = base
		if counts, err := h.git(ctx, forge.ReadTimeout, "rev-list", "--left-right", "--count", "HEAD..."+base); err == nil {
			f := strings.Fields(counts)
			if len(f) == 2 {
				in.AheadBase, _ = strconv.Atoi(f[0])
				in.BehindBase, _ = strconv.Atoi(f[1])
			}
		}
	}
	if pr := snap.PR; pr != nil {
		in.PR = &PR{Number: pr.Number, Title: forge.Clean(pr.Title), State: pr.State, URL: pr.URL, Draft: pr.Draft}
		for _, c := range snap.Checks {
			switch c.State {
			case forge.CheckPass:
				in.PR.Checks.Pass++
			case forge.CheckFail:
				in.PR.Checks.Fail++
			case forge.CheckPending:
				in.PR.Checks.Pending++
			}
		}
	}
	h.mu.Lock()
	h.info, h.infoAt = in, h.now()
	h.mu.Unlock()

	return h.withSync(in)
}

// withSync returns a copy of in carrying the current sync switch and last
// attempt, which change without a repository re-read.
func (h *Hygiene) withSync(in *Info) *Info {
	out := *in
	h.mu.Lock()
	out.Sync = SyncState{Enabled: h.enabled, Last: h.last}
	h.mu.Unlock()

	return &out
}
