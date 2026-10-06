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
