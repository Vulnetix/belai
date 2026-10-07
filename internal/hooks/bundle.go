package hooks

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/belai/internal/posture"
)

// Warn reports a bundle that did not load. The default writes to stderr; the
// TUI and tests replace it.
var Warn = func(msg string) { fmt.Fprintln(os.Stderr, "belai: warning: "+msg) }

// LoadBundles reads every bundle directory under root (the global hooks
// directory): <root>/<name>/hooks.json plus the script files beside it. A bundle
// that is a symlink, is named oddly, has no regular hooks.json, or holds one
// command that fails validation is skipped whole, with a warning that names the
// reason (for example a program missing from hooks.allowed_programs). Hooks come
// back sorted by name, and each keeps its bundle directory as Dir.
func LoadBundles(root string, pol posture.Policy, allowed []string) ([]*Hook, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Hook
	for _, e := range entries {
		name := e.Name()
		// A dot-prefixed directory is an install in progress.
		if !e.IsDir() || !ValidBundleName(name) {
			continue
		}
		dir := filepath.Join(root, name)
		if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			continue
		}
		hs, err := loadBundle(name, dir, allowed)
		if err != nil {
			if pol.Level(posture.HookInvalid) != posture.Ignore {
				Warn(fmt.Sprintf("hook bundle %s: %v", name, err))
			}
			continue
		}
		out = append(out, hs...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// loadBundle reads one bundle's definition and validates it against dir.
func loadBundle(name, dir string, allowed []string) ([]*Hook, error) {
	path := filepath.Join(dir, BundleDefinitionFile)
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("no %s", BundleDefinitionFile)
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", BundleDefinitionFile)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBundleDefinitionBytes+1))
	if err != nil {
		return nil, err
	}
	p, err := ParseDefinition(name, dir, data, allowed)
	if err != nil {
		return nil, err
	}
	return p.Hooks, nil
}
