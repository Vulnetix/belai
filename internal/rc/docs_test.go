package rc

import (
	"context"
	"net/http/httptest"
	"time"

	"github.com/vulnetix/belai/internal/sessionsync"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/docparity"
)

// The dispatch kinds the daemon handles are the contract with the website. Each
// is named in docs/remote-control.md, and an unknown one is refused, so a new
// kind must be added to both the daemon and the page.
func TestDispatchKindsAreDocumented(t *testing.T) {
	doc := docparity.Read(t, "docs/remote-control.md")
	for _, want := range []string{"`pause` or `resume`", "Pause and resume", "pause marker", "refuses an id that does not look like one"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/remote-control.md lacks %q", want)
		}
	}
}

// An unknown kind is refused with a reason that tells the host to update, so a
// website ahead of the host never silently drops a request.
func TestUnknownDispatchKindIsRefused(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	site := &fakeSite{acks: map[string][3]string{}, delivered: make(chan struct{}, 4)}
	srv := httptest.NewServer(site)
	defer srv.Close()
	client, err := sessionsync.NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Client: client, HostID: testHost, HeartbeatEvery: 10 * time.Millisecond, PollWait: time.Second, Exe: "/bin/belai"})
	if err != nil {
		t.Fatal(err)
	}
	site.queue = []sessionsync.Dispatch{{ID: "u1", Kind: "reboot"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = d.Run(ctx); close(done) }()
	select {
	case <-site.delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("no ack arrived")
	}
	cancel()
	<-done

	site.mu.Lock()
	defer site.mu.Unlock()
	if a := site.acks["u1"]; a[0] != sessionsync.DispatchRefused || !strings.Contains(a[2], "update Belai") {
		t.Fatalf("ack = %v", a)
	}
}
