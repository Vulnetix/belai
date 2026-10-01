package mcp

import (
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/docparity"
)

// TestMCPPageStatesTheLimits pins the timeouts, name rules and size caps
// docs/mcp.md states to the constants the client uses.
func TestMCPPageStatesTheLimits(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/mcp.md")), " ")
	for _, want := range []string{
		"Connecting (the handshake and the tool listing together) gets 30 seconds",
		"per-call timeout, default 60000, at most 600000",
		"Server names are letters, digits, `_` and `-`, at most 32",
		"capped at 1024 characters",
		"a result is capped at 64 KiB",
		"restricted to letters, digits, `_` and `-` and 64 characters",
		"response is read up to 16 MiB",
		"still running two seconds after Belai closes its input",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/mcp.md does not say %q", want)
		}
	}
	if connectTimeout != 30*time.Second || defaultCallTimeout != 60*time.Second || maxCallTimeout != 600*time.Second {
		t.Errorf("timeouts %v/%v/%v disagree with the page", connectTimeout, defaultCallTimeout, maxCallTimeout)
	}
	if maxDescRunes != 1024 || maxResultBytes != 64*1024 || maxHTTPBody != 16<<20 {
		t.Errorf("caps %d/%d/%d disagree with the page", maxDescRunes, maxResultBytes, maxHTTPBody)
	}
	if !serverNameRE.MatchString(strings.Repeat("a", 32)) || serverNameRE.MatchString(strings.Repeat("a", 33)) || serverNameRE.MatchString("a b") {
		t.Error("the server name rule is not 1 to 32 of letters, digits, _ and -")
	}
}
