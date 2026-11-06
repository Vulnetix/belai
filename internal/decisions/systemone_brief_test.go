package decisions

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestTransportErrorKeepsCauseBeforeClip(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://api.cloudflare.com/client/v4/accounts/14fb351ce4ada375a5c8fa8ad9503fe8/ai/run/@cf/cloudflare/clef-flash", Err: errors.New("read: connection reset by peer")}
	got := transportError(err).Error()
	if !strings.Contains(got, "connection reset by peer") || !strings.Contains(got, "api.cloudflare.com") {
		t.Fatalf("cause or host lost: %q", got)
	}
	if strings.Contains(got, "/client/v4/accounts") {
		t.Fatalf("request path should be dropped: %q", got)
	}
	if len([]rune(got)) > 120 {
		t.Fatalf("message too long to survive the placeholder clip: %q", got)
	}
}
