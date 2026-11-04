package firewall

import "testing"

func TestParseRefusal(t *testing.T) {
	policy := func(code string) string {
		return `{"error":{"message":"model gpt-5 is denied by organisation policy","type":"policy_violation","code":"` + code + `"}}`
	}
	for _, code := range PolicyRefusalCodes {
		r, ok := ParseRefusal([]byte(policy(code)))
		if !ok || r.Code != code || r.Quota {
			t.Errorf("%s: %+v %v", code, r, ok)
		}
	}
	for _, c := range []struct {
		name  string
		body  string
		code  string
		quota bool
		ok    bool
	}{
		{"firewall quota", `{"error":{"message":"quota","type":"policy_violation","code":"provider_quota"}}`, QuotaRefusalCode, true, true},
		{"builtin quota", `{"success":false,"errors":[{"code":429,"message":"Pix built-in model quota reached for this month. Resets on 2026-11-01."}],"messages":[],"result":null}`, BuiltinQuotaCode, true, true},
		{"builtins off", `{"success":false,"errors":[{"code":429,"message":"Pix built-in models are switched off for this organisation: the monthly quota is 0 tokens."}]}`, BuiltinQuotaCode, true, true},
		{"sandbox model", `{"success":false,"errors":[{"code":403,"message":"only Clef and the Pix built-in models run in a Pix sandbox"}]}`, "model_not_allowed", false, true},
		// A guardrail judges one request, not the model.
		{"guardrail", `{"error":{"message":"blocked by guardrail","type":"policy_violation","code":"request_blocked"}}`, "", false, false},
		{"bad key", `{"error":{"message":"bad","type":"invalid_request_error","code":"invalid_api_key"}}`, "", false, false},
		{"other cloudflare", `{"success":false,"errors":[{"code":500,"message":"internal error"}]}`, "", false, false},
		// The words alone, outside the gateway's envelope, are not a refusal.
		{"text", `Pix built-in model quota reached for this month`, "", false, false},
		{"success envelope", `{"success":true,"errors":[{"message":"Pix built-in model quota reached"}]}`, "", false, false},
		{"empty", ``, "", false, false},
	} {
		r, ok := ParseRefusal([]byte(c.body))
		if ok != c.ok || r.Code != c.code || r.Quota != c.quota {
			t.Errorf("%s: %+v %v", c.name, r, ok)
		}
	}
}
