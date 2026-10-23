package sessionsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The A2A relay's half of the host API (docs/a2a.md): the inbox asks for
// envelopes after a cursor, a batch is posted to /hosts/{id}/a2a, and the
// blackout comes back on the post, the session controls and the rc heartbeat.
func TestA2AClient(t *testing.T) {
	var query string
	var posted map[string][]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case apiPath + "/hosts/" + testHost + "/inbox":
			query = r.URL.RawQuery
			w.Write([]byte(`{"prompts":[],"a2a":[{"seq":4,"messageId":"a2a-x","kind":"broadcast"},{"seq":5,"envelope":{"messageId":"a2a-y"}}]}`))
		case apiPath + "/hosts/" + testHost + "/a2a":
			_ = json.NewDecoder(r.Body).Decode(&posted)
			w.Write([]byte(`{"accepted":0,"blackout":true}`))
		case apiPath + "/hosts/" + testHost + "/rc/heartbeat":
			w.Write([]byte(`{"a2aBlackout":true}`))
		case apiPath + "/sessions/" + testSess + "/heartbeat":
			w.Write([]byte(`{"controls":{"a2aBlackout":false}}`))
		}
	}))
	defer srv.Close()
	c, _ := NewClient(BaseURL(srv.URL), func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	ctx := context.Background()
	b, err := c.InboxBatchA2A(ctx, testHost, testSess, time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "a2aAfter=3") {
		t.Fatalf("query = %q", query)
	}
	if len(b.A2A) != 2 || b.A2A[0].Seq != 4 || !strings.Contains(string(b.A2A[0].Envelope), `"a2a-x"`) ||
		b.A2A[1].Seq != 5 || string(b.A2A[1].Envelope) != `{"messageId":"a2a-y"}` {
		t.Fatalf("a2a items = %+v", b.A2A)
	}
	if _, err := c.InboxBatch(ctx, testHost, testSess, time.Second); err != nil || strings.Contains(query, "a2aAfter") {
		t.Fatalf("a plain inbox asked for a2a: %q %v", query, err)
	}
	res, err := c.PostA2A(ctx, testHost, []json.RawMessage{json.RawMessage(`{"messageId":"a2a-z"}`)})
	if err != nil || !res.Blackout || len(posted["envelopes"]) != 1 {
		t.Fatalf("post = %+v %v %v", res, err, posted)
	}
	hb, err := c.RCHeartbeatState(ctx, testHost, 0, nil, nil)
	if err != nil || hb.A2ABlackout == nil || !*hb.A2ABlackout {
		t.Fatalf("rc heartbeat = %+v %v", hb, err)
	}
	ctl, err := c.HeartbeatControls(ctx, testSess)
	if err != nil || ctl.A2ABlackout == nil || *ctl.A2ABlackout {
		t.Fatalf("controls = %+v %v", ctl, err)
	}
}
