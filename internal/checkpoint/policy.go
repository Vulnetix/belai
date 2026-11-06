package checkpoint

import (
	"path/filepath"
	"strings"

	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/workdiff"
)

// PolicyFor builds the Policy of a session from facts it already holds: its
// working directory, a test for the confinement roots (tools.Cwd.Contains) and
// its permission rules. A Read deny rule keeps a file's bytes out of the store,
// and a Write or Edit deny rule forbids restoring the file.
func PolicyFor(workdir string, contains func(abs string) bool, perms permissions.Settings) Policy {
	rel := func(abs string) string {
		if workdir != "" {
			if r, err := filepath.Rel(workdir, abs); err == nil && !strings.HasPrefix(r, "..") {
				return filepath.ToSlash(r)
			}
		}
		return abs
	}
	return Policy{
		Within: contains,
		CopyDenied: func(abs string) string {
			if workdiff.ReadDenied(perms, workdir, rel(abs)) {
				return WhyDenied
			}
			return ""
		},
		RestoreDenied: func(abs string) bool {
			for _, tool := range []string{"Write", "Edit"} {
				for _, subj := range []string{rel(abs), abs} {
					if dec, rule := perms.Explain(tool, subj); rule != "" && dec == permissions.DecisionBlock {
						return true
					}
				}
			}
			return false
		},
	}
}
