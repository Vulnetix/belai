package rc

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// The git facts a directory's advertisement carries, so the website can tie the
// directory to the same repository its scans, sandboxes and sessions name. They
// are read from the repository's own files without running git, and only values
// of an identifier shape survive: a remote with credentials keeps none of them
// (forge.ParseRemote drops userinfo), and anything else is left out.

var (
	slugRE   = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+){1,7}$`)
	hostRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
)

// dirGit fills the git facts of an rc directory, or leaves them empty when the
// directory is not a checkout or its origin is not a forge address.
func dirGit(dir sessionsync.RCDir) sessionsync.RCDir {
	info, ok := gitinfo.Detect(dir.Path)
	if !ok {
		return dir
	}
	if rem, ok := forge.ParseRemote(gitinfo.OriginURL(dir.Path)); ok && slugRE.MatchString(rem.Slug()) && hostRE.MatchString(rem.Host) {
		dir.Remote, dir.Host, dir.Provider = rem.Slug(), rem.Host, rem.Kind
	}
	if branchRE.MatchString(info.Branch) && !strings.Contains(info.Branch, "..") {
		dir.Branch = info.Branch
	}
	if b := originHead(info.Root); branchRE.MatchString(b) && !strings.Contains(b, "..") {
		dir.DefaultBranch = b
	}
	return dir
}

// originHead reads refs/remotes/origin/HEAD (the default branch clone recorded),
// or "" when the clone did not record one.
func originHead(root string) string {
	gitDir := filepath.Join(root, ".git")
	if data, err := os.ReadFile(gitDir); err == nil {
		// A linked worktree: the shared refs live in the main repository.
		line := strings.TrimSpace(string(data))
		if p, ok := strings.CutPrefix(line, "gitdir: "); ok {
			gitDir = strings.TrimSpace(p)
			if c, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common := strings.TrimSpace(string(c))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
				gitDir = common
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "refs", "remotes", "origin", "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: refs/remotes/origin/")
	if !ok {
		return ""
	}
	return ref
}
