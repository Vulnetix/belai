package profiles

import (
	"embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed builtin/*.json
var builtinFS embed.FS

var builtinProfiles = loadBuiltins()

func loadBuiltins() map[string]Profile {
	files, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return map[string]Profile{}
	}
	m := make(map[string]Profile, len(files))
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		data, err := builtinFS.ReadFile("builtin/" + f.Name())
		if err != nil {
			continue
		}
		var p Profile
		if err := json.Unmarshal(data, &p); err != nil {
			continue
		}
		if err := validate(p, true); err != nil {
			continue
		}
		p.Builtin = true
		m[p.Name] = p
	}
	return m
}

// builtins returns built-in profiles sorted by name.
func builtins() []Profile {
	names := make([]string, 0, len(builtinProfiles))
	for n := range builtinProfiles {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Profile, 0, len(names))
	for _, n := range names {
		out = append(out, builtinProfiles[n])
	}
	return out
}

func builtinByName(name string) (Profile, bool) {
	p, ok := builtinProfiles[name]
	return p, ok
}

// DebugProfile is the built-in profile engaged by inline-shell turns.
const DebugProfile = BuiltinPrefix + "debug"
