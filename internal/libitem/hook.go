package libitem

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// A hook item is a coding agent's lifecycle hooks as a portable definition: the
// `hooks` block of Claude Code's and Codex's settings, with a name and a
// description in front, plus script files that travel beside it (bundle.go in
// libstore unpacks them). These are vdb-site's belaiValidateHook rules, and the
// tests pin the numbers.
//
//	{"name": "pix", "description": "…",
//	 "hooks": {"PreToolUse": [{"matcher": "Bash|Edit",
//	           "hooks": [{"type": "command", "command": "vulnetix agent hook", "timeout": 30}]}]}}
//
// The document is only shape. Whether a command can run on this host (a file the
// bundle carries, or a program the user allowed) is decided when the bundle is
// installed, by internal/hooks, with the host's own settings.

// Hook limits.
const (
	MaxHookDescription = 300
	MaxHookGroups      = 8
	MaxHookHandlers    = 8
	MaxHookMatcher     = 128
	MaxHookCommand     = 512
	MaxHookTimeout     = 600
)

// HookDefinitionFile is the file the document is stored as inside the bundle
// directory, and so a name no bundle file may take.
const HookDefinitionFile = "hooks.json"

var hookFields = []string{"name", "description", "hooks"}

var hookEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "UserPromptSubmit": true, "SessionStart": true,
	"SessionEnd": true, "Stop": true, "SubagentStop": true, "Notification": true, "PreCompact": true,
}

// hookNoMatcher are the events with no tool or source to match.
var hookNoMatcher = map[string]bool{"UserPromptSubmit": true, "Stop": true, "SubagentStop": true}

func init() { register(Hook, validateHook, nil) }

// validateHook checks a canonical hook document and returns its name.
func validateHook(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "hook", hookFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(Hook, n) }, nameRule)
	if err != nil {
		return "", err
	}
	if _, err := optStr(m, "description", "hook", MaxHookDescription, false); err != nil {
		return "", err
	}
	events, present, err := obj(m, "hooks", "hook")
	if err != nil {
		return "", err
	}
	if !present || len(events) == 0 {
		return "", refuse("hook.hooks must name at least one event")
	}
	for _, ev := range sortedKeys(events) {
		if !hookEvents[ev] {
			return "", refuse("hook.hooks.%s is not an event a hook can name", cleanForMessage(ev))
		}
		where := "hook.hooks." + ev
		groups, ok := events[ev].([]any)
		if !ok || len(groups) == 0 {
			return "", refuse("%s must be a list of at least one group", where)
		}
		if len(groups) > MaxHookGroups {
			return "", refuse("%s has %d groups; the most is %d", where, len(groups), MaxHookGroups)
		}
		for i, g := range groups {
			gw := idx(where, i)
			group, err := entry(g, where, i)
			if err != nil {
				return "", err
			}
			if err := onlyKeys(group, gw, "matcher", "hooks"); err != nil {
				return "", err
			}
			matcher, err := optStr(group, "matcher", gw, MaxHookMatcher, false)
			if err != nil {
				return "", err
			}
			if _, set := group["matcher"]; set && hookNoMatcher[ev] && matcher != "" {
				return "", refuse("%s.matcher: %s has nothing to match", gw, ev)
			}
			handlers, listed, err := list(group, "hooks", gw, MaxHookHandlers)
			if err != nil {
				return "", err
			}
			if !listed || len(handlers) == 0 {
				return "", refuse("%s.hooks must list at least one handler", gw)
			}
			for j, h := range handlers {
				hw := idx(gw+".hooks", j)
				handler, err := entry(h, gw+".hooks", j)
				if err != nil {
					return "", err
				}
				if err := onlyKeys(handler, hw, "type", "command", "timeout"); err != nil {
					return "", err
				}
				typ, _, err := str(handler, "type", hw)
				if err != nil {
					return "", err
				}
				if typ != "command" {
					return "", refuse("%s.type must be command", hw)
				}
				cmd, err := reqStr(handler, "command", hw, MaxHookCommand)
				if err != nil {
					return "", err
				}
				if strings.TrimSpace(cmd) != cmd {
					return "", refuse("%s.command must not start or end with a space", hw)
				}
				if _, _, err := whole(handler, "timeout", hw, 1, MaxHookTimeout, 0); err != nil {
					return "", err
				}
			}
		}
	}
	return name, nil
}

// BundleFile is one script or data file of a hook bundle, as the library and the
// host list it: a relative path and the SHA-256 of its bytes.
type BundleFile struct {
	Path   string
	SHA256 string
}

// HashBundle is the hash the library and the host compare for a hook: the
// SHA-256 of the canonical document alone when the bundle carries no files, and
// otherwise of the document, a "--files--" line and one "path sha256" line per
// file in path order. A change to a script therefore changes the hash, which is
// what makes drift see it. vdb-site's belaiBundleHash is the reference, and the
// golden tests in both pin the same values.
func HashBundle(canonical []byte, files []BundleFile) string {
	if len(files) == 0 {
		return Hash(canonical)
	}
	sorted := append([]BundleFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var b strings.Builder
	b.Write(canonical)
	b.WriteString("\n--files--\n")
	for _, f := range sorted {
		b.WriteString(f.Path)
		b.WriteByte(' ')
		b.WriteString(f.SHA256)
		b.WriteByte('\n')
	}
	h := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(h[:])
}
