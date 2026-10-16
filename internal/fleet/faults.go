package fleet

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/resilience"
	"github.com/vulnetix/belai/internal/run"
)

// Fault codes: why a worker's item or the worker itself failed, as a fixed
// code the registry, the audit and the crew log can carry. They are derived
// from the error's type, status and the harness's own sentinels, never from
// the text a provider or a model wrote.
const (
	FaultUnavailable = "provider_unavailable" // 5xx, 408, 409
	FaultRateLimit   = "provider_rate_limit"  // 429
	FaultTransport   = "provider_transport"   // reset, timeout, refused
	FaultAuth        = "provider_auth"        // 401, 403
	FaultQuota       = "provider_quota"       // quota, billing, key
	FaultRejected    = "provider_rejected"    // any other 4xx
	FaultOverflow    = "context_overflow"
	FaultWallBudget  = "wall_budget"
	FaultTokenBudget = "token_budget"
	FaultLeaseLost   = "lease_lost"
	FaultWithheld    = "withheld"
	FaultWorkspace   = "workspace"
	FaultStalled     = "stalled"
	FaultEvaluator   = "evaluator"
	FaultMaxPasses   = "max_passes"
	FaultIncomplete  = "incomplete"
	FaultError       = "error"
	FaultWorker      = "worker_failed"
)

// FaultCodes lists every code, for the control room's filter and the docs.
var FaultCodes = []string{FaultUnavailable, FaultRateLimit, FaultTransport, FaultAuth, FaultQuota, FaultRejected,
	FaultOverflow, FaultWallBudget, FaultTokenBudget, FaultLeaseLost, FaultWithheld, FaultWorkspace,
	FaultStalled, FaultEvaluator, FaultMaxPasses, FaultIncomplete, FaultError, FaultWorker}

// Fault is a classified failure.
type Fault struct {
	Code string
	// Status is the provider's HTTP status, 0 when there was none.
	Status int
	// Outage is a failure that was the provider's, not the item's: it costs
	// the item no attempt, and enough of them in a row move the worker to a
	// fallback.
	Outage bool
}

// providerOutage matches a provider failure that reached the worker only as
// text (wrapped by something that dropped the type).
var providerOutage = regexp.MustCompile(`provider returned (408|409|429|5[0-9][0-9])\b`)

// classifyFault classifies how an item's turn ended. cause is the item
// context's cause; stop the goal loop's stop reason.
func classifyFault(runErr, cause error, stop run.StopReason) Fault {
	switch {
	case errors.Is(cause, errWallBudget):
		return Fault{Code: FaultWallBudget}
	case errors.Is(cause, errTokenBudget):
		return Fault{Code: FaultTokenBudget}
	case errors.Is(cause, errLeaseLost):
		return Fault{Code: FaultLeaseLost}
	}
	if runErr != nil {
		return classifyErr(runErr)
	}
	switch stop {
	case run.StopStalled:
		return Fault{Code: FaultStalled}
	case run.StopEvaluator:
		return Fault{Code: FaultEvaluator}
	case run.StopMaxPasses:
		return Fault{Code: FaultMaxPasses}
	}
	return Fault{Code: FaultIncomplete}
}

func classifyErr(err error) Fault {
	var withheld *agent.ItemWithheldError
	if errors.As(err, &withheld) {
		return Fault{Code: FaultWithheld}
	}
	var pe *run.ProviderError
	v := (resilience.DefaultClassifier{}).Classify(err)
	if errors.As(err, &pe) && pe.Status != 0 {
		// Quota or billing text outranks the status, a 429 included: it is
		// the account, and waiting will not clear it (docs/resilience.md).
		switch {
		case v.Reason == deniedReason:
			return Fault{Code: FaultQuota, Status: pe.Status}
		case v.Class == resilience.ClassOverflow:
			return Fault{Code: FaultOverflow, Status: pe.Status}
		}
		return faultForStatus(pe.Status)
	}
	switch v.Class {
	case resilience.ClassOverflow:
		return Fault{Code: FaultOverflow}
	case resilience.ClassAborted:
		if errors.Is(err, context.DeadlineExceeded) {
			return Fault{Code: FaultTransport, Outage: true}
		}
		return Fault{Code: FaultError}
	case resilience.ClassRetryable:
		// Retries ran out on a failure that could have cleared.
		if v.RetryAfter == resilience.DefaultRateLimitRetryAfter {
			return Fault{Code: FaultRateLimit, Outage: true}
		}
		return Fault{Code: FaultTransport, Outage: true}
	}
	if v.Reason == deniedReason {
		return Fault{Code: FaultQuota}
	}
	if m := providerOutage.FindStringSubmatch(err.Error()); m != nil {
		var status int
		for _, c := range m[1] {
			status = status*10 + int(c-'0')
		}
		return faultForStatus(status)
	}
	return Fault{Code: FaultError}
}

// deniedReason is the resilience classifier's verdict on quota, billing and
// key text.
const deniedReason = "provider denied the request"

func faultForStatus(status int) Fault {
	switch {
	case status == http.StatusTooManyRequests:
		return Fault{Code: FaultRateLimit, Status: status, Outage: true}
	case status >= 500, status == http.StatusRequestTimeout, status == http.StatusConflict:
		return Fault{Code: FaultUnavailable, Status: status, Outage: true}
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return Fault{Code: FaultAuth, Status: status}
	}
	return Fault{Code: FaultRejected, Status: status}
}

// transientProviderError reports whether err is the model provider refusing the
// call for its own reasons, which says nothing about the item.
func transientProviderError(err error) bool { return classifyErr(err).Outage }
