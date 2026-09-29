package decisionserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/decisions"
)

// TestMain doubles the test binary as a fake llama-server serving /health
// and /v1/models with the alias it was given.
func TestMain(m *testing.M) {
	if os.Getenv("DECISIONSERVER_FAKE") == "1" {
		port, alias := "0", ""
		for i, a := range os.Args {
			switch a {
			case "--port":
				port = os.Args[i+1]
			case "--alias":
				alias = os.Args[i+1]
			}
		}
		if f := os.Getenv("DECISIONSERVER_LAUNCHES"); f != "" {
			fh, _ := os.OpenFile(f, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			_, _ = fh.WriteString("x")
			_ = fh.Close()
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {})
		mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, alias)
		})
		_ = http.ListenAndServe("127.0.0.1:"+port, mux)
		return
	}
	os.Exit(m.Run())
}

func testModel() decisions.LocalModel {
	m, _ := decisions.LocalModelByID("decider-4b")
	return m
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_MODELS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
}

func modelServer(t *testing.T, alias string) (*httptest.Server, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprintf(w, `{"data":[{"id":%q}]}`, alias)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return srv, port
}

func TestEnsureReusesServerWithMatchingAlias(t *testing.T) {
	isolate(t)
	m := testModel()
	_, port := modelServer(t, m.Alias)
	writeState(state{Port: port, Alias: m.Alias})
	h, err := Ensure(context.Background(), m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if h.Owned || h.Port != port {
		t.Fatalf("handle %+v; a reused server must not be owned", h)
	}
	h.Stop() // no-op on a borrowed server
	if BaseURL() != rootURL(port) {
		t.Fatalf("BaseURL %s", BaseURL())
	}
}

func TestEnsureIgnoresForeignServerAndNeedsWeights(t *testing.T) {
	isolate(t)
	m := testModel()
	_, port := modelServer(t, "someone-elses-model")
	writeState(state{Port: port, Alias: m.Alias})
	if _, err := Ensure(context.Background(), m, Options{}); !errors.Is(err, ErrWeightsMissing) {
		t.Fatalf("err = %v, want ErrWeightsMissing (never a download)", err)
	}
}

func fakeLlamaOnPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell wrapper")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec " + strconv.Quote(os.Args[0]) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "llama-server"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DECISIONSERVER_FAKE", "1")
	launches := filepath.Join(t.TempDir(), "launches")
	t.Setenv("DECISIONSERVER_LAUNCHES", launches)
	return launches
}

func TestEnsureLaunchesOnceAcrossRacingCallers(t *testing.T) {
	isolate(t)
	launches := fakeLlamaOnPath(t)
	m := testModel()
	gguf := filepath.Join(t.TempDir(), m.File)
	_ = os.WriteFile(gguf, []byte("gguf"), 0o644)

	var wg sync.WaitGroup
	var owned atomic.Int32
	handles := make([]*Handle, 3)
	for i := range handles {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h, err := Ensure(context.Background(), m, Options{ModelPath: gguf, Deadline: 20 * time.Second})
			if err != nil {
				t.Errorf("ensure %d: %v", i, err)
				return
			}
			if h.Owned {
				owned.Add(1)
			}
			handles[i] = h
		}(i)
	}
	wg.Wait()
	for _, h := range handles {
		defer h.Stop()
	}
	b, _ := os.ReadFile(launches)
	if len(b) != 1 || owned.Load() != 1 {
		t.Fatalf("launched %d times, %d owners; want exactly one", len(b), owned.Load())
	}
	if !Serving(context.Background(), nil, handles[0].BaseURL, m.Alias) {
		t.Fatal("launched server is not serving the alias")
	}
}

func TestEnsureWithoutBinary(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", t.TempDir())
	m := testModel()
	gguf := filepath.Join(t.TempDir(), m.File)
	_ = os.WriteFile(gguf, []byte("gguf"), 0o644)
	if _, err := Ensure(context.Background(), m, Options{ModelPath: gguf}); !errors.Is(err, ErrNoBinary) {
		t.Fatalf("err = %v, want ErrNoBinary", err)
	}
}

func TestSupervisorRecoverIsRateLimited(t *testing.T) {
	isolate(t)
	s := &Supervisor{Model: testModel()}
	s.Recover()
	first := s.last
	s.Recover()
	if s.last != first {
		t.Fatal("a second Recover inside the window must not start another attempt")
	}
}
