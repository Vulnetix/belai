package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/permissions"
)

func TestDecodeLegacyFlatMap(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"edit":"allow","bash":"ask","rm":"block"}}`), &s); err != nil {
		t.Fatalf("decode legacy: %v", err)
	}
	if !reflect.DeepEqual(s.Permissions.Allow, []string{"edit"}) {
		t.Fatalf("Allow = %v", s.Permissions.Allow)
	}
	if !reflect.DeepEqual(s.Permissions.Ask, []string{"bash"}) {
		t.Fatalf("Ask = %v", s.Permissions.Ask)
	}
	if !reflect.DeepEqual(s.Permissions.Deny, []string{"rm"}) {
		t.Fatalf("Deny = %v", s.Permissions.Deny)
	}
}

func TestDecodeStructuredForm(t *testing.T) {
	var s Settings
	raw := `{"permissions":{"allow":["Read","Bash(git diff:*)"],"deny":["Write(*)"],"block":["Rm(*)"]}}`
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
	if !reflect.DeepEqual(s.Permissions.Allow, []string{"Read", "Bash(git diff:*)"}) {
		t.Fatalf("Allow = %v", s.Permissions.Allow)
	}
	if !reflect.DeepEqual(s.Permissions.Deny, []string{"Write(*)", "Rm(*)"}) {
		t.Fatalf("Deny = %v", s.Permissions.Deny)
	}
}

func TestDecodeMixedFormsError(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"allow":["Read"],"bash":"ask"}}`), &s); err == nil {
		t.Fatalf("mixed forms must error")
	}
}

func TestDecodeUnknownBucketError(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"frobnicate":["Read"]}}`), &s); err == nil {
		t.Fatalf("unknown bucket must error")
	}
}

// A rule value that is neither an array nor a string is neither form, so it
// fails the decode loudly instead of being silently dropped — a permissions
// block that half-loaded would enable or block tools the user never chose.
func TestDecodeUnsupportedRuleValueError(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"Bash":3}}`), &s); err == nil {
		t.Fatalf("an unsupported rule value must error")
	}
}

func TestMarshalSelfUpgrades(t *testing.T) {
	var s Settings
	if err := json.Unmarshal([]byte(`{"permissions":{"bash":"ask","rm":"block"}}`), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	perms := raw["permissions"].(map[string]any)
	if _, ok := perms["block"]; ok {
		t.Fatalf("block must not round-trip out: %v", perms)
	}
	if _, ok := perms["deny"]; !ok {
		t.Fatalf("expected structured deny bucket: %v", perms)
	}
	if _, ok := perms["ask"]; !ok {
		t.Fatalf("expected structured ask bucket: %v", perms)
	}
}

func TestOverrideCannotUnDeny(t *testing.T) {
	global := Settings{Permissions: PermissionRules{Deny: []string{"Bash(git push*)"}}}
	proj := Settings{Permissions: PermissionRules{Allow: []string{"Bash(*)"}}}
	merged := global.Override(proj)

	engine := permissions.From(merged.Permissions.Allow, merged.Permissions.Ask, merged.Permissions.Deny)
	if got := engine.Evaluate("Bash", "git push origin"); got != permissions.DecisionBlock {
		t.Fatalf("project allow must not un-deny a global deny, got %q", got)
	}
	if got := engine.Evaluate("Bash", "git diff"); got != permissions.DecisionAllow {
		t.Fatalf("project allow should still allow non-denied subjects, got %q", got)
	}
}

func TestOverrideDoesNotMutateReceiver(t *testing.T) {
	s := Settings{Permissions: PermissionRules{Allow: []string{"Read"}, Deny: []string{"Write"}}}
	before := Settings{Permissions: PermissionRules{Allow: append([]string{}, s.Permissions.Allow...), Deny: append([]string{}, s.Permissions.Deny...)}}
	_ = s.Override(Settings{Permissions: PermissionRules{Allow: []string{"Bash"}}})
	if !reflect.DeepEqual(s.Permissions, before.Permissions) {
		t.Fatalf("Override mutated receiver: %+v -> %+v", before.Permissions, s.Permissions)
	}
}

func TestPermissionRulesMergeUnion(t *testing.T) {
	a := PermissionRules{Allow: []string{"Read"}, Deny: []string{"Write"}}
	b := PermissionRules{Allow: []string{"Read", "Bash"}, Ask: []string{"WebFetch"}}
	got := a.Merge(b)
	if !reflect.DeepEqual(got.Allow, []string{"Read", "Bash"}) {
		t.Fatalf("Allow = %v (dedup + union)", got.Allow)
	}
	if !reflect.DeepEqual(got.Ask, []string{"WebFetch"}) {
		t.Fatalf("Ask = %v", got.Ask)
	}
	if !reflect.DeepEqual(got.Deny, []string{"Write"}) {
		t.Fatalf("Deny = %v", got.Deny)
	}
}

func TestPermissionRulesIsZero(t *testing.T) {
	var p PermissionRules
	if !p.IsZero() {
		t.Fatalf("zero PermissionRules should be IsZero")
	}
	if (PermissionRules{Allow: []string{"Read"}}).IsZero() {
		t.Fatalf("non-empty should not be IsZero")
	}
	if strings.Contains(string(mustMarshal(t, p)), "null") {
		t.Fatalf("empty rules should marshal as {}, got %s", string(mustMarshal(t, p)))
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
