package projectregistry

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Found reports one discovered .vulnetix directory.
type Found struct {
	Path string
}

// SweepOptions governs the filesystem sweep.
type SweepOptions struct {
	Roots      []string
	MaxDepth   int
	Budget     time.Duration
	MaxResults int
	SkipDirs   []string
}

// DefaultSkipDirs are directories that should not be descended into.
var DefaultSkipDirs = []string{
	"node_modules", "vendor", "target", "dist", "build", ".venv",
}

// Sweep walks Roots looking for directories named `.vulnetix`. It never
// errors; progress is reported on out.
func Sweep(ctx context.Context, opts SweepOptions, out chan<- Found) {
	defer close(out)

	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 6
	}
	if opts.Budget <= 0 {
		opts.Budget = 20 * time.Second
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 2000
	}

	deadline := time.Now().Add(opts.Budget)
	skip := map[string]bool{}
	for _, d := range DefaultSkipDirs {
		skip[d] = true
	}
	for _, d := range opts.SkipDirs {
		skip[d] = true
	}

	// Determine system roots to discard.
	hardSkip := map[string]bool{}
	if runtime.GOOS != "windows" {
		for _, p := range []string{"/proc", "/sys", "/dev", "/run"} {
			hardSkip[p] = true
		}
		home, _ := os.UserHomeDir()
		if home != "" {
			hardSkip[filepath.Join(home, "Library")] = true
			hardSkip[filepath.Join(home, ".Trash")] = true
			hardSkip[filepath.Join(home, "snap")] = true
		}
	}

	seen := map[string]bool{}
	for _, root := range opts.Roots {
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if hardSkip[absRoot] {
			continue
		}
		_ = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if ctx.Err() != nil || time.Now().After(deadline) {
				return fs.SkipAll
			}
			if d.IsDir() {
				rel, _ := filepath.Rel(absRoot, path)
				depth := 0
				if rel != "." {
					depth = len(strings.Split(rel, string(filepath.Separator)))
				}
				if depth > opts.MaxDepth {
					return fs.SkipDir
				}
				// Skip hard system dirs no matter the root.
				if hardSkip[path] {
					return fs.SkipDir
				}
				// Skip dot-directories and known dependency/output dirs.
				name := d.Name()
				if name != ".vulnetix" && strings.HasPrefix(name, ".") {
					return fs.SkipDir
				}
				if skip[name] {
					return fs.SkipDir
				}
				// Skip symlinked directories.
				if d.Type()&fs.ModeSymlink != 0 {
					return fs.SkipDir
				}
				if name == ".vulnetix" {
					parent := filepath.Dir(path)
					if !seen[parent] {
						seen[parent] = true
						select {
						case out <- Found{Path: parent}:
						case <-ctx.Done():
							return fs.SkipAll
						}
					}
					return fs.SkipDir
				}
				return nil
			}
			return nil
		})
	}
}
