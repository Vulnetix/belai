package rc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
	"github.com/vulnetix/belai/internal/vulnetixcli"
)

// attentionSite answers POST /hosts/{id}/attention like the website: it keeps
// what the host sent and answers with a canned status and body.
type attentionSite struct {
	mu     sync.Mutex
	posts  []map[string]string
	status int
	body   string
}

func (s *attentionSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/hosts/"+testHost+"/attention") {
		http.NotFound(w, r)
		return
	}
	var in map[string]string
	_ = json.NewDecoder(r.Body).Decode(&in)
	s.posts = append(s.posts, in)
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func (s *attentionSite) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.posts)
}

type logBuf struct {
	mu sync.Mutex
	sb strings.Builder
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.String()
}

func attentionDaemon(t *testing.T, site *attentionSite, o Options) (*Daemon, *logBuf) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	srv := httptest.NewServer(site)
	t.Cleanup(srv.Close)
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	log := &logBuf{}
	o.Client, o.HostID, o.Out = client, testHost, log
	d, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return d, log
}

const loginGrant = `{"id":"req-1","kind":"login","state":"pending","expiresAt":4102444800000,"deviceCode":"dev-secret","interval":5}`

func TestRequestFilesTheClosedKindAndReason(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	d, log := attentionDaemon(t, site, Options{})
	if err := d.Request(context.Background(), sessionsync.RequestLogin, sessionsync.ReasonCredentialExpiring, ""); err != nil {
		t.Fatal(err)
	}
	if got := site.posts[0]; got["kind"] != "login" || got["reason"] != "credential_expiring" || got["subject"] != "" {
		t.Fatalf("posted %v", got)
	}
	if !d.hasRequest(sessionsync.RequestLogin) {
		t.Fatal("the open request was not remembered")
	}
	if strings.Contains(log.String(), "dev-secret") {
		t.Fatal("the device code reached the log")
	}
	if !strings.Contains(log.String(), "approve it in the web console") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestRequestThatTheWebsiteCannotTakeSaysWhatToRunInstead(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		want   error
	}{
		"older server":   {http.StatusNotFound, sessionsync.ErrNotFound},
		"login rejected": {http.StatusUnauthorized, sessionsync.ErrUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			site := &attentionSite{status: tc.status, body: `{"error":"x"}`}
			d, log := attentionDaemon(t, site, Options{})
			if err := d.Request(context.Background(), sessionsync.RequestLogin, sessionsync.ReasonReauthNeeded, ""); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if d.hasRequest(sessionsync.RequestLogin) {
				t.Fatal("a request nobody received was remembered")
			}
			if !strings.Contains(log.String(), "vulnetix auth login") {
				t.Fatalf("log = %q", log.String())
			}
		})
	}
}

func TestApprovedLoginIsRedeemedOnceDespiteRepeatedHeartbeats(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	var redeemed []string
	d, log := attentionDaemon(t, site, Options{RedeemLogin: func(_ context.Context, code string) error {
		redeemed = append(redeemed, code)
		return nil
	}})
	ctx := context.Background()
	if err := d.Request(ctx, sessionsync.RequestLogin, sessionsync.ReasonCredentialExpiring, ""); err != nil {
		t.Fatal(err)
	}
	approved := []sessionsync.HostRequest{{ID: "req-1", Kind: "login", State: sessionsync.RequestApproved}}
	d.settleRequests(ctx, approved)
	d.settleRequests(ctx, approved)
	if len(redeemed) != 1 || redeemed[0] != "dev-secret" {
		t.Fatalf("redeemed %v, want the device code exactly once", redeemed)
	}
	if d.hasRequest(sessionsync.RequestLogin) {
		t.Fatal("a settled request is still open")
	}
	if !strings.Contains(log.String(), "signed in again") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestDeniedAndLapsedRequestsRedeemNothing(t *testing.T) {
	for _, state := range []string{sessionsync.RequestDenied, sessionsync.RequestExpired, sessionsync.RequestCancelled} {
		t.Run(state, func(t *testing.T) {
			site := &attentionSite{status: http.StatusCreated, body: loginGrant}
			called := false
			d, log := attentionDaemon(t, site, Options{RedeemLogin: func(context.Context, string) error { called = true; return nil }})
			_ = d.Request(context.Background(), sessionsync.RequestLogin, sessionsync.ReasonReauthNeeded, "")
			d.settleRequests(context.Background(), []sessionsync.HostRequest{{ID: "req-1", State: state}})
			if called || d.hasRequest(sessionsync.RequestLogin) {
				t.Fatalf("called=%v open=%v after a %s request", called, d.hasRequest(sessionsync.RequestLogin), state)
			}
			if !strings.Contains(log.String(), state) {
				t.Fatalf("log = %q", log.String())
			}
		})
	}
}

func TestFailedRedeemIsRetriedThenGivenUp(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	tries := 0
	d, log := attentionDaemon(t, site, Options{RedeemLogin: func(context.Context, string) error { tries++; return errors.New("the CLI is busy") }})
	ctx := context.Background()
	_ = d.Request(ctx, sessionsync.RequestLogin, sessionsync.ReasonCredentialExpiring, "")
	approved := []sessionsync.HostRequest{{ID: "req-1", State: sessionsync.RequestApproved}}
	for i := 0; i < redeemTries+2; i++ {
		d.settleRequests(ctx, approved)
	}
	if tries != redeemTries {
		t.Fatalf("tried %d times, want %d", tries, redeemTries)
	}
	if !strings.Contains(log.String(), "vulnetix auth login") {
		t.Fatalf("giving up must say what to run: %q", log.String())
	}
}

func TestExpiredGrantEndsTheRetry(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	tries := 0
	d, _ := attentionDaemon(t, site, Options{RedeemLogin: func(context.Context, string) error {
		tries++
		return vulnetixcli.ErrDeviceExpired
	}})
	ctx := context.Background()
	_ = d.Request(ctx, sessionsync.RequestLogin, sessionsync.ReasonCredentialExpiring, "")
	approved := []sessionsync.HostRequest{{ID: "req-1", State: sessionsync.RequestApproved}}
	d.settleRequests(ctx, approved)
	d.settleRequests(ctx, approved)
	if tries != 1 {
		t.Fatalf("an expired grant was tried %d times, want 1", tries)
	}
}

func TestApproveKindIsRecordedWithoutRedeeming(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: `{"id":"req-2","kind":"approve","state":"pending","expiresAt":4102444800000}`}
	d, log := attentionDaemon(t, site, Options{RedeemLogin: func(context.Context, string) error { t.Fatal("redeemed an approve request"); return nil }})
	ctx := context.Background()
	if err := d.Request(ctx, sessionsync.RequestApprove, sessionsync.ReasonApprovalNeeded, "run-1a2b"); err != nil {
		t.Fatal(err)
	}
	if site.posts[0]["subject"] != "run-1a2b" {
		t.Fatalf("posted %v", site.posts[0])
	}
	d.settleRequests(ctx, []sessionsync.HostRequest{{ID: "req-2", State: sessionsync.RequestApproved}})
	if !strings.Contains(log.String(), "was approved") {
		t.Fatalf("log = %q", log.String())
	}
}

func TestWatchCredentialAsksOnceWhenTheLoginIsNearExpiry(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	expiry := time.Now().Add(DefaultCredentialWarn * 4)
	var mu sync.Mutex
	d, _ := attentionDaemon(t, site, Options{
		CredentialEvery: 5 * time.Millisecond,
		CredentialExpiry: func() (time.Time, bool) {
			mu.Lock()
			defer mu.Unlock()
			return expiry, true
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.watchCredential(ctx); close(done) }()

	time.Sleep(60 * time.Millisecond)
	if n := site.count(); n != 0 {
		t.Fatalf("asked %d times with hours left", n)
	}
	mu.Lock()
	expiry = time.Now().Add(5 * time.Minute)
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for site.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond)
	if n := site.count(); n != 1 {
		t.Fatalf("asked %d times near expiry, want exactly 1", n)
	}
	cancel()
	<-done
}

func TestWatchCredentialDoesNothingForAKeyWithNoExpiry(t *testing.T) {
	site := &attentionSite{status: http.StatusCreated, body: loginGrant}
	d, _ := attentionDaemon(t, site, Options{
		CredentialEvery:  5 * time.Millisecond,
		CredentialExpiry: func() (time.Time, bool) { return time.Time{}, false },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	d.watchCredential(ctx)
	if n := site.count(); n != 0 {
		t.Fatalf("asked %d times for an ApiKey", n)
	}
}
