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
