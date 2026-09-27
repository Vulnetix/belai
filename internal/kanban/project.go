package kanban

import (
	"path/filepath"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/forge"
	"github.com/vulnetix/belai/internal/gitinfo"
)

// ProjectFor derives the project fields of a Provenance for workdir: the name
// is the origin remote's repository name, else the repository root's
// basename, else the workdir's; the key is the same workdir key session sync
// uses, taken at the repository root so every subdirectory of a checkout
// files into one project.
func ProjectFor(workdir string) (name, key string) {
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	root := abs
	if info, ok := gitinfo.Detect(abs); ok && info.Root != "" {
		root = info.Root
	}
	key = config.WorkdirKey(root)
	if rem, ok := forge.ParseRemote(gitinfo.OriginURL(abs)); ok && rem.Repo != "" {
		return CleanTitle(rem.Repo), key
	}
	return CleanTitle(filepath.Base(root)), key
}

// ProvenanceFor builds the provenance a session in workdir stamps on its
// writes.
func ProvenanceFor(workdir, sessionID, hostID string) Provenance {
	name, key := ProjectFor(workdir)
	abs, err := filepath.Abs(workdir)
	if err != nil {
		abs = workdir
	}
	return Provenance{SessionID: sessionID, HostID: hostID, Project: name, ProjectKey: key, Dir: abs}
}
