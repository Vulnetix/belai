package nonce

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNewPool(t *testing.T) {
	p := New()
	if p.Available() != 0 || p.Active() != 0 {
		t.Fatalf("new pool not empty")
	}
}

func TestSeedAndAvailable(t *testing.T) {
	p := New()
	if err := p.Seed(5); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if p.Available() != 5 {
		t.Fatalf("available = %d", p.Available())
	}
}

func TestReserveCreatesActive(t *testing.T) {
	p := New()
	if err := p.Seed(2); err != nil {
		t.Fatal(err)
	}
	n, err := p.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if !p.Valid(n) {
		t.Fatal("reserved nonce should be valid")
	}
	if p.Available() != 1 || p.Active() != 1 {
		t.Fatalf("avail=%d active=%d", p.Available(), p.Active())
	}
}

func TestReserveFallback(t *testing.T) {
	p := New()
	n, err := p.Reserve()
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if n == "" {
		t.Fatal("expected nonce")
	}
	if !p.Valid(n) {
		t.Fatal("expected valid")
	}
}

func TestRelease(t *testing.T) {
	p := New()
	_ = p.Seed(1)
	n, _ := p.Reserve()
	if p.Available() != 0 {
		t.Fatal("expected 0 available")
	}
	p.Release(n)
	if p.Available() != 1 {
		t.Fatalf("expected 1 available, got %d", p.Available())
	}
	if p.Valid(n) {
		t.Fatal("released nonce should not be valid")
	}
}

func TestReleaseUnknown(t *testing.T) {
	p := New()
	p.Release("nope") // should not panic
}

func TestRotate(t *testing.T) {
	p := New()
	_ = p.Seed(3)
	n, _ := p.Reserve()
	if err := p.Rotate(2); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if p.Valid(n) {
		t.Fatal("old nonce should be invalid after rotate")
	}
	if p.Available() != 2 || p.Active() != 0 {
		t.Fatalf("avail=%d active=%d", p.Available(), p.Active())
	}
}

func TestMintLengthAndHex(t *testing.T) {
	n, err := mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if len(n) != 32 {
		t.Fatalf("len = %d", len(n))
	}
	_, err = hex.DecodeString(n)
	if err != nil {
		t.Fatalf("not hex: %v", err)
	}
}

func TestNonceURL(t *testing.T) {
	cases := []struct {
		base string
		want string
	}{
		{"https://api.openai.com/v1", "https://api.openai.com/v1/nonces"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/nonces"},
		{"https://x.com/v1/", "https://x.com/v1/nonces"},
	}
	for _, c := range cases {
		if got := NonceURL(c.base); got != c.want {
			t.Fatalf("NonceURL(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestFetchNoncesUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	_, err := FetchNonces(server.Client(), server.URL, "")
	if !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestFetchNoncesSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nonces" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(NonceResponse{Nonces: []string{"abc", "def"}, Count: 2})
	}))
	defer server.Close()
	nonces, err := FetchNonces(server.Client(), server.URL, "key")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(nonces) != 2 {
		t.Fatalf("len = %d", len(nonces))
	}
}

func TestFetchNoncesBadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := FetchNonces(server.Client(), server.URL, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchNoncesInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	_, err := FetchNonces(server.Client(), server.URL, "")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSeedFromProviderUnsupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	p := New()
	if err := p.SeedFromProvider(server.Client(), server.URL, "", 3); err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.Available() != 3 {
		t.Fatalf("avail = %d", p.Available())
	}
}

func TestSeedFromProviderSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(NonceResponse{Nonces: []string{"001", "002"}, Count: 2})
	}))
	defer server.Close()
	p := New()
	if err := p.SeedFromProvider(server.Client(), server.URL, "", 3); err != nil {
		t.Fatalf("err = %v", err)
	}
	if p.Available() != 2 {
		t.Fatalf("avail = %d", p.Available())
	}
}

func TestFetchNoncesUnsupportedIsNegativeCached(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	for i := 0; i < 3; i++ {
		_, err := FetchNonces(server.Client(), server.URL, "")
		if !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("expected unsupported, got %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("nonce endpoint probed %d times, want 1 (negative cached)", got)
	}
}
