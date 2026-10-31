package vulnetixcli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Vulnetix/belai#40: the getting started login failed with "authentication
// test failed" the moment the browser approved it, while a later
// `vulnetix auth login` by hand worked. The CLI checks the key against the
// Vulnetix API before saving it, and the API can refuse a key it has just
// issued for about a minute.

// refusingCLI is a fake vulnetix that fails its auth test the first refusals
// times and then succeeds, counting runs in a file. It also records whether
// the key reached it through the environment and never its argv.
func refusingCLI(t *testing.T, refusals int, failure string) (CLI, func() int, string) {
	t.Helper()
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	seen := filepath.Join(dir, "seen")
	script := "#!/bin/sh\n" +
		"n=$(cat " + count + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + count + "\n" +
		"printf '%s|%s|%s\\n' \"$VULNETIX_ORG_ID\" \"$VULNETIX_API_KEY\" \"$*\" >> " + seen + "\n" +
		"if [ $n -le " + strconv.Itoa(refusals) + " ]; then echo 'Testing authentication...'; echo '" + failure + "' >&2; exit 1; fi\n" +
		"echo 'Credentials saved to home store'\n"
	path := filepath.Join(dir, "vulnetix")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runs := func() int {
		b, _ := os.ReadFile(count)
		n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		return n
	}
	return CLI{Path: path}, runs, seen
}

func shortWaits(t *testing.T, n int) {
	t.Helper()
	old := saveLoginWaits
	saveLoginWaits = make([]time.Duration, n)
	for i := range saveLoginWaits {
		saveLoginWaits[i] = time.Millisecond
	}
	t.Cleanup(func() { saveLoginWaits = old })
}

const refused = "Error: authentication test failed: API error (401): Invalid API key"

func TestSaveLoginOutlastsAFreshKeyRefusal(t *testing.T) {
	shortWaits(t, 5)
	c, runs, seen := refusingCLI(t, 3, refused)
	retries := 0
	if err := c.SaveLogin(context.Background(), "org-1", "abc123", func() { retries++ }); err != nil {
		t.Fatalf("SaveLogin = %v, want success after the key is accepted", err)
	}
	if runs() != 4 || retries != 3 {
		t.Fatalf("runs = %d, retries = %d, want 4 and 3", runs(), retries)
	}
	b, _ := os.ReadFile(seen)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		org, rest, _ := strings.Cut(l, "|")
		key, argv, _ := strings.Cut(rest, "|")
		if org != "org-1" || key != "abc123" {
			t.Fatalf("environment = %q, want the org and key", l)
		}
		if strings.Contains(argv, "abc123") || !strings.HasSuffix(argv, "--disable-memory auth login --noninteractive --store home") {
			t.Fatalf("argv = %q, want the fixed argv without the key", argv)
		}
	}
}

func TestSaveLoginGivesUpWithAReasonAndAWayOut(t *testing.T) {
	shortWaits(t, 2)
	c, runs, _ := refusingCLI(t, 100, refused)
	err := c.SaveLogin(context.Background(), "org-1", "abc123")
	if !errors.Is(err, ErrLoginNotAccepted) {
		t.Fatalf("SaveLogin = %v, want ErrLoginNotAccepted", err)
	}
	if runs() != 3 {
		t.Fatalf("runs = %d, want 1 + one per wait", runs())
	}
	for _, want := range []string{"Invalid API key", "vulnetix auth login in a terminal", "/welcome"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "abc123") {
		t.Fatalf("error %q carries the key", err)
	}
}

func TestSaveLoginDoesNotRetryOtherFailures(t *testing.T) {
	shortWaits(t, 5)
	c, runs, _ := refusingCLI(t, 100, "Error: --org-id must be a valid UUID, got: org-1")
	err := c.SaveLogin(context.Background(), "org-1", "abc123", func() { t.Fatal("retried a failure that is not the auth test") })
	if err == nil || errors.Is(err, ErrLoginNotAccepted) || runs() != 1 {
		t.Fatalf("SaveLogin = %v after %d runs, want one plain failure", err, runs())
	}
	if !strings.Contains(err.Error(), "valid UUID") {
		t.Fatalf("error %q lacks the CLI's reason", err)
	}
}

func TestSaveLoginStopsWhenCancelled(t *testing.T) {
	old := saveLoginWaits
	saveLoginWaits = []time.Duration{time.Hour}
	t.Cleanup(func() { saveLoginWaits = old })
	c, runs, _ := refusingCLI(t, 100, refused)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.SaveLogin(ctx, "org-1", "abc123", cancel) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || runs() != 1 {
			t.Fatalf("SaveLogin = %v after %d runs, want context.Canceled after one", err, runs())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SaveLogin kept waiting after its context was cancelled")
	}
}

func TestSaveLoginRetriesATimedOutCheck(t *testing.T) {
	if !loginRetryable(Result{TimedOut: true}) {
		t.Fatal("a run that timed out is not retried")
	}
	if loginRetryable(Result{Stderr: "failed to save credentials: permission denied"}) {
		t.Fatal("a store failure is retried")
	}
	if !loginRetryable(Result{Stdout: "Testing authentication...", Stderr: "Error: Authentication test failed: HTTP 503"}) {
		t.Fatal("the auth test refusal is matched case-sensitively")
	}
}

// The waits must outlast the Vulnetix API's 60-second cache of a missing
// secret (vdb-api authentikSecretTTL), with a margin.
func TestSaveLoginWaitsOutlastTheAPICache(t *testing.T) {
	var total time.Duration
	for _, w := range saveLoginWaits {
		total += w
	}
	if total < 90*time.Second || total > 3*time.Minute {
		t.Fatalf("waits total %v, want between 90s and 3m", total)
	}
}

func TestLoginFailureTextIsOneBoundedLine(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := loginFailureText(Result{Stdout: "a\n\x1b[31mb\x1b[0m", Stderr: long})
	if len(got) > 300 || strings.ContainsAny(got, "\n\x1b") {
		t.Fatalf("loginFailureText = %q, want one plain line of at most 300 bytes", got)
	}
}
