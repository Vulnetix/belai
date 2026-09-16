package skills

import (
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/vulnetix/signet/internal/posture"
)

// loadCache memoises the last loaded directory. Skills are read from disk on
// every turn and again per explore subagent; they are static for a process
// lifetime, so the result is cached. The key is the directory plus the single
// posture gate that affects loading (skill_invalid), since that is the only
// input that changes the outcome.
var (
	loadMu     sync.Mutex
	loadKey    string
	loadVal    []Manifest
	loadValErr error
	loadValSet bool
)

// LoadDir walks dir for skills/*/SKILL.md documents, validates each one, and
// returns the manifests in name order. Files that fail validation are skipped
// (or reported per posture) rather than half-loaded; the skill body is read
// here only to validate front-matter and is never returned. Results are
// memoised per (dir, posture) for the process lifetime.
func LoadDir(dir string, pol posture.Policy) ([]Manifest, error) {
	key := dir + "\x00" + string(pol.Level(posture.SkillInvalid))
	loadMu.Lock()
	defer loadMu.Unlock()
	if loadValSet && loadKey == key {
		return loadVal, loadValErr
	}
	out, err := loadDir(dir, pol)
	loadKey = key
	loadVal = out
	loadValErr = err
	loadValSet = true
	return out, err
}

func loadDir(dir string, pol posture.Policy) ([]Manifest, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name(), "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		m, err := ValidateWithPosture(string(data), pol)
		if err != nil {
			continue // fail closed: an invalid skill is not loaded
		}
		if m == nil {
			continue // warn posture skipped it
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
