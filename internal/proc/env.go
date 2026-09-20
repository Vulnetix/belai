package proc

import (
	"os"
	"strings"
)

// ScrubbedEnv returns a minimal environment with provider credentials and
// Signet config stripped so subprocess output cannot accidentally exfiltrate
// them to a model.
func ScrubbedEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "OPENAI_") || strings.HasPrefix(upper, "ANTHROPIC_") ||
			strings.HasPrefix(upper, "CLOUDFLARE_") || strings.HasPrefix(upper, "SIGNET_") ||
			strings.HasSuffix(upper, "_API_KEY") || strings.HasSuffix(upper, "_TOKEN") ||
			strings.HasSuffix(upper, "_SECRET") {
			continue
		}
		out = append(out, e)
	}
	return out
}
