package sandbox

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSandboxPageListsTheCacheDirectories keeps the cache list in
// docs/sandbox.md equal to the directories the policy makes writable: every
// directory the page names is in the code, and every one in the code is named
// or covered by "a few more".
func TestSandboxPageListsTheCacheDirectories(t *testing.T) {
	doc := docparity.Read(t, "docs/sandbox.md")
	inCode := map[string]bool{}
	for _, d := range cacheDirs {
		inCode[d] = true
	}
	named := map[string]bool{}
	for _, m := range regexp.MustCompile("`~/([^`]+)`").FindAllStringSubmatch(doc, -1) {
		if strings.HasPrefix(m[1], ".ssh") || strings.HasPrefix(m[1], ".vulnetix") {
			continue
		}
		named[m[1]] = true
		if !inCode[m[1]] {
			t.Errorf("docs/sandbox.md names ~/%s as a cache, which the policy does not make writable", m[1])
		}
	}
	if len(named) < 14 {
		t.Fatalf("only %d cache directories found on the page", len(named))
	}
	// The remainder is what "a few more" stands for; keep it small.
	if more := len(cacheDirs) - len(named); more < 1 || more > 5 {
		t.Errorf("%d cache directories are unnamed, which is more than a few", more)
	}
	for _, env := range []string{"GOCACHE", "GOMODCACHE", "GOPATH", "XDG_CACHE_HOME", "CARGO_HOME", "npm_config_cache"} {
		if !strings.Contains(doc, "`"+env+"`") {
			t.Errorf("docs/sandbox.md does not name the variable %s", env)
		}
	}
}

// TestSandboxPageStatesTheSettingValues pins the modes, network values and
// defaults the page states.
func TestSandboxPageStatesTheSettingValues(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sandbox.md")), " ")
	for _, want := range []string{
		"| `mode` | `off`, `auto`, `required` | `auto` |",
		"| `network` | `allow`, `deny` | `allow` |",
		"| `caches` | `true`, `false` | `true` |",
		"may raise (`off` to `auto` to `required`), never lower",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/sandbox.md does not say %q", want)
		}
	}
	if ModeOff != "off" || ModeAuto != "auto" || ModeRequired != "required" || NetworkDeny != "deny" {
		t.Errorf("mode and network constants disagree with the page")
	}
}

// TestSandboxPageNamesBothLinuxBackends pins the backend table and what the
// page says about Landlock: its limits, the kernel it needs, the helper, and
// how to keep bubblewrap where only a switch is in the way.
func TestSandboxPageNamesBothLinuxBackends(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sandbox.md")), " ")
	for _, want := range []string{
		"| Linux | `bwrap` (bubblewrap) |",
		"| Linux without a working `bwrap` | `landlock` (kernel LSM, 5.13+) |",
		"ABI 4",
		"Linux 6.7",
		"kernel.apparmor_restrict_unprivileged_userns",
		"--security-opt seccomp=unconfined",
		"no private `/tmp`",
		"pid namespace",
		"`belai " + LandlockCommand + "`",
		"five second cooldown",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/sandbox.md does not say %q", want)
		}
	}
	if landlockNetABI != 4 {
		t.Errorf("the network ABI is %d, the page says 4", landlockNetABI)
	}
	if probeRetryAfter != 5*time.Second {
		t.Errorf("the cooldown is %v, the page says five seconds", probeRetryAfter)
	}
}
