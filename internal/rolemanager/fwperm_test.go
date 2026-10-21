package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/decisions"
)

type fakePermDecider struct {
	noul float64
	err  error
	n    int
}

func (f *fakePermDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	f.n++
	if f.err != nil {
		return decisions.Result{}, f.err
	}
	return decisions.Result{Answers: map[string]decisions.Answer{"perm": {Type: decisions.TypeNoul, Noul: f.noul}}}, nil
}
func (f *fakePermDecider) Identity() string           { return "systemone/test" }
func (f *fakePermDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

type fakePermClassifier struct {
	reply string
	got   []ClassifierPayload
}

func (f *fakePermClassifier) Classify(_ context.Context, p ClassifierPayload) (string, error) {
	f.got = append(f.got, p)
	return f.reply, nil
}

func TestDetectFirewallPermissionWordGate(t *testing.T) {
	d := &fakePermDecider{noul: 0.99}
	c := &fakePermClassifier{reply: "PERMISSIONS"}
	ok, _, err := DetectFirewallPermission(context.Background(), d, c, "add a test for the parser")
	if ok || err != nil || d.n != 0 || len(c.got) != 0 {
		t.Fatalf("a prompt with no permission words asked a model: ok=%v err=%v decider=%d classifier=%d", ok, err, d.n, len(c.got))
	}
}

func TestDetectFirewallPermissionSystemOneDecides(t *testing.T) {
	c := &fakePermClassifier{reply: "NONE"}
	ok, backend, err := DetectFirewallPermission(context.Background(), &fakePermDecider{noul: 0.97}, c, "never use Bash in this repo")
	if !ok || backend != "systemone" || err != nil || len(c.got) != 0 {
		t.Fatalf("ok=%v backend=%q err=%v classifier calls=%d", ok, backend, err, len(c.got))
	}
	ok, backend, _ = DetectFirewallPermission(context.Background(), &fakePermDecider{noul: 0.01}, c, "never use Bash in this repo")
	if ok || backend != "systemone" || len(c.got) != 0 {
		t.Fatalf("a clear no: ok=%v backend=%q", ok, backend)
	}
}

func TestDetectFirewallPermissionFallsBackToFast(t *testing.T) {
	for name, d := range map[string]decisions.Decider{
		"no backend":   nil,
		"inconclusive": &fakePermDecider{noul: 0.5},
		"unavailable":  &fakePermDecider{err: errors.New("503")},
	} {
		c := &fakePermClassifier{reply: "PERMISSIONS"}
		ok, backend, err := DetectFirewallPermission(context.Background(), d, c, "don't send customer emails to the model")
		if !ok || backend != "fast" || err != nil {
			t.Errorf("%s: ok=%v backend=%q err=%v", name, ok, backend, err)
			continue
		}
		if len(c.got) != 1 || c.got[0].UseCase != UseCaseFirewallPermDetect {
			t.Errorf("%s: payload %+v", name, c.got)
		}
	}
}

func TestParseFirewallPermissionsValidates(t *testing.T) {
	reply := "```json\n" + `{"summary":"Keep Bash out and mask emails","calls":[
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"Deny Bash","ruleType":"tool_deny","action":"strip","pattern":"Bash","group":"g1","stance":"restrict","intent":"never use Bash"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"mask","ruleType":"pii_redact","action":"redact","pattern":"","group":"g2","intent":"mask emails"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"bad-regex","ruleType":"blocked_pattern","action":"block","pattern":"(","intent":"x"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"strip-skill","ruleType":"skill_deny","action":"strip","pattern":"x","intent":"x"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"anchored","ruleType":"tool_deny","action":"block","pattern":"^Bash$","intent":"x"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"delim","ruleType":"delimiter_integrity","action":"block","intent":"x"}},
		{"tool":"shell","arguments":{"name":"rm","ruleType":"tool_deny","action":"block","pattern":"x"}},
		{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"cap","ruleType":"max_messages","action":"block","pattern":40,"intent":"keep it short"}}
	]}` + "\n```"
	offer, err := ParseFirewallPermissions(reply, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(offer.Rules) != 3 {
		t.Fatalf("rules = %+v", offer.Rules)
	}
	bash := offer.Rules[0]
	if bash.Name != "belai-deny-bash" || bash.Family != FirewallFamilyTool || bash.Variant != "deny" || !bash.Enabled || bash.Priority != 100 {
		t.Fatalf("bash rule = %+v", bash)
	}
	if pii := offer.Rules[1]; pii.RuleType != "pii_redact" || !pii.Enabled || pii.Family != FirewallFamilyContent {
		t.Fatalf("pii rule = %+v", pii)
	}
	if cap := offer.Rules[2]; cap.Pattern != "40" {
		t.Fatalf("max_messages pattern = %q", cap.Pattern)
	}
	if offer.Summary != "Keep Bash out and mask emails" || !strings.HasPrefix(offer.ID, "fwp-") {
		t.Fatalf("offer = %+v", offer)
	}
}

func TestParseFirewallPermissionsSkipsEnforcedRules(t *testing.T) {
	reply := `{"calls":[{"tool":"vulnetix_ai_firewall_guardrail","arguments":{"name":"x","ruleType":"tool_deny","action":"strip","pattern":"Bash"}}]}`
	live := []FirewallGuardrail{{RuleType: "tool_deny", Action: "strip", Pattern: "Bash", Enabled: true}}
	if _, err := ParseFirewallPermissions(reply, live); !errors.Is(err, ErrNoFirewallRules) {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseFirewallPermissions("not json", nil); !errors.Is(err, ErrNoFirewallRules) {
		t.Fatalf("err = %v", err)
	}
}

func TestSelectFirewallVariants(t *testing.T) {
	rule := func(rt, stance string) FirewallRule {
		r, ok := ValidateFirewallRule(FirewallRule{Name: rt, RuleType: rt, Action: "block", Pattern: "WebFetch", Stance: stance})
		if !ok {
			t.Fatalf("invalid %s", rt)
		}
		return r
	}
	allowMode := []FirewallGuardrail{{RuleType: "tool_allow", Action: "block", Pattern: "Read", Enabled: true}}
	cases := []struct {
		name string
		r    FirewallRule
		live []FirewallGuardrail
		want bool
	}{
		{"restrict deny", rule("tool_deny", StanceRestrict), nil, true},
		{"restrict allow side", rule("tool_allow", StanceRestrict), allowMode, false},
		{"only allow", rule("tool_allow", StanceOnly), nil, true},
		{"grant in denylist mode", rule("tool_allow", StanceGrant), nil, false},
		{"grant in allowlist mode", rule("tool_allow", StanceGrant), allowMode, true},
		{"grant in another family's allowlist", rule("mcp_allow", StanceGrant), allowMode, false},
		{"deny side of only", rule("tool_deny", StanceOnly), nil, false},
	}
	for _, tc := range cases {
		got := SelectFirewallVariants([]FirewallRule{tc.r}, tc.live)[0].Enabled
		if got != tc.want {
			t.Errorf("%s: enabled=%v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFirewallGuardrailArgs(t *testing.T) {
	args := FirewallGuardrailArgs(FirewallRule{Name: "belai-x", RuleType: "blocked_pattern", Action: "block", Pattern: "-rf /", Priority: 50, Enabled: false})
	got := strings.Join(args, " ")
	want := "ai-firewall policy guardrail belai-x --rule-type blocked_pattern --action block --pattern=-rf / --priority 50 --disable -o json"
	if got != want {
		t.Fatalf("args = %q", got)
	}
}

func TestFirewallPermWritePayloadCarriesUseCase(t *testing.T) {
	p := BuildFirewallPermWritePayload("never use Bash", []FirewallGuardrail{{RuleType: "tool_deny", Pattern: "Curl"}})
	if p.UseCase != UseCaseFirewallPermWrite || len(p.Tools) != 0 || !strings.Contains(p.User, "Curl") {
		t.Fatalf("payload = %+v", p)
	}
}
