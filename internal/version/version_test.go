package version

import (
	"strings"
	"testing"
)

func TestUserAgent(t *testing.T) {
	ua := UserAgent()
	if !strings.Contains(ua, "signet/") {
		t.Fatalf("UserAgent = %q", ua)
	}
	if !strings.Contains(ua, "github.com/Vulnetix/signet") {
		t.Fatalf("UserAgent = %q", ua)
	}
}
