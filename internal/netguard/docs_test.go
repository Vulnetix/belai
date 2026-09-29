package netguard

import (
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestSanitizationPageNamesEveryExportedFunction keeps docs/sanitization.md in
// step with the code.
func TestSanitizationPageNamesEveryExportedFunction(t *testing.T) {
	docparity.RequireMentions(t, "docs/sanitization.md", docparity.ExportedFuncs(t, "."))
}

// TestSanitizationPageListsEveryForbiddenRange keeps the documented ranges in
// step with the table the dialer uses.
func TestSanitizationPageListsEveryForbiddenRange(t *testing.T) {
	doc := docparity.Read(t, "docs/sanitization.md")
	for _, want := range []string{
		"100.64/10", "169.254/16", "172.16/12", "192.168/16", "198.18/15", "`240/4`", "`127/8`", "`10/8`",
		"::1", "64:ff9b::/96", "2001::/32", "2002::/16", "fc00::/7", "fd00:ec2::254",
	} {
		if !contains(doc, want) {
			t.Errorf("docs/sanitization.md does not mention the forbidden range %q", want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
