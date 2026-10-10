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
	if _, _, _, err := c.Inbox(context.Background(), testHost, testSess, time.Second); err != nil {
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

// A filed forge request posts exactly the contract's body to the host's route
// and hands back the status, so the relay can tell a refusal from a retry.
func TestFileForgeRequest(t *testing.T) {
	var body map[string]any
	status := http.StatusCreated
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != apiPath+"/hosts/"+testHost+"/forge-requests" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(status)
		if status < 300 {
			w.Write([]byte(`{"uuid":"u1","status":"pending"}`))
		}
	}))
	defer srv.Close()
	c, _ := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	req := ForgeRequest{RequestID: "0123456789abcdef", ItemID: "i", WorkerID: "w", Kind: "pull_request", RepoOwner: "acme", RepoName: "app",
		Branch: "belai/K-abc123/a1", BaseSHA: "b", HeadSHA: "h", BundleSHA256: "s", BundleBytes: 42, Title: "t", Failure: "auth"}
	got, filed, err := c.FileForgeRequest(context.Background(), testHost, req)
	if err != nil || got != http.StatusCreated || filed.UUID != "u1" || filed.Status != "pending" {
		t.Fatalf("FileForgeRequest = %d %+v %v", got, filed, err)
	}
	want := []string{"requestId", "itemId", "workerId", "kind", "repoOwner", "repoName", "branch", "baseSha", "headSha", "bundleSha256", "bundleBytes", "title", "failure"}
	if len(body) != len(want) {
		t.Fatalf("body has %d fields, want %d: %v", len(body), len(want), body)
	}
	for _, k := range want {
		if _, ok := body[k]; !ok {
			t.Errorf("body lacks %q", k)
		}
	}
	status = http.StatusForbidden
	if got, _, err := c.FileForgeRequest(context.Background(), testHost, req); err != nil || got != http.StatusForbidden {
		t.Fatalf("a refusal is a status, not an error: %d %v", got, err)
	}
}

// A steer dispatch carries the coordinator's fields flat, beside the worker id
// a pause names.
func TestSteerDispatchDecodes(t *testing.T) {
	var d Dispatch
	raw := `{"id":"d1","kind":"steer","worker":"belai-builder-3f9a2c","item":"i1","request":"0123456789abcdef","event":"took_over","pr":7,"prUrl":"https://github.com/acme/app/pull/7","until":0,"reason":"pr_opened"}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	if d.Worker != "belai-builder-3f9a2c" || d.Item != "i1" || d.Request != "0123456789abcdef" || d.Event != "took_over" || d.PR != 7 || d.PRURL != "https://github.com/acme/app/pull/7" || d.Reason != "pr_opened" {
		t.Fatalf("steer = %+v", d)
	}
}
