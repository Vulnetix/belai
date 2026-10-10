package forge

import (
	"errors"
	"testing"
)

func TestClassifyPushError(t *testing.T) {
	for _, c := range []struct {
		msg     string
		kind    FaultKind
		failure string
	}{
		{"git: remote: Permission to acme/app.git denied to bot.\nfatal: unable to access 'https://github.com/acme/app.git/': The requested URL returned error: 403", FaultInfra, FailureAuth},
		{"git: fatal: Authentication failed for 'https://github.com/acme/app.git/'", FaultInfra, FailureAuth},
		{"gh: HTTP 401: Bad credentials (https://api.github.com/graphql)", FaultInfra, FailureAuth},
		{"git: git@github.com: Permission denied (publickey).", FaultInfra, FailureAuth},
		{"gh: GraphQL: Resource not accessible by integration (createPullRequest)", FaultInfra, FailureAuth},
		{"gh: HTTP 403: You have exceeded a secondary rate limit", FaultInfra, FailureRateLimited},
		{"gh: API rate limit exceeded for installation", FaultInfra, FailureRateLimited},
		{"git: The requested URL returned error: 429", FaultInfra, FailureRateLimited},
		{"gh: was submitted too quickly", FaultInfra, FailureRateLimited},
		{"git: The requested URL returned error: 502", FaultInfra, FailureServer},
		{"gh: HTTP 503: Service Unavailable", FaultInfra, FailureServer},
		{"gh not installed — PR and CI actions need the GitHub CLI (https://cli.github.com)", FaultInfra, FailureNoGH},
		{"git: fatal: could not read Username for 'https://github.com': terminal prompts disabled", FaultInfra, FailurePushCredentials},
		{"git: ! [rejected] belai/K-abc123/a1 -> belai/K-abc123/a1 (non-fast-forward)", FaultWork, ""},
		{"git: ! [rejected] belai/K-abc123/a1 (fetch first)\nerror: failed to push some refs\nhint: Updates were rejected", FaultWork, ""},
		{"belai/K-abc123/a1 has no changes beyond its base 0123456789ab, so there is nothing to publish.", FaultWork, ""},
		{"nothing is committed on belai/K-abc123/a1 yet", FaultWork, ""},
		{"git: ! [remote rejected] belai/K-abc123/a1 (pre-receive hook declined)", FaultWork, ""},
		// A hook that quotes a permission is still the branch's fault.
		{"git: remote: permission denied by policy\n! [remote rejected] x (hook declined)", FaultWork, ""},
		{"git: fatal: unable to access: Could not resolve host: github.com", FaultUnknown, ""},
		{"something else entirely", FaultUnknown, ""},
	} {
		got := ClassifyPushError(errors.New(c.msg))
		if got.Kind != c.kind || got.Failure != c.failure {
			t.Errorf("ClassifyPushError(%q) = %+v, want kind %d failure %q", c.msg, got, c.kind, c.failure)
		}
		if got.Infra() != (c.kind == FaultInfra) {
			t.Errorf("Infra() for %q = %v", c.msg, got.Infra())
		}
	}
	if f := ClassifyPushError(nil); f.Kind != FaultUnknown || f.Infra() {
		t.Fatalf("a nil error is no fault: %+v", f)
	}
}
