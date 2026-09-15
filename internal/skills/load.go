package skills

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/signet/internal/posture"
)

// LoadDir walks dir for skills/*/SKILL.md documents, validates each one, and
// returns the manifests in name order. Files that fail validation are skipped
// (or reported per posture) rather than half-loaded; the skill body is read
// here only to validate front-matter and is never returned.
func LoadDir(dir string, pol posture.Policy) ([]Manifest, error) {
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
