package knowledge

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/vulnetix/belai/internal/config"
)

// Scope names for IndexInfo.
const (
	// ScopeProject is a project's index, keyed by its root.
	ScopeProject = "project"
	// ScopeProfile is an agent profile's index, keyed by its id.
	ScopeProfile = "profile"
)

// IndexInfo is one persisted index found on this host, loaded read-only. It is
// what a browsing view shows: the catalogue never writes, so a corrupt file is
// reported and left as it is.
type IndexInfo struct {
	// Scope is ScopeProject or ScopeProfile.
	Scope string
	// Key is the project key (SHA-256 of the root) or the profile id.
	Key string
	// Name labels the index: the profile's name, or the project's.
	Name string
	// Root is the project's root when the caller knows it. The key does not
	// give it back.
	Root string
	// Index is the loaded index; nil when Err is set.
	Index *Index
	// Err is why the index could not be loaded (ErrCorrupt for a file that
	// fails its integrity check).
	Err error
	// Bytes and Updated describe the file on disk; zero when there is none.
	Bytes   int64
	Updated time.Time
}

// Docs is the number of documents the index holds.
func (i IndexInfo) Docs() int {
	if i.Index == nil {
		return 0
	}
	return len(i.Index.Docs())
}

// Tokens is the estimated size of what the index holds.
func (i IndexInfo) Tokens() int {
	if i.Index == nil {
		return 0
	}
	return i.Index.Tokens()
}

// Exists reports whether there is an index file on disk.
func (i IndexInfo) Exists() bool { return !i.Updated.IsZero() }

var projectKeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ProjectKey is the key a project's index is stored under: the SHA-256 of its
// resolved root, as ProjectKnowledgeDir names it.
func ProjectKey(root string) (string, error) {
	dir, err := config.ProjectKnowledgeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Base(dir), nil
}

// ProjectKeys lists the project keys that have a directory on disk, sorted.
func ProjectKeys() ([]string, error) {
	base, err := config.KnowledgeDir()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(filepath.Join(base, "projects"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && projectKeyRe.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// LoadProject loads the index of the project rooted at root.
func LoadProject(root string) IndexInfo {
	key, err := ProjectKey(root)
	if err != nil {
		return IndexInfo{Scope: ScopeProject, Root: root, Err: err}
	}
	return LoadProjectKey(key, filepath.Base(root), root)
}

// LoadProjectKey loads the project index stored under key (a project key from
// ProjectKeys). name and root are labels the caller knows and may be empty.
func LoadProjectKey(key, name, root string) IndexInfo {
	info := IndexInfo{Scope: ScopeProject, Key: key, Name: name, Root: root}
	if !projectKeyRe.MatchString(key) {
		info.Err = errors.New("knowledge: not a project key")
		return info
	}
	base, err := config.KnowledgeDir()
	if err != nil {
		info.Err = err
		return info
	}
	return loadInfo(info, filepath.Join(base, "projects", key), ProjectScope)
}

// LoadProfile loads the index of the agent profile with the given id.
func LoadProfile(id, name string) IndexInfo {
	info := IndexInfo{Scope: ScopeProfile, Key: id, Name: name}
	dir, err := config.ProfileKnowledgeDir(id)
	if err != nil {
		info.Err = err
		return info
	}
	return loadInfo(info, dir, name)
}

func loadInfo(info IndexInfo, dir, indexName string) IndexInfo {
	if fi, err := os.Lstat(Path(dir)); err == nil {
		info.Bytes, info.Updated = fi.Size(), fi.ModTime()
	}
	ix, err := Load(dir, indexName)
	if err != nil {
		info.Err = err
		return info
	}
	info.Index = ix
	return info
}
