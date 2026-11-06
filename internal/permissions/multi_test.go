package permissions

import "testing"

func subs(pairs ...string) []Subject {
	var out []Subject
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Subject{Tool: pairs[i], Path: pairs[i+1]})
	}
	return out
}

func TestExplainAllDenyOnAnyFileDenies(t *testing.T) {
	s := Settings{Allow: []string{"Edit(src/**)"}, Deny: []string{"Edit(go.mod)"}}
	d, rule := s.ExplainAll("apply_patch", subs("Edit", "src/a.go", "Edit", "go.mod"))
	if d != DecisionBlock || rule != "Edit(go.mod)" {
		t.Fatalf("got %q %q, want block by Edit(go.mod)", d, rule)
	}
}

func TestExplainAllDenyOnTheToolNameAlone(t *testing.T) {
	s := Settings{Deny: []string{"apply_patch(secrets/*)"}}
	d, _ := s.ExplainAll("apply_patch", subs("Edit", "ok.go", "Write", "secrets/key"))
	if d != DecisionBlock {
		t.Fatalf("a deny written for apply_patch did not block: %q", d)
	}
}

func TestExplainAllAllowMustCoverEveryFile(t *testing.T) {
	s := Settings{Allow: []string{"Edit(src/**)"}}
	d, rule := s.ExplainAll("apply_patch", subs("Edit", "src/a.go", "Edit", "src/b.go"))
	if d != DecisionAllow || rule != "Edit(src/**)" {
		t.Fatalf("all covered: %q %q", d, rule)
	}
	d, rule = s.ExplainAll("apply_patch", subs("Edit", "src/a.go", "Edit", "go.mod"))
	if rule != "" {
		t.Fatalf("a patch that also touches go.mod was approved by %q (decision %q)", rule, d)
	}
}

func TestExplainAllAskOnAnyFileAsks(t *testing.T) {
	s := Settings{Allow: []string{"Edit(src/**)"}, Ask: []string{"Edit(docs/*)"}}
	d, rule := s.ExplainAll("apply_patch", subs("Edit", "src/a.go", "Edit", "docs/x.md"))
	if d != DecisionAsk || rule != "Edit(docs/*)" {
		t.Fatalf("got %q %q", d, rule)
	}
}

func TestExplainAllAddIsAWrite(t *testing.T) {
	s := Settings{Allow: []string{"Edit(**)"}}
	if _, rule := s.ExplainAll("apply_patch", subs("Write", "new.go")); rule != "" {
		t.Fatalf("an Edit rule approved an added file: %q", rule)
	}
	s = Settings{Allow: []string{"Write(new.go)"}}
	if d, rule := s.ExplainAll("apply_patch", subs("Write", "new.go")); d != DecisionAllow || rule == "" {
		t.Fatalf("a Write rule did not approve an added file: %q %q", d, rule)
	}
}

func TestExplainAllAnApplyPatchAllowCoversEveryFile(t *testing.T) {
	s := Settings{Allow: []string{"apply_patch"}}
	d, rule := s.ExplainAll("apply_patch", subs("Edit", "a.go", "Write", "b.go"))
	if d != DecisionAllow || rule != "apply_patch" {
		t.Fatalf("got %q %q", d, rule)
	}
}

func TestExplainAllNoRuleIsTheDefault(t *testing.T) {
	d, rule := (Settings{}).ExplainAll("apply_patch", subs("Edit", "a.go"))
	if d != DecisionAllow || rule != "" {
		t.Fatalf("got %q %q: with no rule the caller must see an unmatched default", d, rule)
	}
	if d, rule := (Settings{}).ExplainAll("apply_patch", nil); d != DecisionAllow || rule != "" {
		t.Fatalf("no files: %q %q", d, rule)
	}
}

func TestExplainAllHarnessRuleBlocksUnlessPermitted(t *testing.T) {
	s := Settings{Harness: []string{"Edit(*.vulnetix/*)", "Write(*.vulnetix/*)"}}
	if d, _ := s.ExplainAll("apply_patch", subs("Edit", "a.go", "Write", ".vulnetix/x")); d != DecisionBlock {
		t.Fatalf("a harness rule did not block a file of the patch: %q", d)
	}
	s.Permit = []string{"Write(.vulnetix/x)"}
	if d, _ := s.ExplainAll("apply_patch", subs("Write", ".vulnetix/x")); d == DecisionBlock {
		t.Fatalf("a permit did not lift the harness rule")
	}
}
