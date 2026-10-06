package rc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/belai/internal/gitinfo"
	"github.com/vulnetix/belai/internal/projectregistry"
	"github.com/vulnetix/belai/internal/trustgate"
)

// Dir is one directory the daemon will start sessions in.
type Dir struct {
	Path string
	Name string
	// Source is "trusted" (a project already trusted on this host) or "arg"
	// (passed as --dir).
	Source string
}

// Source values.
const (
	SourceTrusted = "trusted"
	SourceArg     = "arg"
)

// Normalize makes a directory comparable: absolute, symlinks resolved, and
// required to exist as a directory.
func Normalize(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	return real, nil
}

// TrustArg trusts a --dir the host user passed, exactly as `belai -trust-dir`
// would: the directory only, never the workspace directories its settings
// propose. Passing it on the command line is the host user's consent. It
// reports whether it was newly trusted.
func TrustArg(dir string) (path string, granted bool, err error) {
	path, err = Normalize(dir)
	if err != nil {
		return "", false, err
	}
	st, err := trustgate.Check(path)
	if err == nil && st.Trusted {
		return path, false, nil
	}
	if err := trustgate.Grant(path, nil); err != nil {
		return "", false, err
	}
	return path, true, nil
}

// Skipped is a directory the offer leaves out because a worker could not
// start there, with the repository root whose trust is missing.
type Skipped struct {
	Dir  Dir
	Root string
}

// Collect gathers the directories to offer: every trusted project on this
// host that still exists, plus args (already trusted by TrustArg). Args come
// first, then wd (the directory rc was started in) when it is offered, so a
// start the website does not place lands where the host user ran rc; the
// rest follow by path. A directory listed both ways is an arg.
//
// A directory is offered only when a worker would start there: `belai agent
// start` takes its trust from the repository root containing the directory,
// so a directory inside an untrusted repository is skipped here instead of
// failing after the website picks it.
func Collect(args []string, wd string) (offered []Dir, skipped []Skipped) {
	seen := map[string]bool{}
	add := func(d Dir) {
		if root, ok := Startable(d.Path); ok {
			offered = append(offered, d)
		} else {
			skipped = append(skipped, Skipped{Dir: d, Root: root})
		}
	}
	for _, a := range args {
		p, err := Normalize(a)
		if err != nil || seen[p] {
			continue
		}
		seen[p] = true
		add(Dir{Path: p, Name: filepath.Base(p), Source: SourceArg})
	}
	reg, err := projectregistry.Load()
	if err != nil {
		return offered, skipped
	}
	var trusted []Dir
	for _, e := range reg.All() {
		if !e.Trusted || e.Missing {
			continue
		}
		p, err := Normalize(e.Path)
		if err != nil || seen[p] || IsTempDir(p) {
			continue
		}
		seen[p] = true
		name := e.Name
		if name == "" {
			name = filepath.Base(p)
		}
		trusted = append(trusted, Dir{Path: p, Name: name, Source: SourceTrusted})
	}
	here, _ := Normalize(wd)
	for _, d := range hereFirst(trusted, here) {
		add(d)
	}
	return offered, skipped
}

// hereFirst sorts dirs by path with here, when present, at the front.
func hereFirst(dirs []Dir, here string) []Dir {
	sort.Slice(dirs, func(i, j int) bool {
		if (dirs[i].Path == here) != (dirs[j].Path == here) {
			return dirs[i].Path == here
		}
		return dirs[i].Path < dirs[j].Path
	})
	return dirs
}

// Startable reports whether `belai agent start` would accept dir: dir is not
// inside a git repository, is the repository root itself, or sits in a
// repository whose root is trusted. It returns the root it checked.
func Startable(dir string) (root string, ok bool) {
	info, found := gitinfo.Detect(dir)
	if !found || info.Root == "" {
		return dir, true
	}
	if r, err := Normalize(info.Root); err == nil && r == dir {
		return dir, true
	}
	st, err := trustgate.Check(info.Root)
	return info.Root, err == nil && st.Trusted
}

// Allowed reports whether cwd is exactly one of dirs once normalised. It is
// never a prefix match: a subdirectory of an offered project is not offered.
func Allowed(dirs []Dir, cwd string) (string, bool) {
	p, err := Normalize(cwd)
	if err != nil {
		return "", false
	}
	for _, d := range dirs {
		if d.Path == p {
			return p, true
		}
	}
	return "", false
}

// MaxExtraDirs bounds the extra workspace directories one session may add.
const MaxExtraDirs = 16

// AllowedExtra normalises the extra workspace directories a start request
// names and checks each is one the host offers. The primary directory and
// repeats are dropped. ok is false when any directory is not offered or there
// are more than MaxExtraDirs.
func AllowedExtra(dirs []Dir, primary string, extra []string) (out []string, ok bool) {
	if len(extra) > MaxExtraDirs {
		return nil, false
	}
	seen := map[string]bool{primary: true}
	for _, e := range extra {
		p, allowed := Allowed(dirs, e)
		if !allowed {
			return nil, false
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, true
}

// IsTempDir reports a scratch directory (tests, throwaway checkouts), which
// the offer hides even when trusted; pass one with --dir to offer it anyway.
func IsTempDir(p string) bool {
	for _, root := range []string{os.TempDir(), "/tmp", "/private/tmp", "/var/tmp"} {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		if p == root {
			return true
		}
		if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !filepath.IsAbs(rel) && len(rel) > 0 && rel[0] != '.' {
			return true
		}
	}
	return false
}
