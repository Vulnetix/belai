package finishreview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/belai/internal/workdiff"
)

// baselineVersion is the version of the saved baseline.
const baselineVersion = 1

// maxBaselineBytes bounds a saved baseline read back: the file is not trusted.
const maxBaselineBytes = 1 << 20

// Baseline is the set of files that were already changed when a goal started,
// so a file the user left in the tree is not mistaken for the goal's scratch.
// The zero value knows nothing, which is the stricter reading: every untracked
// file is new.
type Baseline struct {
	// Complete is true when the snapshot saw the whole tree. An incomplete
	// baseline is treated as empty.
	Complete bool
	// Paths are the changed files, sorted.
	Paths []string
}

// Has reports whether p was already changed when the goal started.
func (b Baseline) Has(p string) bool {
	if !b.Complete {
		return false
	}
	i := sort.SearchStrings(b.Paths, p)
	return i < len(b.Paths) && b.Paths[i] == p
}

// Capture records the working tree's changed files. A tree that cannot be read
// whole yields the zero baseline.
func Capture(ctx context.Context, workdir string, hide func(string) bool) Baseline {
	ctx, cancel := context.WithTimeout(ctx, collectBudget)
	defer cancel()
	return baselineOf(workdiff.Collect(ctx, workdir, hide))
}

func baselineOf(snap workdiff.Snapshot) Baseline {
	if snap.Unavailable != "" || snap.More > 0 {
		return Baseline{}
	}
	b := Baseline{Complete: true}
	for _, f := range snap.Files {
		if !harnessPath(f.Path) {
			b.Paths = append(b.Paths, f.Path)
		}
	}
	sort.Strings(b.Paths)
	return b
}

type baselineFile struct {
	Version  int      `json:"version"`
	Complete bool     `json:"complete"`
	Paths    []string `json:"paths"`
}

// Save writes the baseline to file, private to the user (0600 in a 0700
// directory), through a temporary name and a rename. A symbolic link at the
// destination or as its directory is refused.
func (b Baseline) Save(file string) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(dir); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("finish review baseline directory is a symbolic link")
	}
	if st, err := os.Lstat(file); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return errors.New("finish review baseline is a symbolic link")
	}
	data, err := json.Marshal(baselineFile{Version: baselineVersion, Complete: b.Complete, Paths: b.Paths})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".baseline-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(name, 0o600)); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, file); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// LoadBaseline reads a saved baseline. A missing, oversized, unparsable or
// out-of-shape file yields the zero baseline, the stricter reading, never an
// error that would skip the review.
func LoadBaseline(file string) Baseline {
	st, err := os.Lstat(file)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxBaselineBytes {
		return Baseline{}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Baseline{}
	}
	var f baselineFile
	if json.Unmarshal(data, &f) != nil || f.Version != baselineVersion || !f.Complete || len(f.Paths) > workdiff.MaxFiles {
		return Baseline{}
	}
	for _, p := range f.Paths {
		if p == "" || len(p) > 400 {
			return Baseline{}
		}
	}
	paths := append([]string(nil), f.Paths...)
	sort.Strings(paths)
	return Baseline{Complete: true, Paths: paths}
}
