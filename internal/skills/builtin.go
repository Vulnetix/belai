package skills

import (
	"embed"
	"fmt"
	"sort"
	"strings"
)

// BuiltinSource is the Source of a skill that ships inside Belai.
const BuiltinSource = "builtin"

// BuiltinPrefix starts the name of every builtin skill. A name that starts with
// it is reserved: a user, a plugin and the library cannot create one, so a
// builtin is never shadowed or replaced.
const BuiltinPrefix = "belai-"

// ReservedName reports whether name belongs to Belai's builtin skills.
func ReservedName(name string) bool { return strings.HasPrefix(name, BuiltinPrefix) }

//go:embed builtin/*/SKILL.md
var builtinFS embed.FS

// Builtin lists the skills that ship with Belai, in name order. They are the
// specialist guidance of the builtin worker profiles and are offered only to a
// session whose profile names them (agentprofile skills), never to every
// session. A builtin that does not validate is left out; a test holds the
// shipped set to the rules, so this never happens in a release.
func Builtin() []Entry {
	dirs, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil
	}
	var out []Entry
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		path := "builtin/" + d.Name() + "/SKILL.md"
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			continue
		}
		m, err := ValidateSkill(string(data))
		if err != nil || m.Name != d.Name() {
			continue
		}
		out = append(out, Entry{Manifest: *m, Name: m.Name, Path: path, Source: BuiltinSource})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltinDoc returns the full SKILL.md text of the builtin skill name.
func BuiltinDoc(name string) (string, error) {
	if !ValidSpecName(name) {
		return "", fmt.Errorf("no builtin skill named %q", name)
	}
	data, err := builtinFS.ReadFile("builtin/" + name + "/SKILL.md")
	if err != nil {
		return "", fmt.Errorf("no builtin skill named %q", name)
	}
	return string(data), nil
}
