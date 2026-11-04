package proc

import (
	"os"
	"strings"
)

// ScrubbedEnv returns a minimal environment with provider credentials and
// Belai config stripped so subprocess output cannot accidentally exfiltrate
// them to a model.
func ScrubbedEnv() []string {
	return ScrubEnvOf(os.Environ())
}

// ScrubEnvOf is ScrubbedEnv over an explicit environment.
func ScrubEnvOf(env []string) []string {
	return ScrubEnvKeeping(env, nil)
}

// ScrubEnvKeeping is ScrubEnvOf, except that a variable whose name is in keep
// (matched exactly) survives even when its name looks like a credential. It is
// for the names the secrets vault grants this machine's commands: those were
// handed to the command on purpose, so an AWS_SESSION_TOKEN the vault grants
// is not dropped for ending in _TOKEN.
func ScrubEnvKeeping(env []string, keep []string) []string {
	var out []string
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if sensitiveName(key) && !inList(key, keep) {
			continue
		}
		out = append(out, e)
	}
	return out
}

func sensitiveName(key string) bool {
	upper := strings.ToUpper(key)
	return strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
		strings.HasPrefix(upper, "CLOUDFLARE_") || strings.HasPrefix(upper, "BELAI_") ||
		strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_TOKEN") ||
		strings.HasSuffix(upper, "_SECRET")
}

func inList(key string, list []string) bool {
	for _, k := range list {
		if k == key {
			return true
		}
	}
	return false
}
