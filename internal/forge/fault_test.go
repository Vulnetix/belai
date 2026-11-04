package forge

import (
	"context"
	"errors"
	"fmt"
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

// cliErr builds the error the real runners return for stderr.
func cliErr(stderr string) error {
	return newCLIError(context.Background(), "git", []byte(stderr), errors.New("exit status 1"), nil)
}

// A push's cause is in stderr before git's hints and its closing lines, so the
// classifier reads the whole tail, not the line a note shows.
func TestClassifyPushErrorReadsTheStderrTail(t *testing.T) {
	for _, c := range []struct {
		name, stderr string
		kind         FaultKind
		failure      string
	}{
		{"rejected then hints", "To https://github.com/acme/app.git\n ! [rejected]        belai/K-abc123/a1 -> belai/K-abc123/a1 (fetch first)\nerror: failed to push some refs to 'https://github.com/acme/app.git'\nhint: Updates were rejected because the remote contains work that you do not\nhint: have locally.\nhint: See the 'Note about fast-forwards' in 'git push --help' for details.\n", FaultWork, ""},
		{"non-fast-forward then hints", " ! [rejected]        belai/K-abc123/a1 -> belai/K-abc123/a1 (non-fast-forward)\nerror: failed to push some refs to 'https://github.com/acme/app.git'\nhint: See the 'Note about fast-forwards' in 'git push --help' for details.\n", FaultWork, ""},
		{"workflow scope (token)", "To https://github.com/acme/app.git\n ! [remote rejected] belai/K-abc123/a1 -> belai/K-abc123/a1 (refusing to allow a Personal Access Token to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)\nerror: failed to push some refs to 'https://github.com/acme/app.git'\n", FaultInfra, FailureAuth},
		{"workflow scope (oauth app)", " ! [remote rejected] x -> x (refusing to allow an OAuth App to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)\nerror: failed to push some refs\n", FaultInfra, FailureAuth},
		{"workflow permission (github app)", " ! [remote rejected] x -> x (refusing to allow a GitHub App to create or update workflow `.github/workflows/ci.yml` without `workflows` permission)\nerror: failed to push some refs\n", FaultInfra, FailureAuth},
		{"invalid token", "remote: Invalid username or token. Password authentication is not supported for Git operations.\nfatal: Authentication failed for 'https://github.com/acme/app.git/'\n", FaultInfra, FailureAuth},
		{"password sent", "remote: Password authentication is not supported for Git operations.\nfatal: unable to access 'https://github.com/acme/app.git/'\n", FaultInfra, FailurePushCredentials},
		{"up to date after a remote error", "remote: Internal Server Error\nEverything up-to-date\n", FaultInfra, FailureServer},
		{"403 then nothing", "remote: Permission to acme/app.git denied to bot.\nfatal: unable to access 'https://github.com/acme/app.git/': The requested URL returned error: 403\n", FaultInfra, FailureAuth},
	} {
		got := ClassifyPushError(fmt.Errorf("push: %w", cliErr(c.stderr)))
		if got.Kind != c.kind || got.Failure != c.failure {
			t.Errorf("%s: got %+v, want kind %d failure %q", c.name, got, c.kind, c.failure)
		}
	}
}
