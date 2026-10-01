package knowledge

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Bounds on the reference documents placed in a working directory. They are a
// convenience copy of what the index holds, so past them the rest is simply not
// placed (it is still searchable).
const (
	MaxCopyFiles = 2000
	MaxCopyFile  = 4 << 20
	MaxCopyBytes = 32 << 20
)

// EnsureDirs creates rel (a slash-separated directory path) under root one
// component at a time, refusing a symlink or a file where a directory belongs.
func EnsureDirs(root, rel string) error {
	cur := root
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
			return fmt.Errorf("%s is not a plain directory", cur)
		}
	}
	return nil
}

// WriteFileAtomic writes data to p through a .part file and a rename, so a
// reader never sees half a file, refusing to replace anything but a regular
// file.
func WriteFileAtomic(p string, data []byte) error {
	if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	part := p + ".part"
	_ = os.Remove(part)
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(part)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return err
	}
	return os.Rename(part, p)
}

// CopyDocs places copies of reference documents under workdir at their Dest
// paths, best effort: one that cannot be copied is skipped, because the index
// still finds it. placed records the destinations the caller has placed, so a
// later call can refresh them; a file at a destination that is neither placed
// nor under KnowledgeCopyDir (Belai's own directory) is never replaced, so a
// copy cannot clobber a tracked file or one the worker made. skip, when set,
// lets the caller leave a destination alone. onlyOutside copies only documents
// that live outside the project, for a working directory that already is the
// project. It returns how many files it wrote.
func CopyDocs(workdir string, docs []ProfileFile, placed map[string]bool, skip func(dest string) bool, onlyOutside bool) int {
	n, files, total := 0, 0, int64(0)
	for _, f := range docs {
		if onlyOutside && f.Relative {
			continue
		}
		if hasDotGit(f.Dest) || (skip != nil && skip(f.Dest)) {
			continue
		}
		info, err := os.Lstat(f.Abs)
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxCopyFile {
			continue
		}
		if files++; files > MaxCopyFiles {
			return n
		}
		if total += info.Size(); total > MaxCopyBytes {
			return n
		}
		dst := filepath.Join(workdir, filepath.FromSlash(f.Dest))
		if cur, err := os.Lstat(dst); err == nil {
			owned := placed[f.Dest] || strings.HasPrefix(f.Dest, KnowledgeCopyDir+"/")
			if !owned || (cur.Size() == info.Size() && cur.ModTime().Equal(info.ModTime())) {
				continue
			}
		}
		data, err := os.ReadFile(f.Abs)
		if err != nil || bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		if EnsureDirs(workdir, path.Dir(f.Dest)) != nil || WriteFileAtomic(dst, data) != nil {
			continue
		}
		_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
		if placed != nil {
			placed[f.Dest] = true
		}
		n++
	}
	return n
}
