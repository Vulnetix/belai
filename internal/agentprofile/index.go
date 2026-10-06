package agentprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion counts changes to the profile's keys. It rises whenever a key
// is added to AgentProfile or one changes meaning, so a console that reads the
// builtin profiles from the repository can tell when the newest ones use keys a
// pinned Belai build does not know (such a build rejects an unknown key).
const SchemaVersion = 2

// IndexPath is where the committed index of the builtin files lives, relative to
// the repository root.
const IndexPath = "internal/agentprofile/builtins-index.json"

// IndexFile is one file of the index: where it is in the repository and the
// SHA-256 of its bytes.
type IndexFile struct {
	// Name is the profile's, crew's or skill's name, or the avatar's id.
	Name   string `json:"name"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Index lists everything Belai ships as a builtin: the profiles, the crews, the
// skills and the avatars. The website reads it from the repository to show the
// builtins without keeping a copy; a test keeps it equal to the files.
type Index struct {
	Schema   int         `json:"schema"`
	Profiles []IndexFile `json:"profiles"`
	Crews    []IndexFile `json:"crews"`
	Skills   []IndexFile `json:"skills"`
	Avatars  []IndexFile `json:"avatars"`
}

const (
	profilesDir = "internal/agentprofile/builtin"
	crewsDir    = "internal/agentprofile/crews"
	skillsDir   = "internal/skills/builtin"
)

// BuildIndex reads the builtin files under the repository root and returns
// their index. Names come from the files themselves.
func BuildIndex(root string) (Index, error) {
	idx := Index{Schema: SchemaVersion}
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) }
	sum := func(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

	files, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(profilesDir), "*.json"))
	if err != nil {
		return idx, err
	}
	for _, f := range files {
		rel := path.Join(profilesDir, filepath.Base(f))
		data, err := read(rel)
		if err != nil {
			return idx, err
		}
		var head struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &head); err != nil || head.Name == "" {
			return idx, fmt.Errorf("%s: no name", rel)
		}
		idx.Profiles = append(idx.Profiles, IndexFile{head.Name, rel, sum(data)})
	}
	files, err = filepath.Glob(filepath.Join(root, filepath.FromSlash(crewsDir), "*.json"))
	if err != nil {
		return idx, err
	}
	for _, f := range files {
		rel := path.Join(crewsDir, filepath.Base(f))
		data, err := read(rel)
		if err != nil {
			return idx, err
		}
		var head struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &head); err != nil || head.Name == "" {
			return idx, fmt.Errorf("%s: no name", rel)
		}
		idx.Crews = append(idx.Crews, IndexFile{head.Name, rel, sum(data)})
	}
	dirs, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(skillsDir)))
	if err != nil {
		return idx, err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		rel := path.Join(skillsDir, d.Name(), "SKILL.md")
		data, err := read(rel)
		if err != nil {
			return idx, err
		}
		idx.Skills = append(idx.Skills, IndexFile{d.Name(), rel, sum(data)})
	}
	files, err = filepath.Glob(filepath.Join(root, filepath.FromSlash(profilesDir), "avatars", "*.svg"))
	if err != nil {
		return idx, err
	}
	for _, f := range files {
		rel := path.Join(profilesDir, "avatars", filepath.Base(f))
		data, err := read(rel)
		if err != nil {
			return idx, err
		}
		id := strings.TrimSuffix(filepath.Base(f), ".svg")
		idx.Avatars = append(idx.Avatars, IndexFile{id, rel, sum(data)})
	}
	for _, l := range [][]IndexFile{idx.Profiles, idx.Crews, idx.Skills, idx.Avatars} {
		sort.Slice(l, func(i, j int) bool { return l[i].Name < l[j].Name })
	}
	return idx, nil
}

// MarshalIndex is the committed form of an index: indented JSON and a final
// newline.
func MarshalIndex(idx Index) ([]byte, error) {
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
