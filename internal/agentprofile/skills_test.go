package agentprofile

import (
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/skills"
)

// Every builtin worker names exactly its own builtin skill, keeps the Skill
// tool, and the skill agrees with the profile about who it serves and which
// tools it expects.
func TestBuiltinWorkersCarryTheirSkill(t *testing.T) {
	byName := map[string]skills.Entry{}
	for _, e := range skills.Builtin() {
		byName[e.Name] = e
	}
	for _, name := range workerProfiles {
		p, err := Load(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := "belai-" + strings.TrimPrefix(name, "belai:")
		if len(p.Skills) != 1 || p.Skills[0] != want {
			t.Errorf("%s: skills = %v, want [%s]", name, p.Skills, want)
			continue
		}
		if !hasTool(p.Tools, "Skill") {
			t.Errorf("%s: tools must include Skill", name)
		}
		e, ok := byName[want]
		if !ok {
			t.Errorf("%s: no builtin skill %s", name, want)
			continue
		}
		if e.Metadata[skills.MetaRole] != name {
			t.Errorf("%s: skill role = %q", name, e.Metadata[skills.MetaRole])
		}
		for _, tool := range e.AllowedTools {
			if strings.HasPrefix(tool, "Bash(") {
				if !hasTool(p.Tools, "Bash") {
					t.Errorf("%s: skill expects %s but the profile has no Bash", name, tool)
				}
				continue
			}
			if !KnownTool(tool) {
				t.Errorf("%s: skill allowed-tools names unknown tool %q", name, tool)
				continue
			}
			// The harness adds the kanban tools and PublishBranch to a worker.
			if strings.HasPrefix(tool, "Kanban") || tool == "PublishBranch" {
				continue
			}
			if !hasTool(p.Tools, tool) && !mcpCovered(p.Tools, tool) {
				t.Errorf("%s: skill expects %s, which the profile's tools do not include", name, tool)
			}
		}
	}
}

func mcpCovered(tools []string, tool string) bool {
	for _, t := range tools {
		if strings.HasSuffix(t, "*") && strings.HasPrefix(tool, strings.TrimSuffix(t, "*")) {
			return true
		}
	}
	return false
}

func skillProfile() AgentProfile {
	return AgentProfile{Name: "a", Description: "d", SystemPrompt: "p", Mode: ModeSingle}
}

func TestSkillsFieldValidation(t *testing.T) {
	ok := skillProfile()
	ok.Skills = []string{"belai-scout", "release", "team:deploy"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid skills refused: %v", err)
	}
	for name, mut := range map[string]func(*AgentProfile){
		"too many":                   func(p *AgentProfile) { p.Skills = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} },
		"duplicate":                  func(p *AgentProfile) { p.Skills = []string{"a", "a"} },
		"bad characters":             func(p *AgentProfile) { p.Skills = []string{"../x"} },
		"upper case":                 func(p *AgentProfile) { p.Skills = []string{"Release"} },
		"invented belai skill":       func(p *AgentProfile) { p.Skills = []string{"belai-invented"} },
		"tools without Skill":        func(p *AgentProfile) { p.Skills = []string{"a"}; p.Tools = []string{"Read"} },
		"metadata key":               func(p *AgentProfile) { p.Metadata = map[string]string{"bad key": "x"} },
		"metadata multi-line value":  func(p *AgentProfile) { p.Metadata = map[string]string{"k": "a\nb"} },
		"metadata too many entries":  func(p *AgentProfile) { p.Metadata = manyKeys(33) },
		"metadata value is too long": func(p *AgentProfile) { p.Metadata = map[string]string{"k": strings.Repeat("v", 4097)} },
	} {
		p := skillProfile()
		mut(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	good := skillProfile()
	good.Tools = []string{"Read", "Skill"}
	good.Skills = []string{"a"}
	good.Metadata = map[string]string{"source": "import", "model.fallbacks": "x/y"}
	if err := good.Validate(); err != nil {
		t.Errorf("valid profile refused: %v", err)
	}
}

func manyKeys(n int) map[string]string {
	m := map[string]string{}
	for i := 0; i < n; i++ {
		m["k"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26))] = "v"
	}
	return m
}

func TestSkillsLineNamesOnlyTheSkills(t *testing.T) {
	p := skillProfile()
	if p.SkillsLine() != "" {
		t.Error("no skills, no line")
	}
	p.Skills = []string{"belai-scout"}
	if !strings.Contains(p.Persona(), "the skill `belai-scout`") {
		t.Errorf("persona = %q", p.Persona())
	}
}

// Metadata describes the profile; it never reaches the persona and does not
// restart a running worker.
func TestMetadataIsNotBehavioural(t *testing.T) {
	p := skillProfile()
	q := p
	q.Metadata = map[string]string{"source": "import"}
	if q.Behavioural().Metadata != nil {
		t.Error("Behavioural keeps metadata")
	}
	if strings.Contains(q.Persona(), "import") {
		t.Error("metadata reached the persona")
	}
}
