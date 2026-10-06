package vulnetixcli

import (
	"slices"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestVulnetixPageStatesTheCLIEnvironment checks the paragraph under the
// command table of docs/vulnetix.md against probeEnv, with real variables set.
func TestVulnetixPageStatesTheCLIEnvironment(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/vulnetix.md")), " ")
	for _, want := range []string{
		"whether the API is reachable at `$VULNETIX_API_URL` (default `https://api.vdb.vulnetix.com/v1`)",
		"Variables starting `OPENAI_`, `ANTHROPIC_`, `CLOUDFLARE_` or `BELAI_`, and any ending `_API_KEY`, `_TOKEN` or `_SECRET`, are removed",
		"`NO_COLOR=1`, `TERM=dumb` and `HOMEBREW_NO_AUTO_UPDATE=1` are set",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/vulnetix.md does not say %q", want)
		}
	}
	kept := []string{"VULNETIX_API_TOKEN", "VULNETIX_API_KEY", "VULNETIX_ORG_ID", "VULNETIX_API_URL", "VULNETIX_WEB_URL", "VVD_ORG", "VVD_SECRET"}
	removed := []string{"OPENAI_BASE", "ANTHROPIC_VERSION", "CLOUDFLARE_ZONE", "BELAI_HOME", "GITHUB_TOKEN", "STRIPE_API_KEY", "DB_SECRET"}
	passed := []string{"PATH_EXTRA_FOR_TEST", "HOME_FOR_TEST"}
	for _, k := range append(append(slices.Clone(kept), removed...), passed...) {
		t.Setenv(k, "v")
	}
	env := probeEnv()
	has := func(name string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, name+"=") {
				return true
			}
		}
		return false
	}
	for _, k := range kept {
		if !has(k) {
			t.Errorf("%s was removed, but the page says it is kept", k)
		}
		if !strings.Contains(doc, "`"+k+"`") {
			t.Errorf("the page does not name the kept variable %s", k)
		}
	}
	for _, k := range removed {
		if has(k) {
			t.Errorf("%s was kept, but the page says it is removed", k)
		}
	}
	for _, k := range passed {
		if !has(k) {
			t.Errorf("%s was removed; the page says everything else passes through", k)
		}
	}
	for _, want := range []string{"HOMEBREW_NO_AUTO_UPDATE=1", "NO_COLOR=1", "TERM=dumb"} {
		if !slices.Contains(env, want) {
			t.Errorf("the environment lacks %s", want)
		}
	}
}
