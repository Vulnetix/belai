package agentscan

import "path/filepath"

// scanOther explains agents that are installed but hold no importable key, so
// absence is explained rather than silent. Qwen, Crush and Aider have no
// on-disk key store; Gemini stores keys in the environment or OAuth only.
func scanOther(home string) []Found {
	var out []Found
	checks := []struct {
		agent string
		dir   string
		why   string
	}{
		{"gemini", filepath.Join(home, ".gemini"), "no key store (env or OAuth)"},
		{"qwen", filepath.Join(home, ".qwen"), "no key store"},
		{"crush", filepath.Join(configDir(home), "crush"), "no key store"},
		{"aider", filepath.Join(home, ".aider-desk"), "no key store"},
	}
	for _, c := range checks {
		if dirExists(c.dir) {
			out = append(out, note(c.agent, c.dir, c.why))
		}
	}
	return out
}
