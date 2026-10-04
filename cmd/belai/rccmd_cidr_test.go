package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// --allow-private-cidr is checked before remote control does anything, so a
// range that must never be allowed stops the command with a usage error.
func TestRCRefusesRangesThatCanNeverBeAllowed(t *testing.T) {
	for _, cidr := range []string{"127.0.0.0/8", "169.254.0.0/16", "fe80::/10", "0.0.0.0/0", "8.8.8.0/24", "fd00::1/64", "nonsense"} {
		var stdout, stderr bytes.Buffer
		code := runRCCLI(context.Background(), []string{"--allow-private-cidr", cidr}, &stdout, &stderr)

		if code != 2 {
			t.Errorf("--allow-private-cidr %s: exit %d, want 2", cidr, code)
		}
		if !strings.Contains(stderr.String(), "allow-private-cidr") {
			t.Errorf("--allow-private-cidr %s: stderr %q does not name the flag", cidr, stderr.String())
		}
	}
}

func TestRCUsageNamesTheAllowFlag(t *testing.T) {
	if !strings.Contains(rcUsage, "--allow-private-cidr") {
		t.Error("belai rc usage does not mention --allow-private-cidr")
	}
}
