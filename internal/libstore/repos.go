package libstore

import (
	"encoding/json"
	"fmt"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
)

func init() { register(libitem.Repo, repoStore{}) }

// repoStore keeps a repository in the user's own settings.json, under `repos`. A
// repository is deployment configuration: the entry says what to clone and which
// refs to keep, holds no credential, and is acted on only by `belai repo sync`.
// Only the global settings file is read or written; a project settings file's
// `repos` is dropped by the resolver and never listed here.
type repoStore struct{}

func (repoStore) list() ([]Local, []Skipped, error) {
	s, err := config.LoadGlobal()
	if err != nil {
		return nil, nil, err
	}
	var out []Local
	var skipped []Skipped
	seen := map[string]bool{}
	for _, r := range s.GitRepos {
		b, err := json.Marshal(libitem.RepoDocument(r))
		if err != nil {
			return nil, nil, err
		}
		it, err := libitem.Validate(libitem.Repo, b)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Repo, Name: r.Name, Reason: err.Error()})
			continue
		}
		if seen[it.Name] {
			skipped = append(skipped, Skipped{Kind: libitem.Repo, Name: r.Name, Reason: "another entry already has this name"})
			continue
		}
		seen[it.Name] = true
		out = append(out, local(libitem.Repo, it.Name, it.Doc))
	}
	return out, skipped, nil
}

func (repoStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	r, err := libitem.ParseRepo(it.Doc)
	if err != nil {
		return Result{}, err
	}
	path, err := config.GlobalSettingsPath()
	if err != nil {
		return Result{}, err
	}
	replaced := false
	err = config.Mutate(config.ScopeGlobal, "", func(s *config.Settings) error {
		idx := -1
		for i, e := range s.GitRepos {
			if e.Name == r.Name {
				idx = i
			}
		}
		for i, e := range s.GitRepos {
			if i != idx && e.Subdir() == r.Subdir() {
				return refusal("the repository %q already uses the directory %q; choose another dir in the library", e.Name, r.Subdir())
			}
		}
		if idx >= 0 {
			if !o.Overwrite {
				return ErrExists
			}
			replaced = true
			s.GitRepos[idx] = r
			return nil
		}
		s.GitRepos = append(s.GitRepos, r)
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return Result{Replaced: replaced, Where: fmt.Sprintf("%s (repos)", path)}, nil
}
