package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFetchRefusesEvasiveURLs pins that the URL policy runs before any
// connection: each form below is a known way to make a check and a connection
// disagree about the destination.
func TestWebFetchRefusesEvasiveURLs(t *testing.T) {
	wf := &WebFetch{}
	for _, u := range []string{
		"http://user:pw@example.com/",
		"http://2130706433/",
		"http://0x7f.0.0.1/",
		"http://127.1/",
		"http://[::ffff:127.0.0.1]/",
		"http://[::ffff:a9fe:a9fe]/",
		"http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/",
		"http://metadata.google.internal/",
		"http://localhost./",
		"https://example.com/%0d%0aHost:%20evil",
		"https://example.com/a b",
		"file:///etc/passwd",
		"ftp://example.com/",
	} {
		_, err := wf.Execute(context.Background(), map[string]any{"url": u})
		if err == nil || !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "invalid url") {
			t.Errorf("Execute(%q) = %v, want a URL rejection", u, err)
		}
	}
}

// TestWebFetchRedirectToInternalHostRefused pins the redirect guard: a public
// page cannot bounce the fetch to an internal name or address.
func TestWebFetchRedirectToInternalHostRefused(t *testing.T) {
	hops := 0
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hops++
		rec := httptest.NewRecorder()
		rec.Header().Set("Location", "http://169.254.169.254/latest/")
		rec.WriteHeader(http.StatusFound)
		return rec.Result(), nil
	})
	wf := &WebFetch{Client: &http.Client{Transport: rt}}
	_, err := wf.Execute(context.Background(), map[string]any{"url": "http://93.184.216.34/"})
	if err == nil || !strings.Contains(err.Error(), "redirect rejected") {
		t.Fatalf("err = %v, want a redirect rejection", err)
	}
	if hops != 1 {
		t.Fatalf("hops = %d, want the redirect target never requested", hops)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
