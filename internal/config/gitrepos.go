package config

import "path/filepath"

// GitRepo is one repository the user wants kept on this machine: where to clone
// it from, which refs to keep, and where. It is deployment configuration only.
// `belai repo sync` (docs/library-items.md) clones and updates it with git;
// nothing here holds a credential, and the Vulnetix library never clones.
//
// The list is the user's own: the project layer is dropped in Resolve, so a
// repository cannot make a host clone another.
type GitRepo struct {
	// Name identifies the repository in the library and on the command line.
	Name string `json:"name"`
	// URL is https://host/path[.git] or git@host:path.git, with no credentials.
	URL string `json:"url"`
	// Visibility is "public" or "private".
	Visibility string `json:"visibility"`
	// Auth is "none" (public) or "github_app" (private).
	Auth string `json:"auth"`
	// InstallationID names the GitHub App installation of a private repository.
	InstallationID *int64 `json:"installation_id,omitempty"`
	// Refs are the branches, tags and commits to keep. The first is checked out;
	// the rest are fetched.
	Refs []GitRef `json:"refs"`
	// Dir is the directory under the repos directory. Empty means Name.
	Dir string `json:"dir,omitempty"`
	// Depth limits the history fetched; nil or 0 means all of it.
	Depth *int `json:"depth,omitempty"`
	// Submodules also updates submodules.
	Submodules *bool `json:"submodules,omitempty"`
	// Enabled is false to leave the repository alone without deleting it. nil
	// means on.
	Enabled *bool `json:"enabled,omitempty"`
}

// GitRef is one ref of a repository: a branch, a tag or a commit (kind "sha").
type GitRef struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Ref kinds.
const (
	GitRefBranch = "branch"
	GitRefTag    = "tag"
	GitRefSHA    = "sha"
)

// IsEnabled reports whether the repository is switched on.
func (r GitRepo) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// Subdir is the repository's directory under the repos directory.
func (r GitRepo) Subdir() string {
	if r.Dir != "" {
		return r.Dir
	}
	return r.Name
}

// ReposDir is <GlobalDir>/repos, the directory `belai repo sync` keeps clones
// under.
func ReposDir() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "repos"), nil
}

// GitRepoNamed returns the repository of that name from the user's own settings.
func (s Settings) GitRepoNamed(name string) (GitRepo, bool) {
	for _, r := range s.GitRepos {
		if r.Name == name {
			return r, true
		}
	}
	return GitRepo{}, false
}
