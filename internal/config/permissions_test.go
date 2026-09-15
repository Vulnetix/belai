package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPermissionRulesIsZero(t *testing.T) {
	if !(PermissionRules{}).IsZero() {
		t.Fatal("empty rules should be zero")
	}
	if (PermissionRules{Allow: []string{"Read"}}).IsZero() {
		t.Fatal("non-empty allow should not be zero")
	}
	if (PermissionRules{Ask: []string{"Bash"}}).IsZero() {
		t.Fatal("non-empty ask should not be zero")
	}
	if (PermissionRules{Deny: []string{"Write"}}).IsZero() {
		t.Fatal("non-empty deny should not be zero")
	}
}

func TestPermissionRulesMerge(t *testing.T) {
	a := PermissionRules{Allow: []string{"A"}, Deny: []string{"D1"}}
	b := PermissionRules{Allow: []string{"B"}, Deny: []string{"D2"}}
	got := a.Merge(b)
	want := PermissionRules{
		Allow: []string{"A", "B"},
		Deny:  []string{"D1", "D2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge mismatch:\n want=%+v\n  got=%+v", want, got)
	}

	// Duplicate suppression.
	c := PermissionRules{Allow: []string{"A", "A"}}
	got2 := a.Merge(c)
	want2 := PermissionRules{Allow: []string{"A"}, Deny: []string{"D1"}}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("dedup merge mismatch:\n want=%+v\n  got=%+v", want2, got2)
	}
}

func TestPermissionRulesMarshalEmpty(t *testing.T) {
	data, err := json.Marshal(PermissionRules{})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Fatalf("expected empty object, got %s", string(data))
	}
}

func TestPermissionRulesMarshalStruct(t *testing.T) {
	p := PermissionRules{Allow: []string{"Read"}, Deny: []string{"Write"}}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m["allow"], []any{"Read"}) {
		t.Fatalf("bad allow: %v", m["allow"])
	}
	if !reflect.DeepEqual(m["deny"], []any{"Write"}) {
		t.Fatalf("bad deny: %v", m["deny"])
	}
}

func TestPermissionRulesUnmarshalStructured(t *testing.T) {
	input := `{"allow":["Read"],"ask":["Bash"],"deny":["Write"],"block":["Delete"]}`
	var p PermissionRules
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		t.Fatal(err)
	}
	want := PermissionRules{
		Allow: []string{"Read"},
		Ask:   []string{"Bash"},
		Deny:  []string{"Write", "Delete"},
	}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("unmarshal mismatch:\n want=%+v\n  got=%+v", want, p)
	}
}

func TestPermissionRulesUnmarshalLegacy(t *testing.T) {
	input := `{"read":"allow","bash":"ask","write":"block","delete":"deny"}`
	var p PermissionRules
	if err := json.Unmarshal([]byte(input), &p); err != nil {
		t.Fatal(err)
	}
	// Legacy map order is non-deterministic; check contents rather than order.
	if len(p.Allow) != 1 || p.Allow[0] != "read" {
		t.Fatalf("allow mismatch: %v", p.Allow)
	}
	if len(p.Ask) != 1 || p.Ask[0] != "bash" {
		t.Fatalf("ask mismatch: %v", p.Ask)
	}
	if len(p.Deny) != 2 {
		t.Fatalf("deny length mismatch: %v", p.Deny)
	}
	denySet := map[string]bool{}
	for _, d := range p.Deny {
		denySet[d] = true
	}
	if !denySet["write"] || !denySet["delete"] {
		t.Fatalf("deny contents mismatch: %v", p.Deny)
	}
}

func TestPermissionRulesUnmarshalMixedError(t *testing.T) {
	input := `{"read":"allow","bash":["ask"]}`
	var p PermissionRules
	if err := json.Unmarshal([]byte(input), &p); err == nil {
		t.Fatal("expected error for mixed legacy and structured")
	}
}

func TestPermissionRulesUnmarshalUnknownBucket(t *testing.T) {
	input := `{"allow":["Read"],"unknown":["X"]}`
	var p PermissionRules
	if err := json.Unmarshal([]byte(input), &p); err == nil {
		t.Fatal("expected error for unknown bucket")
	}
}

func TestPermissionRulesRoundTrip(t *testing.T) {
	p := PermissionRules{
		Allow: []string{"Read", "Bash"},
		Deny:  []string{"Write(*)", "Delete"},
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var q PermissionRules
	if err := json.Unmarshal(data, &q); err != nil {
		t.Fatal(err)
	}
	// Marshal sorts slices, so unmarshal result is sorted.
	want := PermissionRules{
		Allow: []string{"Bash", "Read"},
		Deny:  []string{"Delete", "Write(*)"},
	}
	if !reflect.DeepEqual(want, q) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n  got=%+v", want, q)
	}
}
