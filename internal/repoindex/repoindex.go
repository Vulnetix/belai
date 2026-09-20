// Package repoindex discovers git checkouts near the working directory and
// maps their remotes to local paths. It never fetches, never clones, and never
// writes: it only reads directory entries and the git remote that is already
// configured on disk.
package repoindex

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vulnetix/signet/internal/proc"

	"github.com/vulnetix/signet/internal/gitinfo"
)

const (
	scanTimeout  = 3 * time.Second
	maxDirs      = 500
	maxEntries   = 200
	probeTimeout = 500 * time.Millisecond
)

var skipDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
}

// Entry describes one locally available git checkout.
type Entry struct {
	Owner, Name string // parsed from the origin remote, e.g. "Vulnetix", "vdb-site"
	Host        string // "github.com", "gitlab.com", …
	Path        string // absolute path to the checkout
	Branch      string // current branch, via internal/gitinfo
}

// String formats an entry the way grounding evidence renders it.
func (e Entry) String() string {
	s := e.Path
	if e.Owner != "" && e.Name != "" {
		s = e.Host + "/" + e.Owner + "/" + e.Name + " " + e.Path
		if e.Branch != "" {
			s += " (branch " + e.Branch + ")"
		}
	}
	return s
}

// Index is a read-only mapping from remote refs to local paths.
type Index struct{ entries []Entry }

// Scan walks the sibling directories of workdir (depth two) looking for git
// checkouts. The scan is bounded: it examines at most maxDirs directories,
// indexes at most maxEntries checkouts, runs under a single timeout, and
// never follows symlinks.
func Scan(ctx context.Context, workdir string) Index {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	root := filepath.Dir(workdir)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Index{}
	}

	var entries []Entry
	dirsSeen := 0

	// readDir is a bounded, non-recursive listing helper.
	readDir := func(dir string) ([]os.DirEntry, bool) {
		if dirsSeen >= maxDirs {
			return nil, false
		}
		dirsSeen++
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, false
		}
		return entries, true
	}

	children, ok := readDir(absRoot)
	if !ok {
		return Index{}
	}

	for _, child := range children {
		if ctx.Err() != nil {
			break
		}
		if !child.IsDir() {
			continue
		}
		childName := child.Name()
		if strings.HasPrefix(childName, ".") || skipDirs[childName] {
			continue
		}
		childPath := filepath.Join(absRoot, childName)
		if e, ok := tryIndex(ctx, childPath); ok {
			entries = append(entries, e)
			if len(entries) >= maxEntries {
				break
			}
		}

		grandchildren, ok := readDir(childPath)
		if !ok {
			continue
		}
		for _, grandchild := range grandchildren {
			if ctx.Err() != nil {
				break
			}
			if !grandchild.IsDir() {
				continue
			}
			grandchildName := grandchild.Name()
			if strings.HasPrefix(grandchildName, ".") || skipDirs[grandchildName] {
				continue
			}
			grandchildPath := filepath.Join(childPath, grandchildName)
			if e, ok := tryIndex(ctx, grandchildPath); ok {
				entries = append(entries, e)
				if len(entries) >= maxEntries {
					break
				}
			}
		}
		if len(entries) >= maxEntries {
			break
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return Index{entries: entries}
}

// tryIndex indexes a single directory if it contains a git checkout.
func tryIndex(ctx context.Context, dir string) (Entry, bool) {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Lstat(gitPath)
	if err != nil {
		return Entry{}, false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return Entry{}, false
	}

	var e Entry
	e.Path = dir
	info, _ := gitinfo.Detect(dir)
	e.Branch = info.Branch

	if raw := RunProbe(ctx, dir, "git", "config", "--get", "remote.origin.url"); raw != "" {
		host, owner, name, ok := parseRemote(raw)
		if ok {
			e.Host = host
			e.Owner = owner
			e.Name = name
		}
	}
	return e, true
}

// Lookup finds an entry by "owner/repo", or a bare "repo" when the name is
// unique across the index. Matching is case-insensitive.
func (ix Index) Lookup(ref string) (Entry, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Entry{}, false
	}
	wantOwner, wantName, hasSlash := strings.Cut(ref, "/")
	if !hasSlash {
		// Bare repo name: succeed only when exactly one entry matches the
		// name (case-insensitive).
		var hit Entry
		found := 0
		for _, e := range ix.entries {
			if strings.EqualFold(e.Name, ref) {
				hit = e
				found++
			}
		}
		if found == 1 {
			return hit, true
		}
		return Entry{}, false
	}
	for _, e := range ix.entries {
		if strings.EqualFold(e.Owner, wantOwner) && strings.EqualFold(e.Name, wantName) {
			return e, true
		}
	}
	return Entry{}, false
}

// Owner returns every entry owned by the given remote owner (case-insensitive).
func (ix Index) Owner(owner string) []Entry {
	var out []Entry
	for _, e := range ix.entries {
		if strings.EqualFold(e.Owner, owner) {
			out = append(out, e)
		}
	}
	return out
}

// Entries returns the index in stable order.
func (ix Index) Entries() []Entry { return append([]Entry(nil), ix.entries...) }

// Empty reports whether the index contains no entries.
func (ix Index) Empty() bool { return len(ix.entries) == 0 }

// parseRemote extracts host, owner, and repository name from common git remote
// URL shapes. It returns ok=false when the URL does not match any recognised
// shape; the caller still records the checkout by path in that case.
func parseRemote(raw string) (host, owner, name string, ok bool) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")

	var hostPart, pathPart string

	switch {
	case strings.HasPrefix(raw, "ssh://") || strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		// net/url would need importing; splitting by hand keeps the package
		// stdlib-only. Find "//", then the host ends at the next "/".
		rest := raw[strings.Index(raw, "//")+2:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", "", false
		}
		hostPart = rest[:slash]
		if at := strings.Index(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		pathPart = strings.TrimPrefix(rest[slash:], "/")
	case strings.HasPrefix(raw, "git@"):
		// git@host:owner/repo
		rest := strings.TrimPrefix(raw, "git@")
		colon := strings.Index(rest, ":")
		if colon < 0 {
			return "", "", "", false
		}
		hostPart = rest[:colon]
		pathPart = rest[colon+1:]
	default:
		// host:owner/repo
		colon := strings.Index(raw, ":")
		slash := strings.Index(raw, "/")
		if colon > 0 && slash > colon && !strings.Contains(raw[:colon], "/") {
			hostPart = raw[:colon]
			pathPart = raw[colon+1:]
		} else if slash >= 0 {
			// No recognised host prefix; treat as a plain owner/repo path.
			pathPart = raw
		} else {
			return "", "", "", false
		}
	}

	parts := strings.Split(pathPart, "/")
	if len(parts) < 2 {
		return hostPart, "", "", false
	}
	return hostPart, parts[len(parts)-2], parts[len(parts)-1], true
}

// RunProbe runs one read-only command with a short timeout and output cap. An
// absent binary or non-zero exit yields "". It is exported so other packages
// can reuse the same scrubbed, bounded probe that the repo index uses.
func RunProbe(ctx context.Context, dir, name string, args ...string) string {
	if _, err := exec.LookPath(name); err != nil {
		return ""
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	ec := exec.CommandContext(pctx, name, args...)
	ec.Dir = dir
	ec.Env = proc.ScrubbedEnv()
	out, err := ec.Output()
	if err != nil {
		return ""
	}
	return capProbe(out)
}

const probeMaxBytes = 4 * 1024

func capProbe(out []byte) string {
	if len(out) == 0 {
		return ""
	}
	if len(out) > probeMaxBytes {
		out = out[:probeMaxBytes]
	}
	return strings.TrimRight(string(out), "\n")
}
