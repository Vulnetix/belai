package rc

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
	"github.com/vulnetix/belai/internal/sessionsync"
)

func saveCrew(t *testing.T, id, name string, members ...string) {
	t.Helper()
	ms := make([]agentprofile.Member, len(members))
	for i, m := range members {
		ms[i] = agentprofile.Member{Profile: m, Replicas: 1}
	}
	if _, err := agentprofile.SaveCrew(agentprofile.Crew{ID: id, Name: name, Description: "d", Members: ms}); err != nil {
		t.Fatal(err)
	}
}

func uploadedNames(t *testing.T, h *fullHarness) []string {
	t.Helper()
	var out []string
	for _, b := range h.site.backups {
		md, _ := b["markdown"].(string)
		p, err := agentprofile.ParseMarkdown([]byte(md))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Name)
	}
	return out
}

func TestTeleportBackupUploadsTheProfileItsCrewsAndTheirMembers(t *testing.T) {
	h := newFullHarness(t, true)
	for _, p := range []agentprofile.AgentProfile{worker("log", libID), worker("tf", libID2)} {
		if _, err := agentprofile.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	other := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	saveCrew(t, crewID, "aws-infra", "log", "tf")
	saveCrew(t, other, "unrelated", "tf") // does not list the session's profile

	status, report := h.run(sessionsync.Dispatch{Kind: "teleport_backup", Teleport: "tp1", Profiles: []string{"log"}})
	if status != sessionsync.DispatchStarted {
		t.Fatalf("ack = %s %q", status, report)
	}
	got := strings.Join(uploadedNames(t, h), ",")
	if got != "log,tf" {
		t.Fatalf("profiles uploaded = %s", got)
	}
	if len(h.site.crewUps) != 1 {
		t.Fatalf("crews uploaded = %d, want only the one that lists the profile", len(h.site.crewUps))
	}
	// Every upload answers the request that asked for it.
	for _, b := range h.site.backups {
		if b["dispatch"] != "d-teleport_backup" {
			t.Fatalf("an upload named %v", b["dispatch"])
		}
	}
	if !strings.Contains(report, "aws-infra") {
		t.Fatalf("report = %q", report)
	}
}

func TestTeleportBackupRefusesWhatItCannotBackUp(t *testing.T) {
	h := newFullHarness(t, true)
	cases := map[string]sessionsync.Dispatch{
		"no profile":       {Kind: "teleport_backup"},
		"two profiles":     {Kind: "teleport_backup", Profiles: []string{"a", "b"}},
		"a built-in":       {Kind: "teleport_backup", Profiles: []string{"belai:triage-vulns"}},
		"a missing one":    {Kind: "teleport_backup", Profiles: []string{"ghost"}},
		"a name with line": {Kind: "teleport_backup", Profiles: []string{"a\nb"}},
	}
	for name, r := range cases {
		h.site.acks = map[string][3]string{}
		if status, why := h.run(r); status != sessionsync.DispatchRefused || why == "" {
			t.Errorf("%s: %s %q", name, status, why)
		}
	}
	if len(h.site.backups) != 0 {
		t.Fatalf("a refused request uploaded %d profiles", len(h.site.backups))
	}
}
