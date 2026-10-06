package rc

import (
	"testing"

	"github.com/vulnetix/belai/internal/agentprofile"
)

// Golden vectors for the hash a host reports for a profile and a crew. The
// website compares it with the sha256 of a library version, and vdb-site hashes
// the markdown it is given and the crew in its own canonical JSON. The same bytes
// and the same digests are written, as literals, in vdb-site's
// belai_golden_hash_test.go (TestBelaiGoldenLibraryHashes and TestBelaiGoldenCrewCanonicalFormMatchesBelai): change how either
// side renders or hashes and one of the two tests fails, instead of every agent
// quietly reading as edited on the host. If you change a vector here, change it
// there, byte for byte.
//
// The values are the real output of the renderers, written out. Go escapes the
// angle brackets and the ampersand inside a string, which JavaScript's
// JSON.stringify does not, so the description below carries them on purpose.
const (
	goldenAgentMarkdown = "---\n" +
		"name: \"triage\"\n" +
		"id: \"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa\"\n" +
		"description: \"Sorts \\u003cnew\\u003e issues \\u0026 labels them\"\n" +
		"tools: [\"Read\",\"Grep\"]\n" +
		"mode: \"worker\"\n" +
		"autonomy: \"supervised\"\n" +
		"kanban: {\"lists\":[\"backlog\"],\"assigned_only\":true,\"on_success\":{\"list\":\"review\"},\"on_failure\":{\"list\":\"backlog\"}}\n" +
		"---\n" +
		"You triage.\n"
	goldenAgentSHA256 = "037f2f23ccd67b10aad33e79cc6c8e6ad4c206e819b17986dbf65ad74ace1667"

	goldenCrewJSON = "{\n" +
		"  \"id\": \"cccccccc-cccc-4ccc-8ccc-cccccccccccc\",\n" +
		"  \"name\": \"infra\",\n" +
		"  \"description\": \"Runs \\u003cinfra\\u003e \\u0026 checks\",\n" +
		"  \"members\": [\n" +
		"    {\n" +
		"      \"profile\": \"triage\",\n" +
		"      \"replicas\": 2\n" +
		"    }\n" +
		"  ]\n" +
		"}\n"
	goldenCrewSHA256 = "3d960ca152c1707f61656726ac2d40e09ffdb3f225e06505e61d3d2861cfc519"
)

func goldenProfile() agentprofile.AgentProfile {
	return agentprofile.AgentProfile{
		Name: "triage", ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Description: "Sorts <new> issues & labels them",
		SystemPrompt: "You triage.", Mode: agentprofile.ModeWorker, Autonomy: agentprofile.AutonomySupervised, Tools: []string{"Read", "Grep"},
		Kanban: &agentprofile.KanbanSpec{Lists: []string{"backlog"}, AssignedOnly: true, OnSuccess: agentprofile.Route{List: "review"}, OnFailure: agentprofile.Route{List: "backlog"}},
	}
}

func goldenCrew() agentprofile.Crew {
	return agentprofile.Crew{
		ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Name: "infra", Description: "Runs <infra> & checks",
		Members: []agentprofile.Member{{Profile: "triage", Replicas: 2}},
	}
}

// The profile renders to exactly the golden markdown, which hashes to the golden digest.
func TestGoldenProfileRenderAndHash(t *testing.T) {
	md, ok := profileDocument(func() agentprofile.AgentProfile { p := goldenProfile(); p.File = "triage.md"; return p }())
	if !ok {
		t.Fatal("the golden profile is not synced")
	}
	if string(md) != goldenAgentMarkdown {
		t.Fatalf("the render changed; vdb-site's golden vector must change with it.\ngot:\n%s\nwant:\n%s", md, goldenAgentMarkdown)
	}
	if got := hashOf(md); got != goldenAgentSHA256 {
		t.Fatalf("hash = %s, want %s", got, goldenAgentSHA256)
	}
	if got := hashOf([]byte(goldenAgentMarkdown)); got != goldenAgentSHA256 {
		t.Fatalf("the golden markdown hashes to %s, not the golden digest %s", got, goldenAgentSHA256)
	}
}

// The crew renders to exactly the golden canonical JSON, which hashes to the golden digest.
func TestGoldenCrewRenderAndHash(t *testing.T) {
	js, ok := crewDocument(goldenCrew())
	if !ok {
		t.Fatal("the golden crew is not synced")
	}
	if string(js) != goldenCrewJSON {
		t.Fatalf("the canonical crew changed; vdb-site's golden vector must change with it.\ngot:\n%s\nwant:\n%s", js, goldenCrewJSON)
	}
	if got := hashOf(js); got != goldenCrewSHA256 {
		t.Fatalf("hash = %s, want %s", got, goldenCrewSHA256)
	}
	if got := hashOf([]byte(goldenCrewJSON)); got != goldenCrewSHA256 {
		t.Fatalf("the golden crew hashes to %s, not the golden digest %s", got, goldenCrewSHA256)
	}
}
