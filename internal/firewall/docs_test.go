package firewall

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// TestFirewallPageListsEveryAdapterWithItsStatus keeps docs/firewall.md in step
// with the registry: each adapter has a table row naming its label and its
// stability, and a row that is not an adapter is a mistake.
func TestFirewallPageListsEveryAdapterWithItsStatus(t *testing.T) {
	doc := docparity.Read(t, "docs/firewall.md")
	status := map[Stability]string{Stable: "stable", Beta: "beta", Native: "read-only"}
	for _, a := range Adapters() {
		want := "| " + a.Label() + " | " + status[a.Stability()] + " |"
		if !strings.Contains(doc, want) {
			t.Errorf("docs/firewall.md has no row %q", want)
		}
	}
}

// TestFirewallPageNamesEveryMode keeps the three modes the page defines equal
// to the modes the adapters accept and the settings validate.
func TestFirewallPageNamesEveryMode(t *testing.T) {
	doc := docparity.Read(t, "docs/firewall.md")
	known := map[string]bool{
		config.FirewallModeTransparent: true, config.FirewallModeAuthorization: true, config.FirewallModeHeader: true,
	}
	for m := range known {
		if !strings.Contains(doc, "- **"+m+"**") {
			t.Errorf("docs/firewall.md does not define the mode %q", m)
		}
	}
	for _, a := range Adapters() {
		for _, m := range a.Modes() {
			if !known[m] {
				t.Errorf("the %s adapter accepts the mode %q, which the page does not document", a.ID(), m)
			}
		}
	}
}

// TestFirewallPageNamesTheVulnetixHeaders keeps the response headers the page
// says the Vulnetix adapter reads equal to the ones the code reads.
func TestFirewallPageNamesTheVulnetixHeaders(t *testing.T) {
	doc := docparity.Read(t, "docs/firewall.md")
	for _, h := range []string{
		"X-Vulnetix-Firewall-Decision", "-Rules", "-Redactions", "-Stripped", "-Request-Id", "-Nonce", "X-Vulnetix-Nonce-Mode: enforce",
	} {
		if !strings.Contains(doc, h) {
			t.Errorf("docs/firewall.md does not name the header %q", h)
		}
	}
	src := docparity.Read(t, "internal/firewall/vulnetix.go") + docparity.Read(t, "internal/firewall/verdict.go")
	for _, h := range []string{
		"X-Vulnetix-Firewall-Decision", "X-Vulnetix-Firewall-Rules", "X-Vulnetix-Firewall-Redactions",
		"X-Vulnetix-Firewall-Stripped", "X-Vulnetix-Firewall-Request-Id", "X-Vulnetix-Firewall-Nonce",
	} {
		if !strings.Contains(src, h) {
			t.Errorf("the page lists %s but the Vulnetix adapter no longer reads it", h)
		}
	}
}
