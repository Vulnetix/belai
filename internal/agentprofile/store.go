package agentprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// Dir returns the agent-profiles directory (~/.belai/profiles/agents).
func Dir() (string, error) {
	gd, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gd, "profiles", "agents"), nil
}

// Save writes a profile and returns its path. It rejects names that would
// overwrite a built-in profile once sanitised. A profile saved without an id
// keeps the id of the file it replaces, or is given a new one, and a display
// name another profile on this host already holds is refused.
func Save(p AgentProfile) (string, error) { return save(p, "") }

// SaveReplacing is Save for a profile that takes over the one this host holds
// under the same name, whatever its id: that profile's display name is not a
// collision, because the save replaces it in place. Nothing else is touched.
func SaveReplacing(p AgentProfile) (string, error) { return save(p, p.Name) }

func save(p AgentProfile, replaces string) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if !p.Builtin && collidesWithBuiltin(p) {
		return "", fmt.Errorf("profile name %q collides with a built-in profile", p.Name)
	}
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, p.FileName())
	// A rename must never clobber a sibling profile: refuse to write a path
	// that already exists and holds a different Name.
	if existing, err := os.ReadFile(path); err == nil {
		var prev AgentProfile
		if json.Unmarshal(existing, &prev) == nil {
			if prev.Name != "" && prev.Name != p.Name {
				return "", fmt.Errorf("refusing to overwrite %s: it holds profile %q", p.FileName(), prev.Name)
			}
			if p.ID == "" && ValidID(prev.ID) {
				p.ID = prev.ID
			}
		}
	}
	if p.ID == "" && !p.Builtin {
		if p.ID, err = NewID(); err != nil {
			return "", err
		}
	}
	if p.DisplayName != "" {
		if holder, taken, err := displayNameTaken(DisplayNameKey(p.DisplayName), p.ID, replaces); err != nil {
			return "", err
		} else if taken {
			return "", fmt.Errorf("display name %q is already used by profile %q on this host", p.DisplayName, holder)
		}
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// writeAtomic writes data to path through a temporary file in the same
// directory, so a reader never sees half a profile and a crash leaves the
// old one.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".profile-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// stored reads every profile file in the directory leniently: a file that
// does not parse is skipped, so one broken profile cannot block an identity
// lookup. File is set on each result.
func stored() ([]AgentProfile, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []AgentProfile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p AgentProfile
		if json.Unmarshal(data, &p) != nil || p.Name == "" {
			continue
		}
		p.File = e.Name()
		out = append(out, p)
	}
	return out, nil
}

// EnsureIDs gives every stored profile that lacks one an id and rewrites it
// in place, returning how many it stamped. Run when the rc daemon starts so
// a profile has the same identity every time the website sees it.
func EnsureIDs() (int, error) {
	list, err := stored()
	if err != nil {
		return 0, err
	}
	dir, err := Dir()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range list {
		if ValidID(p.ID) {
			continue
		}
		if p.ID, err = NewID(); err != nil {
			return n, err
		}
		file := p.File
		p.File = ""
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return n, err
		}
		if err := writeAtomic(filepath.Join(dir, file), data); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ByID finds a profile by its id: a stored profile, a plugin profile or a
// built-in. The empty and malformed ids match nothing.
func ByID(id string) (AgentProfile, bool) {
	if !ValidID(id) {
		return AgentProfile{}, false
	}
	list, _ := stored()
	for _, p := range list {
		if p.ID == id {
			return p, true
		}
	}
	if ExtraProfiles != nil {
		for _, p := range ExtraProfiles() {
			if p.ID == id {
				return p, true
			}
		}
	}
	for _, p := range builtinProfiles {
		if p.ID == id {
			return p, true
		}
	}
	return AgentProfile{}, false
}

// DisplayNameTaken reports whether a stored profile other than exceptID
// already holds the display-name key (see DisplayNameKey), and its name. The
// store is per host, so this is the host half of the (display name, host)
// uniqueness rule.
func DisplayNameTaken(key, exceptID string) (holder string, taken bool, err error) {
	return displayNameTaken(key, exceptID, "")
}

// displayNameTaken is DisplayNameTaken that also lets the profile named
// exceptName hold the key, for a save that replaces it.
func displayNameTaken(key, exceptID, exceptName string) (holder string, taken bool, err error) {
	if key == "" {
		return "", false, nil
	}
	list, err := stored()
	if err != nil {
		return "", false, err
	}
	for _, p := range list {
		if p.DisplayName != "" && DisplayNameKey(p.DisplayName) == key && (exceptID == "" || p.ID != exceptID) && (exceptName == "" || p.Name != exceptName) {
			return p.Name, true, nil
		}
	}
	return "", false, nil
}

// SaveMoving saves p and removes oldFile when it names a different on-disk
// file. A removal failure is reported rather than swallowed, so a rename that
// writes the new file but cannot drop the old one is visible.
func SaveMoving(p AgentProfile, oldFile string) (string, error) {
	path, err := Save(p)
	if err != nil {
		return "", err
	}
	if oldFile != "" && oldFile != p.FileName() {
		dir, derr := Dir()
		if derr != nil {
			return path, derr
		}
		if rerr := os.Remove(filepath.Join(dir, oldFile)); rerr != nil {
			return path, rerr
		}
	}
	return path, nil
}

// Load reads a profile by name. Built-in names resolve from the embedded set
// and never touch disk, so a user file named belai_triage-vulns.json cannot
// shadow belai:triage-vulns.
func Load(name string) (AgentProfile, error) {
	if p, ok := extraProfile(name); ok {
		return p, nil
	}
	if IsBuiltin(name) {
		p, ok := builtinProfiles[name]
		if !ok {
			return AgentProfile{}, fmt.Errorf("unknown built-in profile %q", name)
		}
		return p, nil
	}
	dir, err := Dir()
	if err != nil {
		return AgentProfile{}, err
	}
	p := AgentProfile{Name: name}
	fileName := p.FileName()
	path := filepath.Join(dir, fileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return AgentProfile{}, err
		}
		// The profile may have been renamed on disk. Scan for a file whose
		// embedded Name matches, so /agent start <name> and the picker keep
		// resolving after a file-name edit.
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return AgentProfile{}, err
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(dir, e.Name()))
			if readErr != nil {
				continue
			}
			var cand AgentProfile
			if json.Unmarshal(data, &cand) != nil {
				continue
			}
			if cand.Name != name {
				continue
			}
			cand.File = e.Name()
			if vErr := cand.Validate(); vErr != nil {
				return AgentProfile{}, fmt.Errorf("invalid profile %s: %w", name, vErr)
			}
			return cand, nil
		}
		return AgentProfile{}, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return AgentProfile{}, err
	}
	p.File = fileName
	if err := p.Validate(); err != nil {
		return AgentProfile{}, fmt.Errorf("invalid profile %s: %w", name, err)
	}
	return p, nil
}

// List returns all stored agent profiles plus built-ins, sorted by name.
func List() ([]AgentProfile, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var out []AgentProfile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".json")
		p, err := Load(name)
		if err != nil {
			return nil, err
		}
		// Load sets File from the entry name; pin it again here so the record
		// survives any future Load fast-path change.
		p.File = e.Name()
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// Plugin profiles follow the user's, named "plugin:name" so they can
	// never shadow a user or built-in profile.
	if ExtraProfiles != nil {
		out = append(out, ExtraProfiles()...)
	}
	// Append built-ins after user profiles so they are selectable but cannot be
	// clobbered on disk.
	for _, n := range builtinNames() {
		out = append(out, builtinProfiles[n])
	}
	return out, nil
}

// Delete removes a profile by name. Built-in profiles cannot be deleted.
func Delete(name string) error {
	if IsBuiltin(name) {
		return fmt.Errorf("cannot delete built-in profile %q", name)
	}
	dir, err := Dir()
	if err != nil {
		return err
	}
	p := AgentProfile{Name: name}
	return os.Remove(filepath.Join(dir, p.FileName()))
}

// collidesWithBuiltin reports whether the sanitised filename of p matches a
// built-in's sanitised filename.
func collidesWithBuiltin(p AgentProfile) bool {
	return collidesWithBuiltinFileName(p)
}

// ExtraProfiles returns profiles from enabled plugins, each already
// validated and named "plugin:name". nil means none. Set once at startup.
var ExtraProfiles func() []AgentProfile

// extraProfile finds a plugin profile by exact name. Only a namespaced name
// (with a colon, outside the built-in prefix) can match.
func extraProfile(name string) (AgentProfile, bool) {
	if ExtraProfiles == nil || IsBuiltin(name) || !strings.Contains(name, ":") {
		return AgentProfile{}, false
	}
	for _, p := range ExtraProfiles() {
		if p.Name == name {
			return p, true
		}
	}
	return AgentProfile{}, false
}
