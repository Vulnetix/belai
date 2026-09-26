// Package trustgate implements the pure logic of the first-run workspace
// trust confirmation: decide whether a directory needs a prompt and record
// the user's ruling. It is deliberately free of any TUI so the decision rule
// and the storage wiring can be unit-tested without a terminal.
package trustgate

import (
	"path/filepath"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/projectregistry"
)

// Status describes the trust state of one working directory.
type Status struct {
	// Workdir is the absolute working directory.
	Workdir string
	// Trusted reports whether the user has affirmed this directory before.
	Trusted bool
	// NewDirs are project-proposed workspace_dirs not yet accepted or
	// declined, as absolute paths.
	NewDirs []string
	// ProposalPath is where the proposals came from, for display.
	ProposalPath string
}

// NeedsPrompt reports whether this status requires a confirmation: an
// untrusted directory, or a trusted one whose settings grew a new proposed
// directory.
func (s Status) NeedsPrompt() bool {
	return !s.Trusted || len(s.NewDirs) > 0
}

// Check computes the trust status for workdir. Any error is returned to the
// caller, which must treat it as "needs prompt" (fail closed).
func Check(workdir string) (Status, error) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		return Status{}, err
	}
	st := Status{Workdir: abs, ProposalPath: filepath.Join(abs, ".vulnetix", "settings.json")}

	trusted, accepted, declined, err := projectregistry.TrustOf(abs)
	if err != nil {
		return st, err
	}
	st.Trusted = trusted

	already := map[string]bool{}
	for _, d := range accepted {
		already[normalize(d)] = true
	}
	for _, d := range declined {
		already[normalize(d)] = true
	}
	for _, d := range projectregistry.WorkspaceDirs(abs) {
		already[normalize(d)] = true
	}

	proj, err := config.LoadProject(abs)
	if err != nil {
		return st, err
	}
	for _, d := range proj.WorkspaceDirs {
		p := resolveProposed(abs, d)
		if !already[p] {
			st.NewDirs = append(st.NewDirs, p)
		}
	}
	return st, nil
}

// Grant records trust and accepts the given proposed directories.
func Grant(workdir string, accept []string) error {
	return projectregistry.Trust(workdir, accept)
}

// Refuse records that the given proposed directories were declined.
func Refuse(workdir string, dirs []string) error {
	return projectregistry.Decline(workdir, dirs)
}

// resolveProposed resolves one project-proposed directory against workdir,
// absolutising it and resolving symlinks when the directory exists.
func resolveProposed(workdir, dir string) string {
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(workdir, dir)
	}
	return normalize(dir)
}

// normalize absolutises dir and resolves symlinks best-effort.
func normalize(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		return eval
	}
	return abs
}
