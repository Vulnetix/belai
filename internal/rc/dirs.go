package rc

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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

// Collect gathers the directories to offer: every trusted project on this
// host that still exists, plus args (already trusted by TrustArg). Args come
// first; a directory listed both ways is an arg.
func Collect(args []string) []Dir {
	var out []Dir
	seen := map[string]bool{}
	for _, a := range args {
		p, err := Normalize(a)
		if err != nil || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, Dir{Path: p, Name: filepath.Base(p), Source: SourceArg})
	}
	reg, err := projectregistry.Load()
	if err != nil {
		return out
	}
	var trusted []Dir
	for _, e := range reg.All() {
		if !e.Trusted || e.Missing {
			continue
		}
		p, err := Normalize(e.Path)
		if err != nil || seen[p] || isTempDir(p) {
			continue
		}
		seen[p] = true
		name := e.Name
		if name == "" {
			name = filepath.Base(p)
		}
		trusted = append(trusted, Dir{Path: p, Name: name, Source: SourceTrusted})
	}
	sort.Slice(trusted, func(i, j int) bool { return trusted[i].Path < trusted[j].Path })
	return append(out, trusted...)
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

// isTempDir hides trusted scratch directories (tests, throwaway checkouts)
// from the offer; pass one with --dir to offer it anyway.
func isTempDir(p string) bool {
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
