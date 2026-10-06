package offload

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestContextOffloadPageStatesTheStoreLimits pins the numbers
// docs/context-offload.md gives for the store and the slices to the constants.
func TestContextOffloadPageStatesTheStoreLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/context-offload.md")), " ")
	var share float64 = headShare
	head := int(share*100 + 0.5)
	for _, want := range []string{
		fmt.Sprintf("estimated at %s characters per token", map[int]string{4: "four"}[CharsPerToken]),
		fmt.Sprintf("%d%% from the head and %d%% from the tail", head, 100-head),
		fmt.Sprintf("capped at %d MiB per session (%d MiB per result", MaxStoreBytes>>20, maxPutBytes>>20),
		fmt.Sprintf("each line at %s characters", withComma(maxSliceLine)),
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/context-offload.md does not say %q", want)
		}
	}
}

func withComma(n int) string {
	s := fmt.Sprint(n)
	if len(s) > 3 {
		return s[:len(s)-3] + "," + s[len(s)-3:]
	}
	return s
}
