package kanban

import (
	"path/filepath"
	"regexp"
	"strings"

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
	return Provenance{SessionID: sessionID, HostID: hostID, Project: name, ProjectKey: key, Remote: RemoteFor(abs), Dir: abs}
}

// RemoteFor is workdir's origin remote as owner/name on github.com and
// host/owner/name on any other forge (NormRemote), or "" when it is not a
// forge checkout. Every checkout of one repository, on any machine, has the
// same remote, where each has its own ProjectKey; the host keeps acme/app on
// github.com and on a Gitea apart.
func RemoteFor(workdir string) string {
	if rem, ok := forge.ParseRemote(gitinfo.OriginURL(workdir)); ok {
		if h := strings.ToLower(rem.Host); h != "" && h != "github.com" {
			return NormRemote(h + "/" + rem.Slug())
		}
		return NormRemote(rem.Slug())
	}
	return ""
}

// LegacyRemote is the owner/name an older Belai wrote for a remote off
// github.com, before remotes kept their host: rem without its host, or "" when
// rem names no host. A GitHub owner holds no dot or colon, so a first segment
// with one is a host.
func LegacyRemote(rem string) string {
	host, rest, ok := strings.Cut(rem, "/")
	if !ok || !strings.ContainsAny(host, ".:") || !strings.Contains(rest, "/") {
		return ""
	}
	return rest
}

// SameRemote reports whether two normalised remotes name one repository: the
// same, or one is the host-qualified form of the other's older owner/name.
func SameRemote(a, b string) bool {
	a, b = NormRemote(a), NormRemote(b)
	if a == "" || b == "" {
		return false
	}
	return a == b || LegacyRemote(a) == b || LegacyRemote(b) == a
}

// remoteRE is a normalised remote: owner/name, or a nested group path of a
// forge that has them, in lower case.
var remoteRE = regexp.MustCompile(`^[a-z0-9_.][a-z0-9_.-]{0,99}(/[a-z0-9_.][a-z0-9_.-]{0,99}){1,7}$`)

// NormRemote lower-cases owner/name and drops a ".git" suffix; anything else
// (one segment, a URL, a dot segment, a control character) is "".
func NormRemote(s string) string {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".git"))
	if !remoteRE.MatchString(s) {
		return ""
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "." || seg == ".." {
			return ""
		}
	}
	return s
}
