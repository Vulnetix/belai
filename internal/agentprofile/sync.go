package agentprofile

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// Workspace sync limits.
const (
	// MaxSyncPaths is the most entries workspace.sync may hold.
	MaxSyncPaths   = 8
	maxSyncPathLen = 256
)

// Sync access values.
const (
	SyncRead  = "read"
	SyncWrite = "write"
)

// SyncSpec names one file or directory the harness copies from the
// repository into a worker's worktree before each turn, and, with write
// access, merges back into the repository after it (docs/fleet.md). It is how
// a crew shares a scratchpad between workers that each work in a worktree of
// their own: the worker uses the ordinary file tools on the relative path, and
// the harness does the copying.
//
// The profile defines what is synced and with what permission: a read entry is
// for reading only, and a write entry lifts the worker's deny on writing that
// path, and only that path. The harness keeps a fixed floor under every entry
// (see ProtectedRead and ProtectedWrite): Git's own files, Belai's state and
// the scanner evidence are never synced, whoever wrote the profile.
type SyncSpec struct {
	// Path is relative to the repository. A path ending in "/" is a directory.
	Path string `json:"path"`
	// Access is "read" (the default) or "write".
	Access string `json:"access,omitempty"`
}

// Writes reports whether the entry has write access.
func (s SyncSpec) Writes() bool { return s.Access == SyncWrite }

// IsDir reports whether the entry names a directory by its trailing slash.
func (s SyncSpec) IsDir() bool { return strings.HasSuffix(s.Path, "/") }

// Clean returns the path without its trailing slash.
func (s SyncSpec) Clean() string { return path.Clean(s.Path) }

// SyncPaths returns the profile's workspace.sync entries, or nil.
func (p AgentProfile) SyncPaths() []SyncSpec {
	if p.Workspace == nil || len(p.Workspace.Sync) == 0 {
		return nil
	}
	return append([]SyncSpec(nil), p.Workspace.Sync...)
}

// vulnetixWriteProtected are the entries of .vulnetix a worker never writes
// back: Belai's own state, the files a scan or a verdict leaves as evidence,
// and the settings. Forging one could fake a review.
var vulnetixWriteProtected = map[string]bool{
	"belai": true, "vex": true, "quality": true, "plans": true, "goals": true,
	"prompts": true, "processes": true, "knowledge": true,
	"settings.json": true, "credentials.json": true, "prompts.json": true,
	"memory.yaml": true, "memory.yml": true,
	"code-review-summary.md": true, "code-review-manifest.json": true,
}

// ProtectedRead reports whether rel (a clean repository-relative path) is never
// copied into a worktree: Git's own files and the Belai state and credentials
// in .vulnetix.
func ProtectedRead(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, p := range parts {
		if p == ".git" {
			return true
		}
	}
	if len(parts) >= 2 && parts[0] == ".vulnetix" {
		switch parts[1] {
		case "belai", "settings.json", "credentials.json":
			return true
		}
	}
	return false
}

// ProtectedWrite reports whether rel is never merged back into the
// repository: everything ProtectedRead names, plus the scanner evidence and
// Belai's other files in .vulnetix, and any scan artefact placed there.
func ProtectedWrite(rel string) bool {
	if ProtectedRead(rel) {
		return true
	}
	parts := strings.Split(rel, "/")
	if len(parts) >= 2 && parts[0] == ".vulnetix" {
		if vulnetixWriteProtected[parts[1]] {
			return true
		}
		name := strings.ToLower(parts[len(parts)-1])
		if len(parts) == 2 && (strings.HasSuffix(name, ".sarif") || strings.HasSuffix(name, ".cdx.json") ||
			strings.HasSuffix(name, ".openvex.json") || strings.HasPrefix(name, "vex")) {
			return true
		}
	}
	return false
}

// validateSync checks workspace.sync.
func validateSync(w *WorkspaceSpec) error {
	if len(w.Sync) == 0 {
		return nil
	}
	if w.Isolation != IsolationWorktree {
		return errors.New("workspace.sync needs isolation: worktree (a worktree to copy into)")
	}
	if len(w.Sync) > MaxSyncPaths {
		return fmt.Errorf("workspace.sync lists %d paths; the most is %d", len(w.Sync), MaxSyncPaths)
	}
	var cleaned []string
	for _, e := range w.Sync {
		if err := validSyncPath(e); err != nil {
			return fmt.Errorf("workspace.sync: %w", err)
		}
		switch e.Access {
		case "", SyncRead, SyncWrite:
		default:
			return fmt.Errorf("workspace.sync: access must be read or write, not %q", e.Access)
		}
		c := e.Clean()
		for _, prev := range cleaned {
			if c == prev || strings.HasPrefix(c, prev+"/") || strings.HasPrefix(prev, c+"/") {
				return fmt.Errorf("workspace.sync lists %q and %q, which overlap", prev, c)
			}
		}
		cleaned = append(cleaned, c)
	}
	return nil
}

func validSyncPath(e SyncSpec) error {
	s := e.Path
	switch {
	case strings.TrimSpace(s) == "":
		return errors.New("a path is empty")
	case len(s) > maxSyncPathLen:
		return fmt.Errorf("a path is longer than %d bytes", maxSyncPathLen)
	case strings.Contains(s, `\`):
		return fmt.Errorf("%q must use forward slashes", s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '/':
		default:
			// Plain characters only, so a permission rule built from the
			// path is the path and nothing wider.
			return fmt.Errorf("%q may hold only letters, digits and . _ - /", s)
		}
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("%q must be relative to the repository", s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return fmt.Errorf("%q must not contain ..", s)
		}
	}
	c := path.Clean(s)
	if c == "." {
		return fmt.Errorf("%q names the repository itself; list a file or a directory in it", s)
	}
	if ProtectedRead(c) {
		return fmt.Errorf("%q is never synced (Git's files and Belai's state and credentials)", s)
	}
	if e.Writes() && ProtectedWrite(c) {
		return fmt.Errorf("%q is never written back (Belai's state and the scanner evidence); give it read access", s)
	}
	return nil
}
