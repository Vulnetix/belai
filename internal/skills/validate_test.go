package skills

import (
	"reflect"
	"testing"
)

const validDoc = `---
name: code-reviewer
description: Reviews code for security issues
license: Apache-2.0
compatibility: belai>=0.1
metadata: {"icon":"shield"}
allowed-tools: [read, bash]
disable-model-invocation: true
---
body`

func TestValidateSkillValid(t *testing.T) {
	m, err := ValidateSkill(validDoc)
	if err != nil {
		t.Fatalf("ValidateSkill: %v", err)
	}
	if m.Name != "code-reviewer" {
		t.Fatalf("Name = %q", m.Name)
	}
	if m.Description != "Reviews code for security issues" {
		t.Fatalf("Description = %q", m.Description)
	}
	if m.License != "Apache-2.0" {
		t.Fatalf("License = %q", m.License)
	}
	if !reflect.DeepEqual(m.AllowedTools, []string{"read", "bash"}) {
		t.Fatalf("AllowedTools = %v", m.AllowedTools)
	}
	if !m.DisableModelInvocation {
		t.Fatalf("DisableModelInvocation = false, want true")
	}
}

func TestValidateSkillMissingName(t *testing.T) {
	doc := `---
description: no name here
---
body`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected missing name to be rejected")
	}
}

func TestValidateSkillMissingDescription(t *testing.T) {
	doc := `---
name: no-desc
---
body`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected missing description to be rejected")
	}
}

func TestValidateSkillUnknownField(t *testing.T) {
	doc := `---
name: x
description: y
version: 1.2.3
---
body`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected unknown field to be rejected")
	}
}

func TestValidateSkillBadBool(t *testing.T) {
	doc := `---
name: x
description: y
disable-model-invocation: yes
---
body`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected bad bool to be rejected")
	}
}

func TestValidateSkillBadList(t *testing.T) {
	doc := `---
name: x
description: y
allowed-tools: read
---
body`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected bad list to be rejected")
	}
}

func TestValidateSkillMissingDelimiter(t *testing.T) {
	doc := `name: x
description: y`
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected missing delimiter to be rejected")
	}
}

func TestValidateSkillUnterminated(t *testing.T) {
	doc := "---\nname: x\ndescription: y"
	if _, err := ValidateSkill(doc); err == nil {
		t.Fatalf("expected unterminated front-matter to be rejected")
	}
}
