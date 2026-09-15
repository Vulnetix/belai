package copilotauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTokenServer(t *testing.T, expiresAt time.Time, token string, delay time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":%q,"expires_at":%d}`, token, expiresAt.Unix())
	}))
	return srv, &calls
}

func newTestExchanger(client *http.Client, endpoint string) *Exchanger {
	return &Exchanger{
		client:   client,
		endpoint: endpoint,
		cache:    map[string]exchangeResult{},
		pending:  map[string]*inFlight{},
	}
}

func TestExchangerCachesUntilExpiry(t *testing.T) {
	srv, calls := newTokenServer(t, time.Now().Add(10*time.Minute), "tok-1", 0)
	defer srv.Close()
	e := newTestExchanger(srv.Client(), srv.URL)

	tok1, err := e.Token(context.Background(), "oauth")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	tok2, err := e.Token(context.Background(), "oauth")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok1.Value != "tok-1" || tok2.Value != "tok-1" {
		t.Fatalf("tokens = %q, %q", tok1.Value, tok2.Value)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("expected 1 exchange, got %d", got)
	}
}

func TestExchangerRefreshesOnMargin(t *testing.T) {
	srv, calls := newTokenServer(t, time.Now().Add(1*time.Minute), "tok-1", 0)
	defer srv.Close()
	e := newTestExchanger(srv.Client(), srv.URL)

	if _, err := e.Token(context.Background(), "oauth"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, err := e.Token(context.Background(), "oauth"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Fatalf("token inside refresh margin should re-exchange, got %d calls", got)
	}
}

func TestExchangerConcurrentCallersShareOneRequest(t *testing.T) {
	srv, calls := newTokenServer(t, time.Now().Add(10*time.Minute), "tok-1", 50*time.Millisecond)
	defer srv.Close()
	e := newTestExchanger(srv.Client(), srv.URL)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Token(context.Background(), "oauth"); err != nil {
				t.Errorf("Token: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("concurrent callers should share one exchange, got %d", got)
	}
}

func TestExchangerRevokedTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	e := newTestExchanger(srv.Client(), srv.URL)

	_, err := e.Token(context.Background(), "oauth")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrRevokedToken) {
		t.Fatalf("error = %v, want ErrRevokedToken", err)
	}
}
