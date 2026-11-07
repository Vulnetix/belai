package sandbox

import (
	"context"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
)

// The policy rides on the context: WithPolicy stores it and FromContext
// returns it, absent meaning off. Nothing about a command is attached; the
// caller wraps the command itself.
func TestPolicyRidesOnTheContext(t *testing.T) {
	p := Policy{Mode: ModeRequired, DenyNetwork: true, Writable: []string{"/w"}}
	got := FromContext(WithPolicy(context.Background(), p))
	if got.Mode != p.Mode || got.DenyNetwork != p.DenyNetwork || len(got.Writable) != 1 || got.Writable[0] != "/w" {
		t.Fatalf("round trip = %+v", got)
	}
	if got := FromContext(context.Background()); got.Mode != "" {
		t.Fatalf("absent = %+v, want the zero policy", got)
	}
	if got := FromContext(WithPolicy(context.Background(), Policy{Mode: ModeOff})); got.Mode != ModeOff {
		t.Fatalf("an explicit off policy is kept, not read as absent: %+v", got)
	}
}

// Status is the one-word footer state: on when the mode asks for a sandbox
// and a backend works here, n/a when it is wanted but none works, off when
// the mode or the guardrails switch says so.
func TestStatusReflectsTheModeAndTheBackend(t *testing.T) {
	if got := Status(&config.SandboxSettings{Mode: ModeOff}, posture.Defaults()); got != "off" {
		t.Fatalf("off mode: %q", got)
	}
	if got := Status(nil, posture.AllIgnore()); got != "off" {
		t.Fatalf("guardrails off: %q", got)
	}
	name, _ := Backend()
	got := Status(&config.SandboxSettings{Mode: ModeRequired}, posture.Defaults())
	if got != "on" && got != "n/a" {
		t.Fatalf("required mode: %q", got)
	}
	if Status(nil, posture.Defaults()) != got {
		t.Fatalf("required and auto modes disagree here: %q vs %q", got, Status(nil, posture.Defaults()))
	}
	if (got == "on") != (name != "") {
		t.Fatalf("status %q but the backend is %q", got, name)
	}
}
