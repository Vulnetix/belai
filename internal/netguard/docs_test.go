package netguard

import (
	"net/netip"
	"strconv"
	"strings"
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

// shortForm renders a forbidden prefix the way docs/sanitization.md writes it:
// an IPv4 range keeps only the octets its mask needs (10.0.0.0/8 is 10/8), a
// host route in IPv6 drops its length (::1/128 is ::1).
func shortForm(p netip.Prefix) string {
	if p.Addr().Is4() {
		octets := strings.Split(p.Addr().String(), ".")
		keep := (p.Bits() + 7) / 8
		return strings.Join(octets[:keep], ".") + "/" + strconv.Itoa(p.Bits())
	}
	if p.Bits() == 128 {
		return strings.TrimSuffix(p.String(), "/128")
	}
	return p.String()
}

// TestEveryForbiddenRangeIsOnThePage derives each range from the table the
// dialer uses, so adding one without documenting it fails. Multicast, link-local
// and site-local are named in words on the page.
func TestEveryForbiddenRangeIsOnThePage(t *testing.T) {
	doc := docparity.Read(t, "docs/sanitization.md")
	byWord := map[string]string{"224.0.0.0/4": "multicast", "ff00::/8": "multicast", "fe80::/10": "link-local", "fec0::/10": "site-local"}
	for _, p := range forbidden {
		if word, ok := byWord[p.String()]; ok {
			if !strings.Contains(doc, word) {
				t.Errorf("docs/sanitization.md does not name %s (%s)", word, p)
			}
			continue
		}
		if !strings.Contains(doc, "`"+shortForm(p)+"`") && !strings.Contains(doc, "`"+shortForm(p)+" ") {
			t.Errorf("docs/sanitization.md does not list the forbidden range %s as `%s`", p, shortForm(p))
		}
	}
}

// TestInternalNamesAndLimitsMatchThePage pins the suffix list, the byte cap and
// the refusals the page states for a model-supplied URL.
func TestInternalNamesAndLimitsMatchThePage(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/sanitization.md")), " ")
	for _, suffix := range internalNames {
		if !strings.Contains(doc, "`*"+suffix+"`") {
			t.Errorf("docs/sanitization.md does not list the internal name `*%s`", suffix)
		}
	}
	if !strings.Contains(doc, "empty or over 8192 bytes") || MaxURLBytes != 8192 {
		t.Errorf("the page says 8192 bytes; MaxURLBytes is %d", MaxURLBytes)
	}
	// What the page says a Fetch URL may not name.
	for _, host := range []string{"localhost", "a.localhost", "printer.local", "db.internal", "wiki.intranet", "x.localdomain", "router.lan", "nas.home.arpa", "intranet"} {
		if _, err := CheckURL("https://"+host+"/", Fetch); err == nil {
			t.Errorf("Fetch accepted the internal name %q", host)
		}
	}
	// An Endpoint may name a private address; a Fetch URL may not.
	if _, err := CheckURL("https://10.1.2.3/", Endpoint); err != nil {
		t.Errorf("Endpoint refused a private address: %v", err)
	}
	if _, err := CheckURL("https://10.1.2.3/", Fetch); err == nil {
		t.Error("Fetch accepted a private address")
	}
	if _, err := CheckURL("http://example.com/", Endpoint); err == nil {
		t.Error("Endpoint accepted plain http to a public host")
	}
	if _, err := CheckURL("http://localhost:8080/", Endpoint); err != nil {
		t.Errorf("Endpoint refused http to loopback: %v", err)
	}
	// The numeric host forms the page lists.
	for _, host := range []string{"2130706433", "0x7f.1", "127.1", "0177.0.0.1"} {
		if _, err := CheckURL("https://"+host+"/", Endpoint); err == nil {
			t.Errorf("a non-canonical numeric host %q was accepted", host)
		}
	}
	// Percent escapes that decode to a control character at any depth to four.
	for _, tail := range []string{"%0d%0a", "%250d%250a", "%2500"} {
		if _, err := CheckURL("https://example.com/a"+tail, Fetch); err == nil {
			t.Errorf("an escape that decodes to a control character was accepted: %s", tail)
		}
	}
}
