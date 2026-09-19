package vulnetixcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAuthStatusUnauthenticated(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "auth-status-unauth.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	state := ParseAuthStatus(string(data))
	if !state.Parsed {
		t.Fatal("expected auth status to be parsed")
	}
	if state.Authenticated {
		t.Fatal("expected unauthenticated")
	}
	if state.Plan != PlanCommunity {
		t.Fatalf("plan = %q, want community", state.Plan)
	}
	if len(state.Sources) != 7 {
		t.Fatalf("sources = %d, want 7", len(state.Sources))
	}
	if state.Active != "" {
		t.Fatalf("active source should be empty when unauthenticated, got %q", state.Active)
	}
}

func TestParseAuthStatusAuthenticated(t *testing.T) {
	text := "AUTH STATE\n[OK] Authenticated\nPlan: [PRO]\nOrg ID: a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11\nCREDENTIAL SOURCES\n[OK] VULNETIX_API_TOKEN environment variable set\n"
	state := ParseAuthStatus(text)
	if !state.Authenticated {
		t.Fatal("expected authenticated")
	}
	if state.Plan != PlanPro {
		t.Fatalf("plan = %q, want pro", state.Plan)
	}
	if state.OrgID != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
		t.Fatalf("org id = %q", state.OrgID)
	}
	if state.Active != SourceEnvAPIToken {
		t.Fatalf("active source = %q, want env-api-token", state.Active)
	}
}

func TestParseAuthStatusFailsClosed(t *testing.T) {
	cases := []string{
		"garbage text with no sections",
		"SOME SECTION\nno auth marker here",
	}
	for _, tc := range cases {
		t.Run(strings.TrimSpace(tc), func(t *testing.T) {
			state := ParseAuthStatus(tc)
			if state.Authenticated {
				t.Fatal("expected fail-closed false")
			}
		})
	}
}
