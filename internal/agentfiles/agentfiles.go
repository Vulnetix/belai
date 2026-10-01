// Package agentfiles carries the files an agent profile uses with its library
// backup (docs/knowledge.md, "A profile's own files").
//
// A profile names files in two places: knowledge.paths (documents the agent may
// search) and workspace.sync (files a worker's crew shares). Until now only the
// profile travelled, so an agent restored on another host came back without the
// files it was written around. Capture reads those files on the host that has
// them, for a profile_backup request; Install writes a library version's files
// on the host that receives it, into a directory the daemon owns, from where the
// profile's own paths resolve as a fallback (knowledge.EnumerateProfileOwned).
//
// What the package guarantees, whoever wrote the profile or made the request:
//
//   - Capture reads only what the knowledge index itself would: no symlinks, no
//     hidden-credential names or key stores (locate.EligibleFile), nothing under
//     .git, no path knowledge.BlockedAbsolute refuses, and a relative path never
//     leaves the directory it is resolved under. A file that holds a private key
//     or a known token is left out and reported, never uploaded.
//   - Files are text: UTF-8, no NUL byte, at most MaxFileBytes each, MaxFiles and
//     MaxTotalBytes in all, the caps the server enforces too.
//   - Install writes only inside config.ProfileFilesDir(id). A path that climbs
//     out, names nothing or is not UTF-8 text is refused before anything is
//     written, a file's bytes must hash to the hash the library listed, and the
//     directory is replaced as a whole, so a failed install leaves the old files.
package agentfiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/knowledge"
	"github.com/vulnetix/belai/internal/locate"
)

// Bounds, equal to the library's (vdb-site belai_library_files.go).
const (
	MaxFileBytes  = 256 << 10
	MaxFiles      = 32
	MaxTotalBytes = 2 << 20
)

// File is one file of a profile: the path the profile lists (or a path under a
// listed directory) and its bytes.
type File struct {
	Path    string
	Content []byte
	SHA256  string
}

// Report says what Capture left out, for the acknowledgement. Reasons are
// harness words and counts, never file names or text from the files.
type Report struct {
	Missing   int // a listed path that exists nowhere this host can read
	Refused   int // a path the rules never read (symlink, blocked, credentials, binary, too large)
	Secrets   int // a file left out because it holds a key or token
	OverLimit int // files beyond the count or size caps
}

// Total is how many paths and files were left out.
func (r Report) Total() int { return r.Missing + r.Refused + r.Secrets + r.OverLimit }

// ListedPaths are the paths a profile names, knowledge.paths first and then
// workspace.sync, each once.
func ListedPaths(p agentprofile.AgentProfile) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range p.KnowledgePaths() {
		add(s)
	}
	for _, sp := range p.SyncPaths() {
		add(sp.Path)
	}
	return out
}

// Sum is the hex SHA-256 of b.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Capture reads the files the profile's listed paths name on this host. A
// relative path is tried under each of roots, in order, then in the profile's own
// owned directory (a host that installed the files from the library can back them
// up again); the first place that holds it wins. The result is sorted by path
// and within the bounds.
func Capture(ctx context.Context, p agentprofile.AgentProfile, roots []string) ([]File, Report, error) {
	var rep Report
	var out []File
	owned := ""
	if p.ID != "" {
		owned, _ = config.ProfileFilesDir(p.ID)
	}
	seen := map[string]bool{}
	total := 0
	home, _ := os.UserHomeDir()

	take := func(listed string, f File) {
		if seen[f.Path] {
			return
		}
		if len(out) >= MaxFiles || total+len(f.Content) > MaxTotalBytes {
			rep.OverLimit++
			return
		}
		seen[f.Path] = true
		total += len(f.Content)
		out = append(out, f)
	}

	for _, listed := range ListedPaths(p) {
		if err := ctx.Err(); err != nil {
			return nil, rep, err
		}
		raw := strings.TrimRight(listed, "/")
		if _, ok := knowledge.OwnedRelPath(raw); !ok {
			rep.Refused++
			continue
		}
		abs, relativeRoot, fromOwned, found := resolve(raw, roots, owned, home)
		if !found {
			rep.Missing++
			continue
		}
		relative := !strings.HasPrefix(raw, "~/") && !filepath.IsAbs(raw)
		if (relative && hasDotGit(raw)) || (!relative && !fromOwned && knowledge.BlockedAbsolute(abs)) {
			rep.Refused++
			continue
		}
		base := relativeRoot
		if fromOwned {
			base = owned
		}
		if (relative || fromOwned) && !confined(base, abs) {
			rep.Refused++
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			rep.Refused++
			continue
		}
		if info.IsDir() {
			inv, berr := locate.Build(ctx, abs)
			if berr != nil {
				rep.Refused++
				continue
			}
			for _, lf := range inv.Files {
				child := filepath.Join(inv.Root, filepath.FromSlash(lf.Path))
				if !confined(abs, child) {
					rep.Refused++
					continue
				}
				if f, why := readFile(path.Join(raw, lf.Path), child); why == "" {
					take(listed, f)
				} else {
					rep.count(why)
				}
			}
			continue
		}
		if _, ok := locate.EligibleFile(abs, info.Size()); !ok {
			rep.Refused++
			continue
		}
		if f, why := readFile(raw, abs); why == "" {
			take(listed, f)
		} else {
			rep.count(why)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, rep, nil
}

func (r *Report) count(why string) {
	if why == "secret" {
		r.Secrets++
		return
	}
	r.Refused++
}

// resolve finds the file a listed path names: under a root (relative), at the
// home or absolute path, then in the owned directory. It reports the root it
// used for a relative path so the caller can confine the result to it.
func resolve(raw string, roots []string, owned, home string) (abs, root string, fromOwned, found bool) {
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	switch {
	case strings.HasPrefix(raw, "~/"):
		if home != "" {
			if p := filepath.Join(home, filepath.FromSlash(raw[2:])); exists(p) {
				return p, "", false, true
			}
		}
	case filepath.IsAbs(raw):
		if exists(raw) {
			return filepath.Clean(raw), "", false, true
		}
	default:
		for _, r := range roots {
			if r == "" {
				continue
			}
			if p := filepath.Join(r, filepath.FromSlash(raw)); exists(p) {
				return p, r, false, true
			}
		}
	}
	if op, ok := knowledge.OwnedPath(owned, raw); ok && exists(op) {
		return op, "", true, true
	}
	return "", "", false, false
}

// confined reports whether p lies inside root with no symlink on the way: its
// fully resolved form is exactly root's resolved form joined with the rest.
func confined(root, p string) bool {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rr, rp)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	// A symlink between root and p would make the resolved path differ from
	// the lexical one.
	lex, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return filepath.Clean(rel) == filepath.Clean(lex)
}

func hasDotGit(raw string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(raw), "/") {
		if seg == ".git" {
			return true
		}
	}
	return false
}

// readFile reads one regular text file within the bounds, or says why not. The
// why is "secret" for a file that holds a key or token, anything else for the
// rest.
func readFile(listed, abs string) (File, string) {
	info, err := os.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > MaxFileBytes {
		return File{}, "refused"
	}
	if _, ok := locate.EligibleFile(abs, info.Size()); !ok {
		return File{}, "refused"
	}
	fh, err := os.Open(abs)
	if err != nil {
		return File{}, "refused"
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, MaxFileBytes+1))
	if err != nil || len(b) == 0 || len(b) > MaxFileBytes || !utf8.Valid(b) || containsNUL(b) {
		return File{}, "refused"
	}
	if HoldsSecret(b) {
		return File{}, "secret"
	}
	return File{Path: listed, Content: b, SHA256: Sum(b)}, ""
}

func containsNUL(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// ErrBadFile is a file the library listed that Install will not write.
var ErrBadFile = errors.New("agentfiles: a file is not one this host will write")

// Install replaces the profile's owned directory with files. Nothing is written
// until every file is checked: the path keeps inside the directory
// (knowledge.OwnedRelPath), the bytes are text within the bounds, and they hash
// to the hash the library listed (sum, when set). The directory is built beside
// the old one and swapped in, so a failure leaves the old files as they were. An
// empty files removes the directory.
func Install(profileID string, files []File) error {
	dir, err := config.ProfileFilesDir(profileID)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return os.RemoveAll(dir)
	}
	if len(files) > MaxFiles {
		return fmt.Errorf("%w: more than %d files", ErrBadFile, MaxFiles)
	}
	total := 0
	type placed struct {
		rel     string
		content []byte
	}
	var plan []placed
	seen := map[string]bool{}
	for _, f := range files {
		rel, ok := knowledge.OwnedRelPath(f.Path)
		if !ok || seen[rel] {
			return fmt.Errorf("%w: a path is not one it can keep", ErrBadFile)
		}
		seen[rel] = true
		if len(f.Content) == 0 || len(f.Content) > MaxFileBytes || !utf8.Valid(f.Content) || containsNUL(f.Content) {
			return fmt.Errorf("%w: a file is not bounded text", ErrBadFile)
		}
		if f.SHA256 != "" && Sum(f.Content) != f.SHA256 {
			return fmt.Errorf("%w: a file does not match its hash", ErrBadFile)
		}
		if total += len(f.Content); total > MaxTotalBytes {
			return fmt.Errorf("%w: more than %d bytes", ErrBadFile, MaxTotalBytes)
		}
		plan = append(plan, placed{rel: rel, content: f.Content})
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, "."+filepath.Base(dir)+".new-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, pf := range plan {
		dst := filepath.Join(tmp, filepath.FromSlash(pf.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, pf.content, 0o600); err != nil {
			return err
		}
	}
	old := dir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Lstat(dir); err == nil {
		if err := os.Rename(dir, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		_ = os.Rename(old, dir)
		return err
	}
	_ = os.RemoveAll(old)
	return nil
}

// Remove deletes the profile's owned directory.
func Remove(profileID string) error {
	dir, err := config.ProfileFilesDir(profileID)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Owned lists the files in the profile's owned directory, each with the path a
// profile lists for it (the inverse of knowledge.OwnedRelPath): relative paths
// as they are, "~/" for the home form and a leading slash for the absolute one.
// A profile with no directory has none.
func Owned(profileID string) ([]File, error) {
	dir, err := config.ProfileFilesDir(profileID)
	if err != nil {
		return nil, err
	}
	var out []File
	for _, kind := range []string{knowledge.OwnedRel, knowledge.OwnedHome, knowledge.OwnedAbs} {
		base := filepath.Join(dir, kind)
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, werr error) error {
			if werr != nil {
				if errors.Is(werr, os.ErrNotExist) {
					return nil
				}
				return werr
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			listed := filepath.ToSlash(rel)
			switch kind {
			case knowledge.OwnedHome:
				listed = "~/" + listed
			case knowledge.OwnedAbs:
				listed = "/" + listed
			}
			out = append(out, File{Path: listed, Content: content, SHA256: Sum(content)})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Add stores one file in the profile's owned directory under the path a profile
// lists for it, replacing a file already there, within the same bounds as
// Install. It is how a person attaches a file to an agent from this host.
func Add(profileID, listed string, content []byte) error {
	files, err := Owned(profileID)
	if err != nil {
		return err
	}
	var next []File
	for _, f := range files {
		if f.Path != listed {
			next = append(next, f)
		}
	}
	return Install(profileID, append(next, File{Path: listed, Content: content, SHA256: Sum(content)}))
}

// RemoveFile deletes one file from the profile's owned directory and reports
// whether it was there.
func RemoveFile(profileID, listed string) (bool, error) {
	files, err := Owned(profileID)
	if err != nil {
		return false, err
	}
	var next []File
	found := false
	for _, f := range files {
		if f.Path == listed {
			found = true
			continue
		}
		next = append(next, f)
	}
	if !found {
		return false, nil
	}
	return true, Install(profileID, next)
}
