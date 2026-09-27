package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/firewall"
)

func TestFirewallLine(t *testing.T) {
	v := firewall.Verdict{Firewall: "Vulnetix AI Firewall", Action: firewall.ActionRedact, Rules: []string{"PII"}, Redactions: 2, RequestID: "r1"}
	got := firewallLine(v)
	for _, want := range []string{"firewall: Vulnetix AI Firewall redacted", "rules=PII", "redactions=2", "request=r1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line %q lacks %q", got, want)
		}
	}
	var buf bytes.Buffer
	cancel := watchFirewallHeadless(&buf)
	cancel()
	if buf.Len() != 0 {
		t.Fatal("observer wrote without an event")
	}
}
