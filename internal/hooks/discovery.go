package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/vulnetix/signet/internal/posture"
)

// LoadDir walks dir for *.json hook definitions, validates each one (honouring
// the hook_invalid posture), and returns them sorted by name. Invalid files
// are skipped — fail closed: an invalid hook is never loaded.
func LoadDir(dir string, pol posture.Policy) ([]*Hook, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Hook
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var h Hook
		if err := json.Unmarshal(data, &h); err != nil {
			continue
		}
		validated, err := ValidateWithPosture(h, pol)
		if err != nil || validated == nil {
			continue
		}
		out = append(out, validated)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
