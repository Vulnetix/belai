package locate

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
)

// File is one eligible file.
type File struct {
	// Path is relative to the inventory root, with forward slashes.
	Path string
	Size int64
}

// Inventory is the set of files that may be looked at under one root.
type Inventory struct {
	Root  string
	Files []File
	// Skipped counts what was left out, by reason.
	Skipped map[string]int
	// Truncated is true when MaxFiles stopped the walk.
	Truncated bool
}

// Build walks root and returns its eligible files, in path order. It honours
// .gitignore at every level (a nested repository resets git rules) and
// .ignore, which is applied after and so wins. Symlinks and special files are
// never followed or listed. The walk stops at ctx's deadline or MaxFiles.
func Build(ctx context.Context, root string) (*Inventory, error) {
	abs, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	inv := &Inventory{Root: abs, Skipped: map[string]int{}}
	w := &walker{inv: inv, ctx: ctx}
	w.dir("", nil)
	sort.Slice(inv.Files, func(i, j int) bool { return inv.Files[i].Path < inv.Files[j].Path })
	return inv, ctx.Err()
}

type walker struct {
	inv *Inventory
	ctx context.Context
}

// ignored reports whether the stack of ignore files excludes rel. The last
// file with a matching rule decides, and a .ignore file, pushed after the
// .gitignore of the same directory, therefore wins.
func ignored(stack []*ignoreFile, rel string, isDir bool) bool {
	verdict := false
	for _, f := range stack {
		if ig, matched := f.match(rel, isDir); matched {
			verdict = ig
		}
	}
	return verdict
}

func (w *walker) dir(rel string, stack []*ignoreFile) {
	if w.ctx.Err() != nil {
		return
	}
	abs := filepath.Join(w.inv.Root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return
	}
	nested := false
	if rel != "" {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			nested = true
		}
	}
	if nested {
		// A nested repository has its own git scope: the parent's .gitignore
		// rules do not apply inside it. .ignore rules still do.
		kept := stack[:0:0]
		for _, f := range stack {
			if !f.git {
				kept = append(kept, f)
			}
		}
		stack = kept
	}
	next := stack
	if f := readIgnore(abs, rel, ".gitignore", true); f != nil {
		next = append(append([]*ignoreFile{}, next...), f)
	}
	if f := readIgnore(abs, rel, ".ignore", false); f != nil {
		next = append(append([]*ignoreFile{}, next...), f)
	}
	for _, e := range entries {
		if w.ctx.Err() != nil {
			return
		}
		name := e.Name()
		erel := path.Join(rel, name)
		mode := e.Type()
		switch {
		case mode&fs.ModeSymlink != 0:
			w.inv.Skipped[SkipSymlink]++
			continue
		case e.IsDir():
			if why, skip := skipDir(name); skip {
				w.inv.Skipped[why]++
				continue
			}
			if ignored(next, erel, true) {
				w.inv.Skipped[SkipIgnored]++
				continue
			}
			w.dir(erel, next)
		case mode.IsRegular():
			if why, skip := skipFile(name); skip {
				w.inv.Skipped[why]++
				continue
			}
			if ignored(next, erel, false) {
				w.inv.Skipped[SkipIgnored]++
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Size() > MaxFileBytes {
				w.inv.Skipped[SkipLarge]++
				continue
			}
			if len(w.inv.Files) >= MaxFiles {
				w.inv.Truncated = true
				w.inv.Skipped[SkipLimit]++
				return
			}
			w.inv.Files = append(w.inv.Files, File{Path: erel, Size: info.Size()})
		default:
			w.inv.Skipped[SkipSpecial]++
		}
	}
}
