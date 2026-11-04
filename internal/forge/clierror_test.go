package forge

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCLIErrorSummary(t *testing.T) {
	for _, c := range []struct {
		name, stderr, want string
	}{
		{"rejected beats the generic error and the hints",
			"To https://github.com/acme/app.git\n ! [rejected]        b -> b (fetch first)\nerror: failed to push some refs to 'https://github.com/acme/app.git'\nhint: Updates were rejected because the remote contains work\nhint: See the 'Note about fast-forwards' in 'git push --help' for details.\n",
			"! [rejected] b -> b (fetch first)"},
		{"remote rejected names the refusal",
			" ! [remote rejected] b -> b (refusing to allow a Personal Access Token to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)\nerror: failed to push some refs to 'https://github.com/acme/app.git'\n",
			"! [remote rejected] b -> b (refusing to allow a Personal Access Token to create or update workflow `.github/workflows/ci.yml` without `workflow` scope)"},
		{"a remote error outranks remote progress",
			"remote: Resolving deltas: 100% (2/2), done.\nremote: error: GH013: Repository rule violations found for refs/heads/b.\n ! [remote rejected] b -> b (push declined due to repository rule violations)\n",
			"remote: error: GH013: Repository rule violations found for refs/heads/b."},
		{"fatal outranks a plain remote line",
			"remote: Permission to acme/app.git denied to bot.\nfatal: unable to access 'https://github.com/acme/app.git/': The requested URL returned error: 403\n",
			"fatal: unable to access 'https://github.com/acme/app.git/': The requested URL returned error: 403"},
		{"a plain remote line with content",
			"remote: \nremote: Invalid username or token. Password authentication is not supported for Git operations.\nhint: x\n",
			"remote: Invalid username or token. Password authentication is not supported for Git operations."},
		{"hints and up-to-date are skipped for the last line",
			"something went sideways\nhint: try again\nEverything up-to-date\n",
			"something went sideways"},
		{"nothing but up-to-date",
			"Everything up-to-date\n",
			`exit status 1 after "Everything up-to-date"`},
		{"no stderr", "", "exit status 1"},
	} {
		e := newCLIError(context.Background(), "git", []byte(c.stderr), errors.New("exit status 1"), nil)
		if e.Summary != c.want {
			t.Errorf("%s: Summary = %q, want %q", c.name, e.Summary, c.want)
		}
		if e.Error() != "git: "+c.want {
			t.Errorf("%s: Error() = %q", c.name, e.Error())
		}
		if want := strings.Contains(c.stderr, "Everything up-to-date"); e.UpToDate != want {
			t.Errorf("%s: UpToDate = %v, want %v", c.name, e.UpToDate, want)
		}
	}
}

func TestCLIErrorRedactsAndBounds(t *testing.T) {
	stderr := "fatal: unable to access 'https://x-access-token:s3cr3tvalue1234@github.com/acme/app.git/': 403\n" +
		"remote: token ghp_abcdefghijklmnopqrstuvwxyz0123 refused\n" +
		"remote: env value envsecret-0001 echoed\n" +
		"remote: \x1b[31mred\x1b[0m\u202e text by belai@example.com\n"
	e := newCLIError(context.Background(), "git", []byte(stderr), errors.New("exit status 128"), []string{"GH_TOKEN=envsecret-0001", "GIT_AUTHOR_EMAIL=belai@example.com"})
	for _, leak := range []string{"s3cr3tvalue1234", "x-access-token", "ghp_abcdef", "envsecret-0001", "\x1b", "\u202e"} {
		if strings.Contains(e.Stderr, leak) || strings.Contains(e.Summary, leak) {
			t.Errorf("%q leaked: Stderr %q Summary %q", leak, e.Stderr, e.Summary)
		}
	}
	if !strings.Contains(e.Summary, "https://[redacted]@github.com/acme/app.git/") {
		t.Errorf("Summary = %q, want the URL kept with its credential redacted", e.Summary)
	}
	if !strings.Contains(e.Stderr, "belai@example.com") {
		t.Errorf("an identity value is not a credential: %q", e.Stderr)
	}
	if n := len(strings.Split(e.Stderr, "\n")); n != 4 {
		t.Errorf("Stderr keeps %d lines, want 4: %q", n, e.Stderr)
	}

	// A long stderr keeps only its tail, cut at a line boundary.
	long := strings.Repeat("remote: progress line that says nothing useful at all\n", 400) + " ! [rejected] b -> b (non-fast-forward)\n"
	e = newCLIError(context.Background(), "git", []byte(long), errors.New("exit status 1"), nil)
	if len(e.Stderr) > maxStderrBytes {
		t.Errorf("Stderr is %d bytes, want at most %d", len(e.Stderr), maxStderrBytes)
	}
	if !strings.HasPrefix(e.Stderr, "remote: progress line") || !strings.HasSuffix(e.Stderr, "(non-fast-forward)") {
		t.Errorf("the tail must start at a whole line and end with the cause: %q ... %q", e.Stderr[:40], e.Stderr[len(e.Stderr)-40:])
	}
	if e.Summary != "! [rejected] b -> b (non-fast-forward)" {
		t.Errorf("Summary = %q", e.Summary)
	}
}

func TestCLIErrorTimedOut(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := newCLIError(ctx, "gh", []byte("fatal: half way\n"), context.Canceled, nil)
	if e.Error() != "gh: timed out" || e.Stderr != "fatal: half way" {
		t.Fatalf("Error() %q Stderr %q", e.Error(), e.Stderr)
	}
}

// Both real runners return a *CLIError whose Summary is the cause, not the
// last line.
func TestRunnersReturnCLIError(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	script := `printf 'remote: error: denied by rule\nhint: retry\nEverything up-to-date\n' >&2; exit 1`
	for name, r := range map[string]Runner{"ExecRunner": ExecRunner, "HardenedGit": HardenedGit("", "")} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := r(ctx, t.TempDir(), "sh", "-c", script)
		cancel()
		ce := AsCLIError(err)
		if ce == nil {
			t.Fatalf("%s: %T %v, want a *CLIError", name, err, err)
		}
		if err.Error() != "sh: remote: error: denied by rule" || !ce.UpToDate {
			t.Errorf("%s: Error() %q UpToDate %v", name, err.Error(), ce.UpToDate)
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Errorf("%s: the exec error must stay reachable: %v", name, err)
		}
	}
}
