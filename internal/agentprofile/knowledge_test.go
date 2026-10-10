package agentprofile

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/knowledge"
)

func knowledgeProfile(paths ...string) AgentProfile {
	return AgentProfile{
		Name: "kb-agent", Description: "d", SystemPrompt: "s", Mode: ModeSingle,
		Knowledge: &KnowledgeSpec{Paths: paths},
	}
}

func TestKnowledgeLimitMatchesTheStore(t *testing.T) {
	if MaxKnowledgePaths != knowledge.MaxProfilePaths {
		t.Fatalf("agentprofile allows %d paths, the store %d", MaxKnowledgePaths, knowledge.MaxProfilePaths)
	}
}

func TestKnowledgeAcceptsAbsoluteAndHomePaths(t *testing.T) {
	if err := knowledgeProfile("/srv/docs", "~/handbook/policy.md", ".vulnetix", "docs/handbook").Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (AgentProfile{Name: "plain", Description: "d", SystemPrompt: "s", Mode: ModeSingle}).Validate(); err != nil {
		t.Fatalf("knowledge is optional: %v", err)
	}
}

func TestKnowledgeRefusesWhatItCannotRead(t *testing.T) {
	many := make([]string, MaxKnowledgePaths+1)
	for i := range many {
		many[i] = "/docs/" + strings.Repeat("a", i+1)
	}
	for name, p := range map[string]AgentProfile{
		"empty list":         knowledgeProfile(),
		"relative dot dot":   knowledgeProfile("docs/../../etc"),
		"leading dot dot":    knowledgeProfile("../outside"),
		"project root":       knowledgeProfile("."),
		"project root slash": knowledgeProfile("./"),
		"relative duplicate": knowledgeProfile(".vulnetix", "./.vulnetix/"),
		"dot dot":            knowledgeProfile("/srv/../etc"),
		"home dot dot":       knowledgeProfile("~/../x"),
		"blank":              knowledgeProfile("  "),
		"control character":  knowledgeProfile("/srv/a\nb"),
		"nul":                knowledgeProfile("/srv/a\x00b"),
		"too long":           knowledgeProfile("/" + strings.Repeat("a", 2000)),
		"duplicate":          knowledgeProfile("/srv/docs", "/srv/docs/"),
		"too many":           knowledgeProfile(many...),
		"tilde user":         knowledgeProfile("~root/x"),
	} {
		err := p.Validate()
		if err == nil || !strings.Contains(err.Error(), "knowledge.paths") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The security crew reads the review artifacts it works from through the
// documents it lists, relative to the project's trusted root. Where they are
// and what they hold is told to the agent by the harness from this list
// (fleet.knowledgeDirective), not by the instructions.
func TestSecurityCrewMembersListTheReviewArtifacts(t *testing.T) {
	crew, err := LoadCrew("belai:security")
	if err != nil {
		t.Fatal(err)
	}
	if len(crew.Members) != 3 {
		t.Fatalf("members = %+v", crew.Members)
	}
	for _, m := range crew.Members {
		p, err := Load(m.Profile)
		if err != nil {
			t.Fatal(err)
		}
		if got := p.KnowledgePaths(); len(got) != 1 || got[0] != ".vulnetix" {
			t.Errorf("%s lists %v, want [.vulnetix]", m.Profile, got)
		}
		if p.ID == "" {
			t.Errorf("%s has no id, so its index could not be keyed", m.Profile)
		}
	}
}

func TestKnowledgeSurvivesMarkdownAndIsBehavioural(t *testing.T) {
	p := knowledgeProfile("/srv/docs", "~/notes.md")
	md, err := MarshalMarkdown(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "\nknowledge: ") {
		t.Fatalf("front matter lacks knowledge:\n%s", md)
	}
	got, err := ParseMarkdown(md)
	if err != nil || got.Knowledge == nil || len(got.Knowledge.Paths) != 2 || got.Knowledge.Paths[1] != "~/notes.md" {
		t.Fatalf("round trip = %+v err=%v", got.Knowledge, err)
	}
	if b := p.Behavioural(); b.Knowledge == nil {
		t.Fatal("the documents a profile lists are behaviour: changing them must restart a worker")
	}
	if got := p.KnowledgePaths(); len(got) != 2 {
		t.Fatalf("paths = %v", got)
	}
	if (AgentProfile{}).KnowledgePaths() != nil {
		t.Fatal("no knowledge, no paths")
	}
}

func TestKnowledgeRejectsUnknownKeysInsideTheBlock(t *testing.T) {
	_, err := ParseMarkdown([]byte("---\nname: x\ndescription: d\nmode: single\nknowledge: {\"paths\": [\"/srv\"], \"command\": \"cat\"}\n---\nbody\n"))
	if err == nil {
		t.Fatal("strict front matter must reject an unknown key under knowledge")
	}
}
