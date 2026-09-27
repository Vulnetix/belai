// Package gitinfo detects repository context without shelling out.
package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
)

// Info describes the git context of a working directory.
type Info struct {
	Root     string // repository root
	Branch   string // "" when detached
	Head     string // short SHA
	Detached bool
}

// Detect walks up from workdir looking for a .git directory or file.
// It returns the Info and true when a repository is found.
func Detect(workdir string) (Info, bool) {
	root, gitDir := findGit(workdir)
	if root == "" {
		return Info{}, false
	}

	headFile := filepath.Join(gitDir, "HEAD")
	data, err := os.ReadFile(headFile)
	if err != nil {
		return Info{Root: root}, true
	}

	content := strings.TrimSpace(string(data))
	var info Info
	info.Root = root

	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(content, refPrefix) {
		info.Branch = strings.TrimPrefix(content, refPrefix)
	} else {
		info.Detached = true
		if len(content) >= 7 {
			info.Head = content[:7]
		} else {
			info.Head = content
		}
	}

	return info, true
}

// findGit walks up from dir to find a .git entry.
func findGit(dir string) (root, gitDir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", ""
	}
	for {
		gitPath := filepath.Join(abs, ".git")
		fi, err := os.Stat(gitPath)
		if err == nil {
			if fi.IsDir() {
				return abs, gitPath
			}
			// .git file → worktree
			data, err := os.ReadFile(gitPath)
			if err == nil {
				line := strings.TrimSpace(string(data))
				const prefix = "gitdir: "
				if strings.HasPrefix(line, prefix) {
					return abs, strings.TrimSpace(strings.TrimPrefix(line, prefix))
				}
			}
			return abs, gitPath
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		abs = parent
	}
	return "", ""
}

// OriginURL returns the raw remote.origin.url from the repository's config,
// read without shelling out, or "" when there is none. A linked worktree
// reads the main repository's config through its commondir.
func OriginURL(workdir string) string {
	_, gitDir := findGit(workdir)
	if gitDir == "" {
		return ""
	}
	if data, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
		common := strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		gitDir = common
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.EqualFold(strings.ReplaceAll(line, " ", ""), `[remote"origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "url") {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
