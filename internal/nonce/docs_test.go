package nonce

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestNonceSpecStatesTheCacheAndPoolNumbers pins the numbers
// docs/nonce-endpoint-spec.md gives to the constants the pool and the negative
// cache use.
func TestNonceSpecStatesTheCacheAndPoolNumbers(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/nonce-endpoint-spec.md")), " ")
	for _, want := range []string{
		fmt.Sprintf("refills from the same endpoint, %d at a time", refillBatch),
		fmt.Sprintf("caches that verdict for **%d days**", int(UnsupportedTTL.Hours()/24)),
		fmt.Sprintf("is cached for **%d minutes**", int(TransientTTL.Minutes())),
		"the 3-second timeout",
		"`~/.vulnetix/belai/cache/nonce-unsupported.json`",
		"returns `401` (or `400`/`403`/`404`/`405`)",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/nonce-endpoint-spec.md does not say %q", want)
		}
	}
}

// TestNonceSpecDescribesCanonicalForwarding pins the canonical-mode rules the
// spec gives to the names the pool uses: the advertised modes field, the
// mode value ModeFor returns, and the forwarded form of a verified block.
func TestNonceSpecDescribesCanonicalForwarding(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/nonce-endpoint-spec.md")), " ")
	for _, want := range []string{
		"`modes`",
		"`X-Vulnetix-Nonce-Mode: canonical`",
		`<kind integrity="…">…</kind>`,
		"reads the header as observe",
		"`;canonical=N`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/nonce-endpoint-spec.md does not say %q", want)
		}
	}
}
