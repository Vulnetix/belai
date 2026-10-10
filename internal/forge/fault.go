package forge

import "strings"

// FaultKind says whose fault a failed push or pull request was.
type FaultKind int

// Push fault kinds.
const (
	// FaultUnknown is a failure the table does not recognise. It is reported
	// as it always was and is never handed to a coordinator.
	FaultUnknown FaultKind = iota
	// FaultInfra is the machine's or the forge's: a refused or missing
	// credential, a rate limit, a server error, a missing CLI. The work itself
	// is fine, so another party holding a credential may publish it.
	FaultInfra
	// FaultWork is the branch's: a rejected non-fast-forward, nothing to
	// publish, a hook that declined it. Nobody else can publish it either.
	FaultWork
)

// Infrastructure failure words, as the forge coordinator contract names them
// (BelaiForgeRequest.failure).
const (
	FailureAuth            = "auth"
	FailureRateLimited     = "rate_limited"
	FailureServer          = "server"
	FailureNoGH            = "no_gh"
	FailurePushCredentials = "push_credentials"
)

// Fault is a classified push or pull request failure. Failure is one of the
// Failure* words for an infrastructure fault and "" otherwise.
type Fault struct {
	Kind    FaultKind
	Failure string
}

// Infra reports whether the fault is an infrastructure fault.
func (f Fault) Infra() bool { return f.Kind == FaultInfra && f.Failure != "" }

// workFaults are the failures that belong to the branch. They are checked
// first: a hook that declined a push can quote a permission in its message.
var workFaults = []string{
	"non-fast-forward",
	"fetch first",
	"updates were rejected",
	"[rejected]",
	"nothing is committed",
	"nothing to publish",
	"no changes beyond its base",
	"hook declined",
	"pre-receive hook",
	"protected branch",
	"left its branch",
	".git file was changed",
}

// infraFaults maps lower-cased substrings to the failure word, in order: the
// first match wins.
var infraFaults = []struct {
	needle, failure string
}{
	{"gh not installed", FailureNoGH},
	{"executable file not found", FailureNoGH},
	{"could not read username", FailurePushCredentials},
	{"could not read password", FailurePushCredentials},
	{"terminal prompts disabled", FailurePushCredentials},
	{"secondary rate limit", FailureRateLimited},
	{"rate limit", FailureRateLimited},
	{"submitted too quickly", FailureRateLimited},
	{"error: 429", FailureRateLimited},
	{"http 429", FailureRateLimited},
	{"too many requests", FailureRateLimited},
	{"error: 401", FailureAuth},
	{"error: 403", FailureAuth},
	{"http 401", FailureAuth},
	{"http 403", FailureAuth},
	{"bad credentials", FailureAuth},
	{"authentication failed", FailureAuth},
	{"invalid username or password", FailureAuth},
	{"permission denied", FailureAuth},
	{"permission to ", FailureAuth},
	{"resource not accessible by integration", FailureAuth},
	{"gh auth login", FailureAuth},
	{"error: 500", FailureServer},
	{"error: 502", FailureServer},
	{"error: 503", FailureServer},
	{"error: 504", FailureServer},
	{"http 500", FailureServer},
	{"http 502", FailureServer},
	{"http 503", FailureServer},
	{"http 504", FailureServer},
	{"internal server error", FailureServer},
	{"bad gateway", FailureServer},
	{"service unavailable", FailureServer},
	{"gateway timeout", FailureServer},
}

// ClassifyPushError sorts a failed push or pull request by whose fault it was,
// from the error text git and the forge CLI print. It reads the text and
// returns only a kind and a fixed word: nothing of the text is carried.
func ClassifyPushError(err error) Fault {
	if err == nil {
		return Fault{}
	}
	msg := strings.ToLower(err.Error())
	for _, n := range workFaults {
		if strings.Contains(msg, n) {
			return Fault{Kind: FaultWork}
		}
	}
	for _, f := range infraFaults {
		if strings.Contains(msg, f.needle) {
			return Fault{Kind: FaultInfra, Failure: f.failure}
		}
	}
	return Fault{}
}
