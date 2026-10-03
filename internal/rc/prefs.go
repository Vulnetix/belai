package rc

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// Project preferences from the web (`belai rc --web-project-settings`). The
// website reads each offered directory's preferences from the advertisement
// and edits them with a "project_prefs" request. The file written is the
// host-private preference file for the directory (config.ProjectPrefs under the
// user's state directory), never the repository's settings: only its flat keys
// can be named, each value is checked against the key's shape, and the result
// must pass the settings validators before it is written. A running session
// keeps its settings; the next one reads the new preferences.

// maxPrefChanges caps the keys one request may touch.
const maxPrefChanges = 128

// DirPrefs is one directory's preferences as advertised.
type DirPrefs struct {
	Prefs     map[string]any
	Effective map[string]sessionsync.RCEffective
}

// localPrefs reads each offered directory's preferences and resolved values.
// A directory whose settings cannot be read is left out.
func localPrefs(dirs []Dir) map[string]DirPrefs {
	out := map[string]DirPrefs{}
	for _, d := range dirs {
		p, err := config.LoadProjectPrefs(d.Path)
		if err != nil {
			continue
		}
		eff, err := config.Resolve(d.Path, os.Getenv, config.Settings{})
		if err != nil {
			continue
		}
		mode := p.Mode
		if mode == "" {
			mode = "auto"
		}
		dp := DirPrefs{Prefs: p.Flat(), Effective: map[string]sessionsync.RCEffective{}}
		for k, v := range config.EffectivePrefs(eff, mode) {
			dp.Effective[k] = sessionsync.RCEffective{Value: v.Value, Origin: string(v.Origin)}
		}
		out[d.Path] = dp
	}
	return out
}

// setProjectPrefs applies a "project_prefs" request. It returns the report for
// the acknowledgement, or the refusal reason.
func (d *Daemon) setProjectPrefs(r sessionsync.Dispatch) (string, string) {
	if !d.o.ProjectSettings {
		return "", "this host does not let the website change project settings; start belai rc with --web-project-settings"
	}
	dir, ok := Allowed(d.o.Dirs, r.Cwd)
	if !ok {
		return "", "that directory is not offered by belai rc on this host"
	}
	if n := len(r.PrefsSet) + len(r.PrefsUnset); n == 0 {
		return "", "the request changes nothing"
	} else if n > maxPrefChanges {
		return "", fmt.Sprintf("a request may change at most %d preferences", maxPrefChanges)
	}
	global, err := config.LoadGlobal()
	if err != nil {
		return "", "the host's settings could not be read"
	}
	var why string
	err = config.MutateProjectPrefs(dir, func(p *config.ProjectPrefs) {
		next := *p
		for k, v := range r.PrefsSet {
			if err := next.SetFlat(k, v); err != nil {
				why = err.Error()
				return
			}
		}
		for _, k := range r.PrefsUnset {
			if err := next.UnsetFlat(k); err != nil {
				why = err.Error()
				return
			}
		}
		if err := next.ValidateOver(global); err != nil {
			why = err.Error()
			return
		}
		*p = next
	})
	if why != "" {
		return "", clip(why)
	}
	if err != nil {
		return "", "the preferences could not be written on the host"
	}
	return fmt.Sprintf("set %d and cleared %d preference%s for %s", len(r.PrefsSet), len(r.PrefsUnset),
		plural(len(r.PrefsSet)+len(r.PrefsUnset), "", "s"), filepath.Base(dir)), ""
}
