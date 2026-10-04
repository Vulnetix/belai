package sessionsync

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// teleportSite answers the teleport routes the way vdb-site does, for one
// teleport, and records what it was asked.
type teleportSite struct {
	seen   []string
	status int
	body   string
}

func (s *teleportSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.seen = append(s.seen, r.Method+" "+strings.TrimPrefix(r.URL.RequestURI(), "/api/site/v1/belai"))
	if r.Header.Get("Authorization") != "ApiKey o:k" {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	if s.status != 0 {
		w.WriteHeader(s.status)
	}
	_, _ = w.Write([]byte(s.body))
}

func teleportClient(t *testing.T, s *teleportSite) *Client {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTeleportCreateGetAndAckSpeakTheServersShapes(t *testing.T) {
	s := &teleportSite{body: `{"teleport":{"id":"t1","originSessionId":"o1","status":"ready","snapshotSeq":4,"newSessionId":null},` +
		`"git":{"branch":"main"},"overrides":{"mode":"plan","model":"m"},"manifest":{"profile":"builder","profiles":[{"profile":"builder","library":"l","version":"202610010941"}]}}`}
	c := teleportClient(t, s)
	ctx := context.Background()

	tp, err := c.TeleportCreate(ctx, "host 1", "abcd1234")
	if err != nil || tp.ID != "t1" || tp.SnapshotSeq != 4 || tp.Status != TeleportReady {
		t.Fatalf("create = %+v %v", tp, err)
	}
	st, err := c.TeleportGet(ctx, "host 1", "t1")
	if err != nil || st.Manifest.Profile != "builder" || len(st.Manifest.Profiles) != 1 || st.Overrides.Mode != "plan" || !strings.Contains(string(st.Git), "main") {
		t.Fatalf("get = %+v %v", st, err)
	}
	if err := c.TeleportAck(ctx, "host 1", "t1", "completed", "n1", "a\nb"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /hosts/host%201/teleports",
		"GET /hosts/host%201/teleports/t1",
		"POST /hosts/host%201/teleports/t1/ack",
	}
	for i, w := range want {
		if s.seen[i] != w {
			t.Errorf("request %d = %q, want %q", i, s.seen[i], w)
		}
	}
}

func TestTeleportEntriesAskForTheNextPageAfterASeq(t *testing.T) {
	s := &teleportSite{body: `{"entries":[{"seq":5,"id":"e5","type":"user","timestamp":1}]}`}
	c := teleportClient(t, s)
	got, err := c.TeleportEntries(context.Background(), "h", "t1", 4)
	if err != nil || len(got) != 1 || got[0].Seq != 5 {
		t.Fatalf("entries = %+v %v", got, err)
	}
	if s.seen[0] != "GET /hosts/h/teleports/t1/entries?after=4&limit=100" {
		t.Fatalf("request = %q", s.seen[0])
	}
}

func TestTeleportRefusalsCarryTheServersReasonCleaned(t *testing.T) {
	s := &teleportSite{status: http.StatusConflict, body: "{\"error\":\"a sandbox has no terminal\\n\\u001b[31mred\"}"}
	c := teleportClient(t, s)
	_, err := c.TeleportCreate(context.Background(), "h", "abcd1234")
	var te *TeleportError
	if !errors.As(err, &te) || te.Status != http.StatusConflict || strings.ContainsAny(te.Reason, "\x1b\n") || !strings.Contains(te.Reason, "a sandbox has no terminal") {
		t.Fatalf("err = %#v", err)
	}
	// It still matches the package's sentinels by status.
	if !errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		t.Fatalf("sentinels: conflict=%v notfound=%v", errors.Is(err, ErrConflict), errors.Is(err, ErrNotFound))
	}
	s.status, s.body = http.StatusNotFound, `{"error":"not found"}`
	if _, err := c.TeleportGet(context.Background(), "h", "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// A body that is not JSON still yields a status error.
	s.status, s.body = http.StatusBadGateway, "<html>bad gateway</html>"
	if _, err := c.TeleportGet(context.Background(), "h", "t1"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v", err)
	}
}

func TestTeleportFileDecodesAndBoundsWhatItReads(t *testing.T) {
	s := &teleportSite{body: `{"contentBase64":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}`}
	c := teleportClient(t, s)
	b, err := c.TeleportFile(context.Background(), "h", "t1", strings.Repeat("a", 64))
	if err != nil || string(b) != "hello" {
		t.Fatalf("file = %q %v", b, err)
	}
	s.body = `{"contentBase64":"%%%"}`
	if _, err := c.TeleportFile(context.Background(), "h", "t1", "x"); err == nil {
		t.Fatal("a file that is not base64 was accepted")
	}
	s.body = `{"contentBase64":"` + base64.StdEncoding.EncodeToString(make([]byte, MaxLibraryFile+1)) + `"}`
	if _, err := c.TeleportFile(context.Background(), "h", "t1", "x"); err == nil {
		t.Fatal("an oversized file was accepted")
	}
}
