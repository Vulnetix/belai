package kiroauth

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestKiroPageStatesTheSignInAndRefreshRules pins the numbers and regions
// docs/kiro.md gives for the device sign-in, the token refresh and the profile
// lookup to the constants the code uses.
func TestKiroPageStatesTheSignInAndRefreshRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/kiro.md")), " ")
	for _, want := range []string{
		"The poll asks every 5 seconds",
		"waits 5 seconds longer each time AWS says to slow down",
		"after 10 minutes when AWS gave none",
		"until two minutes before it expires",
		"default is `us-east-1`",
		"then `us-east-1` and `eu-central-1`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/kiro.md does not say %q", want)
		}
	}
	if devicePollInterval != 5*time.Second || deviceSlowDown != 5*time.Second || devicePollTimeout != 10*time.Minute {
		t.Errorf("poll constants %v/%v/%v disagree with the page", devicePollInterval, deviceSlowDown, devicePollTimeout)
	}
	if refreshMargin != 2*time.Minute {
		t.Errorf("refreshMargin = %v, the page says two minutes", refreshMargin)
	}
	if DefaultRegion != "us-east-1" || strings.Join(ProfileRegions, ",") != "us-east-1,eu-central-1" {
		t.Errorf("regions %q %v disagree with the page", DefaultRegion, ProfileRegions)
	}
}
