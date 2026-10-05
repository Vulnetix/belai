package replay

import (
	"os"
	"path/filepath"
)

// missing reports whether the worktree has no entry at rel.
func missing(dir, rel string) (bool, error) {
	_, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(rel)))
	return os.IsNotExist(err), err
}
