package tools

import (
	"context"
	"testing"
	"time"
)

// TestDetectLocalUtilitiesProbesPath pins local detection: present binaries
// appear, missing ones are absent.
func TestDetectLocalUtilitiesProbesPath(t *testing.T) {
	probe := CapabilityProbe{
		LookPath: func(name string) (string, bool) {
			switch name {
			case "cat", "head", "jq":
				return "/usr/bin/" + name, true
			}
			return "", false
		},
	}
	caps := Detect(context.Background(), probe, time.Second)
	if !caps.Has("Cat") || !caps.Has("Head") || !caps.Has("JQ") {
		t.Fatalf("expected Cat/Head/JQ detected, got %v", caps.LocalNames())
	}
	if caps.Has("YQ") || caps.Has("Git") {
		t.Fatalf("undetected tools must be absent: %v", caps.LocalNames())
	}
}

// TestDetectCloudRequiresAuthProbe pins the fail-closed cloud rule: an
// installed CLI whose auth probe fails is not offered, while one whose probe
// passes is.
func TestDetectCloudRequiresAuthProbe(t *testing.T) {
	probe := CapabilityProbe{
		LookPath: func(name string) (string, bool) {
			switch name {
			case "gh", "aws":
				return "/usr/bin/" + name, true
			}
			return "", false
		},
		Run: func(_ context.Context, name string, args ...string) bool {
			return name == "gh" // only gh is authenticated
		},
	}
	caps := Detect(context.Background(), probe, time.Second)
	if !caps.Has("GH") {
		t.Fatal("authenticated gh must be offered")
	}
	if caps.Has("AWS") {
		t.Fatal("installed-but-unauthenticated aws must be absent")
	}
	if caps.Has("Cat") {
		t.Fatal("Cat was not in the fake path, must be absent")
	}
}

// TestDetectMissingLookPathYieldsEmpty pins the seam: no LookPath means no
// detection and therefore no tools, never a panic.
func TestDetectMissingLookPathYieldsEmpty(t *testing.T) {
	caps := Detect(context.Background(), CapabilityProbe{}, time.Second)
	if !caps.IsEmpty() {
		t.Fatalf("expected empty capabilities, got %v", caps.LocalNames())
	}
}

// TestDetectPresenceOnlyCloud pins that a CLI with no auth probe is offered on
// presence alone.
func TestDetectPresenceOnlyCloud(t *testing.T) {
	probe := CapabilityProbe{
		LookPath: func(name string) (string, bool) {
			return "/usr/bin/" + name, name == "linode-cli"
		},
		Run: func(_ context.Context, _ string, _ ...string) bool { return false },
	}
	caps := Detect(context.Background(), probe, time.Second)
	if !caps.Has("LinodeCLI") {
		t.Fatal("presence-only CLI must be detected when installed")
	}
	if caps.Has("GH") {
		t.Fatal("gh is not in the fake path and must be absent")
	}
}

// TestCloudReadOnlyPrefixes pins the read-only subcommand gate for a
// representative CLI set.
func TestCloudReadOnlyPrefixes(t *testing.T) {
	byName := map[string]cloudSpec{}
	for _, cs := range cloudSpecs {
		byName[cs.name] = cs
	}
	cases := []struct {
		spec string
		cmd  string
		want bool
	}{
		{"GH", "pr list", true},
		{"GH", "pr merge", false},
		{"GH", "repo delete", false},
		{"AWS", "s3 ls", true},
		{"AWS", "s3 rm", false},
		{"Kubectl", "get pods", true},
		{"Kubectl", "delete pod", false},
		{"Terraform", "plan", true},
		{"Terraform", "apply", false},
	}
	for _, tc := range cases {
		spec := byName[tc.spec]
		if spec.name == "" {
			t.Fatalf("no cloud spec named %q", tc.spec)
		}
		if got := cloudAllowed(spec, tc.cmd); got != tc.want {
			t.Fatalf("cloudAllowed(%q, %q) = %v, want %v", spec.binary, tc.cmd, got, tc.want)
		}
	}
}

// TestCapabilitiesHasBinary pins the gate the repo-native tools use: the
// answer follows the binary of a *detected* local utility, not the mere
// presence of the tool name in the catalogue.
func TestCapabilitiesHasBinary(t *testing.T) {
	caps := Capabilities{local: map[string]bool{"Git": true, "Cat": true}}
	if !caps.HasBinary("git") {
		t.Fatal("git must be available through the detected Git tool")
	}
	if !caps.HasBinary("cat") {
		t.Fatal("cat must be available through the detected Cat tool")
	}
	// jq is in the catalogue but was not detected: its binary is not
	// available even though a tool named JQ exists.
	if caps.HasBinary("jq") {
		t.Fatal("an undetected tool's binary must report false")
	}
	if caps.HasBinary("repos") || caps.HasBinary("") {
		t.Fatal("unknown or empty binaries must report false")
	}
	empty := Capabilities{}
	if empty.HasBinary("git") {
		t.Fatal("an empty capability set must report nothing available")
	}
}
