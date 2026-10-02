package libstore

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/skills"
)

func init() { register(libitem.Skill, skillStore{}) }

// skillStore keeps a skill as <skills dir>/<name>/SKILL.md, the layout the
// loader reads. The file holds the canonical document byte for byte, so a skill
// installed from the library hashes to what the library holds.
type skillStore struct{}

// maxSkillFile bounds a SKILL.md read from disk: the library's limit with room
// for the whitespace canonicalisation removes.
const maxSkillFile = 64 << 10

func skillsDir() (string, error) { return config.GlobalSkillsDir() }

func (skillStore) list() ([]Local, []Skipped, error) {
	root, err := skillsDir()
	if err != nil {
		return nil, nil, err
	}
	des, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var out []Local
	var skipped []Skipped
	seen := map[string]bool{}
	for _, de := range des {
		if !de.IsDir() {
			continue // a symlinked directory is not IsDir here, so it is never followed
		}
		path := filepath.Join(root, de.Name(), "SKILL.md")
		data, why := readRegular(path, maxSkillFile)
		if why != "" {
			if why != "missing" {
				skipped = append(skipped, Skipped{Kind: libitem.Skill, Name: de.Name(), Reason: why})
			}
			continue
		}
		it, err := libitem.Validate(libitem.Skill, data)
		if err != nil {
			skipped = append(skipped, Skipped{Kind: libitem.Skill, Name: de.Name(), Reason: err.Error()})
			continue
		}
		if seen[it.Name] {
			skipped = append(skipped, Skipped{Kind: libitem.Skill, Name: de.Name(), Reason: fmt.Sprintf("another directory already holds a skill named %s", it.Name)})
			continue
		}
		seen[it.Name] = true
		out = append(out, local(libitem.Skill, it.Name, it.Doc))
	}
	return out, skipped, nil
}

func (s skillStore) install(it libitem.Item, o InstallOptions) (Result, error) {
	if err := untrustedGate(it.Doc); err != nil {
		return Result{}, err
	}
	root, err := skillsDir()
	if err != nil {
		return Result{}, err
	}
	dir := filepath.Join(root, it.Name)
	path := filepath.Join(dir, "SKILL.md")
	replaced, err := existingTarget(dir, path)
	if err != nil {
		return Result{}, err
	}
	if replaced && !o.Overwrite {
		return Result{}, ErrExists
	}
	if err := config.WriteGlobalFileAtomic(path, it.Doc); err != nil {
		return Result{}, err
	}
	skills.Invalidate()
	return Result{Replaced: replaced, Where: path}, nil
}

// existingTarget reports whether dir or its file already exists, refusing a
// symlink at either: an install never writes through a link.
func existingTarget(dir, file string) (bool, error) {
	exists := false
	for _, p := range []string{dir, file} {
		fi, err := os.Lstat(p)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			return false, err
		case fi.Mode()&os.ModeSymlink != 0:
			return false, refusal("%s is a symbolic link, so nothing was written", p)
		}
		exists = true
	}
	return exists, nil
}

// readRegular reads a regular file of at most max bytes. why is "missing" for a
// file that is not there, and a reason for one that is not usable.
func readRegular(path string, max int64) (data []byte, why string) {
	fi, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		return nil, "missing"
	case err != nil:
		return nil, err.Error()
	case !fi.Mode().IsRegular():
		return nil, "it is not a regular file"
	case fi.Size() > max:
		return nil, fmt.Sprintf("the file is larger than %d bytes", max)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		return nil, err.Error()
	}
	return data, ""
}
