package sessionsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The inbox names its session, so another Belai on the same host (a second
// TUI, an rc session) never claims this one's prompts.
func TestInboxNamesItsSession(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		w.Write([]byte(`{"prompts":[],"answers":[]}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Inbox(context.Background(), testHost, testSess, time.Second); err != nil {
		t.Fatal(err)
	}
	if got != "wait=1&session="+testSess {
		t.Fatalf("query = %q", got)
	}
}

func TestRCClientRoundTrip(t *testing.T) {
	var host Host
	var ack map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/hosts/" + testHost:
			_ = json.NewDecoder(r.Body).Decode(&host)
		case apiPath + "/hosts/" + testHost + "/dispatch":
			w.Write([]byte(`{"dispatches":[{"id":"d1","kind":"start","cwd":"/src","prompt":"hi"}]}`))
		case apiPath + "/dispatches/d1/ack":
			_ = json.NewDecoder(r.Body).Decode(&ack)
		}
	}))
	defer srv.Close()
	c, _ := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	ctx := context.Background()
	if err := c.PutHost(ctx, testHost, Host{Hostname: "h", RC: &RCInfo{MaxSessions: 2, Dirs: []RCDir{{Path: "/src", Source: "arg"}}}}); err != nil {
		t.Fatal(err)
	}
	if host.RC == nil || host.RC.Dirs[0].Path != "/src" {
		t.Fatalf("host = %+v", host)
	}
	ds, err := c.Dispatches(ctx, testHost, time.Second)
	if err != nil || len(ds) != 1 || ds[0].Cwd != "/src" {
		t.Fatalf("dispatches = %+v %v", ds, err)
	}
	if err := c.AckDispatch(ctx, "d1", DispatchStarted, testSess, ""); err != nil {
		t.Fatal(err)
	}
	if ack["status"] != DispatchStarted || ack["sessionId"] != testSess {
		t.Fatalf("ack = %v", ack)
	}
	// A plain host upsert (the TUI's) sends no rc block at all.
	if b, _ := json.Marshal(Host{Hostname: "h"}); string(b) != `{"hostname":"h","os":"","belaiVersion":""}` {
		t.Fatalf("plain host = %s", b)
	}
}
