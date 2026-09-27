package nonce

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestNegativeCachePersistsUnsupportedEndpoints(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	SetNegativeCacheFile(filepath.Join(t.TempDir(), "neg.json"))
	defer SetNegativeCacheFile("")
	if _, err := FetchNonces(srv.Client(), srv.URL, ""); err != ErrUnsupported {
		t.Fatalf("400 err = %v, want ErrUnsupported", err)
	}
	unsupportedURLs.Delete(srv.URL) // a new process: only the disk remembers
	if _, err := FetchNonces(srv.Client(), srv.URL, ""); err != ErrUnsupported || hits != 1 {
		t.Fatalf("second fetch err=%v hits=%d, want the disk verdict and no request", err, hits)
	}
}
