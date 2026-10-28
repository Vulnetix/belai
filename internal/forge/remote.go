package forge

import (
	"net/url"
	"strings"
)

// Provider kinds. Gitea and Forgejo share one provider (the Gitea API).
const (
	KindGitHub  = "github"
	KindGitLab  = "gitlab"
	KindGitea   = "gitea"
	KindForgejo = "forgejo"
	KindGeneric = "generic"
)

// Remote is a parsed origin URL.
type Remote struct {
	URL   string // masked: no userinfo, safe to display
	Host  string // lower-cased, no port
	Owner string // everything before the repo segment (GitLab subgroups included)
	Repo  string // final path segment without .git
	Kind  string // KindGitHub, KindGitLab, KindGitea, KindForgejo or KindGeneric
}

// Slug returns owner/repo, or "" when either part is unknown.
func (r Remote) Slug() string {
	if r.Owner == "" || r.Repo == "" {
		return ""
	}
	return r.Owner + "/" + r.Repo
}

// ParseRemote parses a git remote URL in URL form (https://, ssh://, git://)
// or scp form (git@host:owner/repo.git). Credentials never survive into the
// result. ok is false when no host can be found.
func ParseRemote(raw string) (Remote, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Remote{}, false
	}
	var host, path string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return Remote{}, false
		}
		host = u.Hostname()
		path = u.Path
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		masked := u.String()
		rem := build(host, path)
		rem.URL = masked
		return rem, true
	}
	// scp form: [user@]host:path. A local path has no colon before a slash.
	colon := strings.Index(raw, ":")
	if colon <= 0 || strings.Contains(raw[:colon], "/") {
		return Remote{}, false
	}
	host = raw[:colon]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	path = raw[colon+1:]
	if host == "" {
		return Remote{}, false
	}
	rem := build(host, path)
	rem.URL = host + ":" + strings.TrimPrefix(path, "/")
	return rem, true
}

func build(host, path string) Remote {
	host = strings.ToLower(host)
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	var owner, repo string
	if i := strings.LastIndex(path, "/"); i >= 0 {
		owner, repo = path[:i], path[i+1:]
	}
	return Remote{Host: host, Owner: owner, Repo: repo, Kind: kindFor(host)}
}

// kindFor maps a host to a provider kind: the forges setting first
// (hosts.go), then GitHub and GitLab by name. A GitHub Enterprise, Gitea or
// Forgejo host the setting does not name stays generic.
func kindFor(host string) string {
	if k := configuredKind(host); k != "" {
		return k
	}
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasPrefix(host, "github."):
		return KindGitHub
	case host == "gitlab.com" || strings.Contains(host, "gitlab"):
		return KindGitLab
	}
	return KindGeneric
}
