package tui

import (
	"os"
	"strings"
	"testing"
)

// noHostCredentials blanks every credential-shaped environment variable for
// the test, so an App built from the environment reads as unconfigured
// whatever the developer's shell exports. CI has none of these set, which is
// why a test that assumes "no credentials" only fails on a dev machine.
func noHostCredentials(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, suffix := range []string{"_API_KEY", "_TOKEN", "_KEY", "_SECRET"} {
			if strings.HasSuffix(name, suffix) {
				t.Setenv(name, "")
				break
			}
		}
	}
	t.Setenv("BELAI_HOME", t.TempDir())
}
