package httpclient

import (
	"net/http"
	"testing"
)

func TestDefaultClientIsSharedAndTuned(t *testing.T) {
	c := Default()
	if c == nil {
		t.Fatal("Default() = nil")
	}
	if c.Timeout != 0 {
		t.Fatalf("Default client has a blanket Timeout %v, which would kill SSE streams", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatal("Default client transport is not an *http.Transport")
	}
	if tr.MaxIdleConnsPerHost != 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 16", tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, ResponseHeaderTimeout)
	}
	// Same pointer on both calls: connection pooling is shared.
	if Default() != Default() {
		t.Fatal("Default() must return the shared singleton")
	}
}

func TestTransportReturnsFreshCopy(t *testing.T) {
	a := Transport()
	b := Transport()
	if a == b {
		t.Fatal("Transport() must return a fresh copy, not a shared pointer")
	}
	if a.ResponseHeaderTimeout != ResponseHeaderTimeout {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", a.ResponseHeaderTimeout, ResponseHeaderTimeout)
	}
	if a.MaxIdleConnsPerHost != 16 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want 16", a.MaxIdleConnsPerHost)
	}
}
