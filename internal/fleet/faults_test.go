package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/firewall"
	"github.com/vulnetix/belai/internal/run"
)

func TestEndReason(t *testing.T) {
	if EndReason(nil) != "" {
		t.Error("no error, no reason")
	}
	if got := EndReason(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("deadline = %q", got)
	}
	if got := EndReason(errors.New("provider returned 429 Too Many Requests")); got != "rate_limited" {
		t.Errorf("429 text = %q", got)
	}
	if got := EndReason(errors.New("exit status 2")); got != "error" {
		t.Errorf("plain = %q", got)
	}
}

// A gateway refusing the model by policy, or a quota only a person can raise,
// is a config fault: the code comes from the body, which outranks the status,
// whether the error kept its type or reached the worker as text.
func TestAPolicyRefusalIsAConfigFault(t *testing.T) {
	policy := func(code string) string {
		return `{"error":{"message":"model gpt-5 is denied by organisation policy","type":"policy_violation","code":"` + code + `"}}`
	}
	for _, code := range firewall.PolicyRefusalCodes {
		pe := &run.ProviderError{Status: 403, Body: policy(code)}
		if got := classifyErr(pe); got != (Fault{Code: FaultPolicy, Status: 403, Config: true}) {
			t.Errorf("%s: %+v", code, got)
		}
		// Wrapped by something that kept only the text.
		if got := classifyErr(fmt.Errorf("worker turn ended: %s", pe.Error())); got != (Fault{Code: FaultPolicy, Status: 403, Config: true}) {
			t.Errorf("%s as text: %+v", code, got)
		}
		// Text after the body, or a body over several lines, does not hide it.
		if got := classifyErr(fmt.Errorf("%s (after 2 passes)", pe.Error())); got != (Fault{Code: FaultPolicy, Status: 403, Config: true}) {
			t.Errorf("%s with a suffix: %+v", code, got)
		}
		multi := &run.ProviderError{Status: 403, Body: strings.ReplaceAll(policy(code), ",", ",\n  ")}
		if got := classifyErr(errors.New(multi.Error())); got != (Fault{Code: FaultPolicy, Status: 403, Config: true}) {
			t.Errorf("%s over lines: %+v", code, got)
		}
	}
	quota := &run.ProviderError{Status: 429, Body: `{"success":false,"errors":[{"code":429,"message":"Pix built-in model quota reached for this month. Resets on 2026-11-01."}],"messages":[],"result":null}`}
	if got := classifyErr(quota); got != (Fault{Code: FaultQuota, Status: 429, Config: true}) {
		t.Errorf("builtin quota: %+v", got)
	}
	fwQuota := &run.ProviderError{Status: 429, Body: `{"error":{"message":"quota","type":"policy_violation","code":"provider_quota"}}`}
	if got := classifyErr(fwQuota); got != (Fault{Code: FaultQuota, Status: 429, Config: true}) {
		t.Errorf("firewall quota: %+v", got)
	}
	// Everything else keeps its class, and none is a config fault.
	for _, c := range []struct {
		err  error
		code string
	}{
		{&run.ProviderError{Status: 403, Body: "forbidden"}, FaultAuth},
		{&run.ProviderError{Status: 403, Body: `{"error":{"message":"blocked","type":"policy_violation","code":"request_blocked"}}`}, FaultAuth},
		{&run.ProviderError{Status: 429, Body: "slow down"}, FaultRateLimit},
		{&run.ProviderError{Status: 429, Body: "insufficient_quota"}, FaultQuota},
		{&run.ProviderError{Status: 503, Body: `{"error":{"message":"down","code":"overloaded"}}`}, FaultUnavailable},
	} {
		if got := classifyErr(c.err); got.Code != c.code || got.Config {
			t.Errorf("%v: %+v, want %s and no config", c.err, got, c.code)
		}
	}
	if got := EndReason(&run.ProviderError{Status: 403, Body: policy("model_denied")}); got != "auth_failed" {
		t.Errorf("end reason %q", got)
	}
}
