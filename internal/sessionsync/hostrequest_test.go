package sessionsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jwtWith(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "HS512"}) + "." + enc(claims) + ".sig"
}

func TestBearerExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	got, ok := BearerExpiry("Bearer " + jwtWith(t, map[string]any{"exp": exp}))
	if !ok || got.Unix() != exp {
		t.Fatalf("expiry = %v, %v, want %d", got, ok, exp)
	}
	for name, h := range map[string]string{
		"api key":       "ApiKey org:abc",
		"opaque token":  "Bearer abc123",
		"no exp":        "Bearer " + jwtWith(t, map[string]any{"sub": "x"}),
		"bad payload":   "Bearer a.!!!.c",
		"empty":         "",
		"zero exp":      "Bearer " + jwtWith(t, map[string]any{"exp": 0}),
		"too many dots": "Bearer a.b.c.d",
	} {
		if _, ok := BearerExpiry(h); ok {
			t.Errorf("%s: reported an expiry", name)
		}
	}
}

func TestRequestAttentionWireShape(t *testing.T) {
	var path, auth string
	var body map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"a1","kind":"login","state":"pending","expiresAt":42,"deviceCode":"dc","interval":5}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, func() (string, error) { return "ApiKey o:k", nil }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.RequestAttention(context.Background(), "host-1", RequestLogin, ReasonCredentialExpiring, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, "/hosts/host-1/attention") || auth != "ApiKey o:k" {
		t.Fatalf("path %q auth %q", path, auth)
	}
	if body["kind"] != "login" || body["reason"] != "credential_expiring" {
		t.Fatalf("body %v", body)
	}
	if _, has := body["subject"]; has {
		t.Fatal("an empty subject must be left out")
	}
	if got.ID != "a1" || got.DeviceCode != "dc" || got.Interval != 5 || got.State != RequestPending {
		t.Fatalf("grant = %+v", got)
	}
}

func TestHeartbeatResultCarriesAttention(t *testing.T) {
	var r RCHeartbeatResult
	if err := json.Unmarshal([]byte(`{"ok":true,"attention":[{"id":"a1","hostId":"h","kind":"login","state":"approved","createdAt":1,"updatedAt":2,"expiresAt":3}]}`), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Attention) != 1 || r.Attention[0].ID != "a1" || r.Attention[0].State != RequestApproved {
		t.Fatalf("attention = %+v", r.Attention)
	}
	if err := json.Unmarshal([]byte(`{"ok":true}`), &r); err != nil {
		t.Fatal(err)
	}
}
