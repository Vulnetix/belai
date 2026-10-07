package components

import (
	"regexp"
	"strings"
	"sync/atomic"
)

// macKeyHints is the one switch for Mac media-key hints. It is set from the
// settings (ui.mac_key_hints, which follows the platform when unset) and read
// whenever a hint is drawn, so a /settings change shows on the next frame.
var macKeyHints atomic.Bool

// SetMacKeyHints turns the Mac media-key form of F-key hints on or off.
func SetMacKeyHints(on bool) { macKeyHints.Store(on) }

// MacKeyHintsOn reports whether F-key hints carry the Mac form.
func MacKeyHintsOn() bool { return macKeyHints.Load() }

// macFKeys is what each F key does on a Mac keyboard without Fn: the key is a
// media or system key, so pressing it with Fn held reaches the function key.
// The glyphs are the ones printed on the keycaps.
var macFKeys = map[string]string{
	"f1": "brightness down", "f2": "brightness up", "f3": "Mission Control",
	"f4": "Launchpad", "f5": "dictation", "f6": "Do Not Disturb",
	"f7": "⏮", "f8": "⏯", "f9": "⏭",
	"f10": "mute", "f11": "volume down", "f12": "volume up",
}

// MacFKeyHint returns the Mac form of an F key, such as "f9 (Fn+⏭)", or key
// unchanged when the hints are off or key is not f1 to f12.
func MacFKeyHint(key string) string {
	if !macKeyHints.Load() {
		return key
	}
	if label, ok := macFKeys[strings.ToLower(key)]; ok {
		return key + " (Fn+" + label + ")"
	}
	return key
}

// fKeyToken finds an F-key name standing alone as a word. The match is
// case-sensitive on purpose: the bindings are written in lower case.
var fKeyToken = regexp.MustCompile(`\bf(?:1[0-2]|[1-9])\b`)

// MacKeyText adds the Mac form after the first mention of each F key in s
// when the hints are on, and returns s untouched when they are off. A mention
// that already carries "(Fn+" is left alone, so applying it twice changes
// nothing. A path or a word such as "f1.txt" is not a key, so a match followed
// by a dot or slash is skipped.
func MacKeyText(s string) string {
	if !macKeyHints.Load() || !strings.Contains(s, "f") {
		return s
	}
	seen := map[string]bool{}
	var b strings.Builder
	last := 0
	for _, m := range fKeyToken.FindAllStringIndex(s, -1) {
		start, end := m[0], m[1]
		key := s[start:end]
		if seen[key] {
			continue
		}
		if end < len(s) && (s[end] == '.' || s[end] == '/' || s[end] == '_' || s[end] == '-') {
			continue
		}
		if start > 0 && (s[start-1] == '/' || s[start-1] == '.' || s[start-1] == '_' || s[start-1] == '-') {
			continue
		}
		if strings.HasPrefix(s[end:], " (Fn+") {
			seen[key] = true
			continue
		}
		seen[key] = true
		b.WriteString(s[last:end])
		b.WriteString(" (Fn+" + macFKeys[key] + ")")
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}
