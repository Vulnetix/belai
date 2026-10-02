package libitem

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/vulnetix/belai/internal/config"
)

// Repo limits, shared with the website and the server.
const (
	MaxRepoURL   = 1024
	MaxRepoRefs  = 16
	MaxRepoRef   = 255
	MaxRepoDir   = 256
	MaxRepoDepth = 1000
	// MaxRepoInstallation is the largest integer a JSON number carries without
	// loss in a browser (2^53 - 1).
	MaxRepoInstallation = 1<<53 - 1
)

var (
	repoFields = []string{"name", "url", "visibility", "auth", "installation_id", "refs", "dir", "depth", "submodules", "enabled"}

	// A host name: labels of letters, digits and hyphens, starting with a letter
	// or digit (so it can never be read as an option), with an optional port.
	repoHostRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	// git@host:path.git, the scp form. The user is always the literal git.
	repoSCPRE = regexp.MustCompile(`^git@([A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?):([A-Za-z0-9._~/-]+\.git)$`)
	repoSHARE = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
)

func init() { register(Repo, validateRepo, nil) }

// ParseRepo validates a canonical repo document and decodes it.
func ParseRepo(canonical []byte) (config.GitRepo, error) {
	if _, err := validateRepo(canonical); err != nil {
		return config.GitRepo{}, err
	}
	var r config.GitRepo
	if err := json.Unmarshal(canonical, &r); err != nil {
		return config.GitRepo{}, refuse("the document does not match the schema")
	}
	return r, nil
}

// ValidateRepo checks a repository from the user's settings against the same rules
// as a library document, so a hand-written entry is held to them too.
func ValidateRepo(r config.GitRepo) error {
	b, err := json.Marshal(RepoDocument(r))
	if err != nil {
		return err
	}
	_, err = Validate(Repo, b)
	return err
}

// RepoDocument is the library document for a repository: every key the library
// requires is present, so a hand-written entry that leaves the defaults out
// exports the same bytes as one that spells them.
func RepoDocument(r config.GitRepo) map[string]any {
	d := map[string]any{
		"name": r.Name, "url": r.URL, "visibility": r.Visibility, "auth": r.Auth,
		"refs": refsDocument(r.Refs), "dir": r.Subdir(), "depth": 0, "submodules": false, "enabled": true,
	}
	if r.InstallationID != nil {
		d["installation_id"] = *r.InstallationID
	}
	if r.Depth != nil {
		d["depth"] = *r.Depth
	}
	if r.Submodules != nil {
		d["submodules"] = *r.Submodules
	}
	if r.Enabled != nil {
		d["enabled"] = *r.Enabled
	}
	return d
}

func refsDocument(refs []config.GitRef) []map[string]string {
	out := make([]map[string]string, len(refs))
	for i, r := range refs {
		out[i] = map[string]string{"kind": r.Kind, "name": r.Name}
	}
	return out
}

// validateRepo checks a canonical repo document: every key but installation_id is
// required, and any other key is refused.
func validateRepo(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "repo", repoFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Repo, n) }, nameRule)
	if err != nil {
		return "", err
	}
	rawURL, err := reqStr(m, "url", "repo", MaxRepoURL)
	if err != nil {
		return "", err
	}
	if err := checkRepoURL(rawURL); err != nil {
		return "", err
	}
	visibility, err := reqStr(m, "visibility", "repo", 16)
	if err != nil {
		return "", err
	}
	auth, err := reqStr(m, "auth", "repo", 16)
	if err != nil {
		return "", err
	}
	if visibility != "public" && visibility != "private" {
		return "", refuse("repo.visibility must be public or private")
	}
	if auth != "none" && auth != "github_app" {
		return "", refuse("repo.auth must be none or github_app: a token is never stored here")
	}
	if visibility == "public" && auth != "none" {
		return "", refuse("a public repo uses auth none")
	}
	if visibility == "private" && auth != "github_app" {
		return "", refuse("a private repo uses auth github_app: a token is never stored here")
	}
	_, hasInstall, err := whole(m, "installation_id", "repo", 1, MaxRepoInstallation, 0)
	if err != nil {
		return "", err
	}
	switch {
	case visibility == "private" && !hasInstall:
		return "", refuse("repo.installation_id is required for a private repo")
	case visibility == "public" && hasInstall:
		return "", refuse("repo.installation_id is only allowed for a private repo")
	}
	if err := checkRepoRefs(m); err != nil {
		return "", err
	}
	dir, err := reqStr(m, "dir", "repo", MaxRepoDir)
	if err != nil {
		return "", err
	}
	if err := checkRepoDir(dir); err != nil {
		return "", err
	}
	if _, present, err := whole(m, "depth", "repo", 0, MaxRepoDepth, 0); err != nil || !present {
		if err == nil {
			err = refuse("repo.depth is required (0 clones the full history)")
		}
		return "", err
	}
	for _, key := range []string{"submodules", "enabled"} {
		if _, present := m[key]; !present {
			return "", refuse("repo.%s is required", key)
		}
		if _, err := boolean(m, key, "repo", false); err != nil {
			return "", err
		}
	}
	return name, nil
}

// checkRepoURL accepts https://host[:port]/path[.git] and git@host:path.git and
// refuses everything that could carry a credential or reach a different place than
// the one it names: userinfo, a query, a fragment, a backslash, a control or space,
// a dot-dot segment, a host that starts with a dash.
func checkRepoURL(raw string) error {
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) || r == '\\' {
			return refuse("repo.url holds a space, control character or backslash")
		}
	}
	if m := repoSCPRE.FindStringSubmatch(raw); m != nil {
		return checkRepoPath(m[2])
	}
	if strings.HasPrefix(raw, "git@") {
		return refuse("repo.url %q is not git@host:path.git", cleanForMessage(raw))
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" {
		return refuse("repo.url is not a valid URL")
	}
	if u.Scheme != "https" {
		return refuse("repo.url must be https://host/path or git@host:path.git")
	}
	if u.User != nil || strings.Contains(strings.SplitN(strings.TrimPrefix(raw, "https://"), "/", 2)[0], "@") {
		return refuse("repo.url must not carry credentials; a private repo uses the GitHub App")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "?#") {
		return refuse("repo.url must not carry a query or a fragment")
	}
	if !repoHostRE.MatchString(u.Hostname()) {
		return refuse("repo.url has no valid host name")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return refuse("repo.url has an invalid port")
		}
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path == "" {
		return refuse("repo.url has no repository path")
	}
	return checkRepoPath(path)
}

// checkRepoPath refuses an empty segment, a dot segment, and a segment that could
// be read as an option.
func checkRepoPath(path string) error {
	for _, seg := range strings.Split(strings.TrimSuffix(path, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, "-") {
			return refuse("repo.url has an empty, dot or dash-leading path segment")
		}
	}
	return nil
}

// checkRepoRefs checks refs: 1 to 16 distinct {kind, name}.
func checkRepoRefs(m map[string]any) error {
	refs, present, err := list(m, "refs", "repo", MaxRepoRefs)
	if err != nil {
		return err
	}
	if !present || len(refs) == 0 {
		return refuse("repo.refs needs at least one ref")
	}
	seen := map[string]bool{}
	for i, v := range refs {
		r, err := entry(v, "repo.refs", i)
		if err != nil {
			return err
		}
		where := "repo.refs[" + itoa(i) + "]"
		if err := onlyKeys(r, where, "kind", "name"); err != nil {
			return err
		}
		kind, err := reqStr(r, "kind", where, 16)
		if err != nil {
			return err
		}
		name, err := reqStr(r, "name", where, MaxRepoRef)
		if err != nil {
			return err
		}
		switch kind {
		case config.GitRefBranch, config.GitRefTag:
			if !ValidRefName(name) {
				return refuse("%s.name %q is not a valid git %s name", where, cleanForMessage(name), kind)
			}
		case config.GitRefSHA:
			if !repoSHARE.MatchString(name) {
				return refuse("%s.name must be 7 to 64 hex characters for a sha", where)
			}
		default:
			return refuse("%s.kind must be branch, tag or sha", where)
		}
		if seen[kind+"\x00"+name] {
			return refuse("%s repeats the %s %q", where, kind, cleanForMessage(name))
		}
		seen[kind+"\x00"+name] = true
	}
	return nil
}

// ValidRefName follows git check-ref-format for a branch or tag name given without
// its refs/ prefix, and is stricter where git is odd: no leading dash, no space or
// control, none of ~ ^ : ? * [ \, no "..", no "@{", no "//", no component that
// starts with a dot or ends in ".lock", no trailing dot or slash.
func ValidRefName(n string) bool {
	if n == "" || len(n) > MaxRepoRef || n == "@" || strings.HasPrefix(n, "-") || strings.HasPrefix(n, "/") ||
		strings.HasSuffix(n, "/") || strings.HasSuffix(n, ".") || strings.Contains(n, "..") || strings.Contains(n, "@{") ||
		strings.Contains(n, "//") {
		return false
	}
	for _, r := range n {
		if r <= ' ' || r == 0x7f || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, c := range strings.Split(n, "/") {
		if strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return false
		}
	}
	return true
}

// checkRepoDir checks the checkout directory, relative to the host's repos
// directory: no leading slash, no drive, no backslash, no dot or dot-dot segment.
func checkRepoDir(dir string) error {
	if strings.HasPrefix(dir, "/") || strings.HasPrefix(dir, "~") || strings.ContainsRune(dir, '\\') || strings.Contains(dir, ":") {
		return refuse("repo.dir must be a relative path under the host's repos directory")
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return refuse("repo.dir must not hold an empty, . or .. segment")
		}
	}
	return nil
}
