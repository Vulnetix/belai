package knowledge

import (
	"path"
	"path/filepath"
	"strings"
)

// A profile's own files. A library backup carries the files a profile names, and
// an install on another host writes them into a directory the daemon owns,
// config.ProfileFilesDir(id), never into a repository: a remote request must not
// choose which files land in a project, or which ones get indexed. The profile
// still names them the way it always did (a path relative to the project, under
// ~/, or absolute), and that path keeps its meaning: the owned copy is the
// fallback for a path that does not exist on this host, so a project's own file
// of the same name always wins.
//
// Inside the owned directory the three forms of path live apart, so a relative
// path can never be mistaken for a home or an absolute one:
//
//	rel/<path>    a path relative to the project
//	home/<path>   a path under ~/
//	abs/<path>    an absolute path, without its leading slash

// Owned path kinds.
const (
	OwnedRel  = "rel"
	OwnedHome = "home"
	OwnedAbs  = "abs"
)

// OwnedRelPath is where a profile's listed path lives inside its owned
// directory, slash separated, or false when the path cannot be kept (it climbs
// out with .., is empty or names nothing).
func OwnedRelPath(raw string) (string, bool) {
	slash := filepath.ToSlash(raw)
	var kind, rest string
	switch {
	case strings.HasPrefix(slash, "~/"):
		kind, rest = OwnedHome, slash[2:]
	case strings.HasPrefix(slash, "/"):
		kind, rest = OwnedAbs, strings.TrimLeft(slash, "/")
	case filepath.IsAbs(raw):
		// A drive path: not representable in a portable bundle.
		return "", false
	default:
		kind, rest = OwnedRel, slash
	}
	for _, seg := range strings.Split(rest, "/") {
		if seg == ".." {
			return "", false
		}
	}
	rest = path.Clean(rest)
	if rest == "." || rest == "" || strings.HasPrefix(rest, "../") || rest == ".." {
		return "", false
	}
	return kind + "/" + rest, true
}

// OwnedPath is the file on this host that holds a listed path inside owned, the
// profile's owned directory, or false when the path cannot be kept.
func OwnedPath(owned, raw string) (string, bool) {
	rel, ok := OwnedRelPath(raw)
	if !ok || owned == "" {
		return "", false
	}
	return filepath.Join(owned, filepath.FromSlash(rel)), true
}
