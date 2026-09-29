package modeltest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/localinfer"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/wire"
)

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("BELAI_HOME", t.TempDir())
	t.Setenv("BELAI_MODELS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HF_HOME", "")
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("VULNETIX_WEB_URL", "http://127.0.0.1:1")
}

func TestRunStopsAtFirstFailureAndRecoversPanics(t *testing.T) {
	var events []EventKind
	rep := Run(context.Background(), []Step{
		{Name: "a", Run: func(context.Context, *State) Outcome { return ok("fine") }},
		{Name: "b", Run: func(context.Context, *State) Outcome { panic("boom") }},
		{Name: "c", Run: func(context.Context, *State) Outcome { t.Fatal("ran after a failure"); return ok("") }},
	}, nil, func(e Event) { events = append(events, e.Kind) })
	if rep.Passed {
		t.Fatal("a crashed step must fail the run")
	}
	f, _ := rep.Failure()
	if f.Name != "b" || !strings.Contains(f.Outcome.Detail, "crashed") {
		t.Fatalf("failure %+v", f)
	}
	if rep.Steps[2].Outcome.Status != StatusSkip {
		t.Fatalf("step after a failure = %s, want skip", rep.Steps[2].Outcome.Status)
	}
	if len(events) != 4 {
		t.Fatalf("events %v", events)
	}
}

func hfStub(t *testing.T, payload []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	sum := sha256.Sum256(payload)
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/models/"):
			fmt.Fprintf(w, `{"siblings":[{"rfilename":"m-Q4_K_M.gguf","size":%d,"lfs":{"sha256":%q,"size":%d}},{"rfilename":"mmproj-m.gguf","size":5}]}`,
				len(payload), hex.EncodeToString(sum[:]), len(payload))
		case strings.Contains(r.URL.Path, "/resolve/main/"):
			downloads.Add(1)
			http.ServeContent(w, r, "m.gguf", timeZero, strings.NewReader(string(payload)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := localinfer.HFBase
	localinfer.HFBase = srv.URL
	t.Cleanup(func() { localinfer.HFBase = old })
	return srv, &downloads
}

func TestEnsureHFWeightsAsksBeforeDownloading(t *testing.T) {
	isolate(t)
	payload := []byte(strings.Repeat("w", 5000))
	srv, downloads := hfStub(t, payload)

	// Declined: nothing is downloaded and the step fails with a retry hint.
	declined := Run(context.Background(), []Step{{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
		return ensureHFWeights(ctx, st, "M", "org/m", "m-Q4_K_M.gguf", localinfer.RemoteFile{})
	}}}, &Env{Client: srv.Client(), Confirm: func(context.Context, DownloadOffer) bool { return false }}, nil)
	if declined.Passed || downloads.Load() != 0 {
		t.Fatalf("declined run passed=%v downloads=%d", declined.Passed, downloads.Load())
	}
	f, _ := declined.Failure()
	if len(f.Outcome.Hints) == 0 || f.Outcome.Hints[0].Action != ActRetry {
		t.Fatalf("declined hints %+v", f.Outcome.Hints)
	}

	// Accepted: the offer carries the real size, progress is reported, and
	// the file is found afterwards without asking again.
	var offer DownloadOffer
	var progress []int64
	rep := Run(context.Background(), []Step{{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
		return ensureHFWeights(ctx, st, "M", "org/m", "m-Q4_K_M.gguf", localinfer.RemoteFile{})
	}}}, &Env{Client: srv.Client(), Confirm: func(_ context.Context, o DownloadOffer) bool { offer = o; return true }},
		func(e Event) {
			if e.Kind == EventProgress {
				progress = append(progress, e.Done)
			}
		})
	if !rep.Passed || offer.Size != int64(len(payload)) || len(progress) == 0 || progress[len(progress)-1] != int64(len(payload)) {
		t.Fatalf("passed=%v offer=%+v progress=%v", rep.Passed, offer, progress)
	}
	asked := false
	again := Run(context.Background(), []Step{{Name: "weights", Run: func(ctx context.Context, st *State) Outcome {
		return ensureHFWeights(ctx, st, "M", "org/m", "m-Q4_K_M.gguf", localinfer.RemoteFile{})
	}}}, &Env{Client: srv.Client(), Confirm: func(context.Context, DownloadOffer) bool { asked = true; return true }}, nil)
	if !again.Passed || asked {
		t.Fatal("a file on disk must not be offered again")
	}
}

func TestSystemOneDiscoversPathAndReportsFix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/systemone" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct {
			Questions map[string]map[string]any `json:"questions"`
			State     map[string]any            `json:"state"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		answers := map[string]any{}
		content, _ := body.State["content"].(string)
		p := 0.02
		if strings.Contains(content, "Ignore all previous") {
			p = 0.97
		}
		for id, q := range body.Questions {
			if q["type"] == "noul" {
				answers[id] = map[string]any{"type": "noul", "noul": p}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"answers": answers})
	}))
	defer srv.Close()
	rep := Run(context.Background(), SystemOneSteps(SystemOneTarget{Name: "home", BaseURL: srv.URL}), &Env{Client: srv.Client()}, nil)
	if !rep.Passed {
		f, _ := rep.Failure()
		t.Fatalf("failed at %s: %s", f.Name, f.Outcome.Detail)
	}
	if rep.Fix == nil || rep.Fix.Path != "/systemone" {
		t.Fatalf("fix = %+v, want the discovered /systemone path", rep.Fix)
	}
}

func TestSystemOneAuthAndDeadServer(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer auth.Close()
	rep := Run(context.Background(), SystemOneSteps(SystemOneTarget{Name: "home", BaseURL: auth.URL}), &Env{Client: auth.Client()}, nil)
	f, _ := rep.Failure()
	if !strings.Contains(f.Outcome.Detail, "none is stored") || f.Outcome.Hints[0].Action != ActProviders {
		t.Fatalf("auth failure %+v", f.Outcome)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	rep = Run(context.Background(), SystemOneSteps(SystemOneTarget{Name: "home", BaseURL: url}), &Env{}, nil)
	f, _ = rep.Failure()
	if !strings.Contains(f.Outcome.Detail, "nothing is listening") {
		t.Fatalf("dead server failure %q", f.Outcome.Detail)
	}

	rep = Run(context.Background(), SystemOneSteps(SystemOneTarget{Name: "home", BaseURL: "http://jev.example.com"}), &Env{}, nil)
	f, _ = rep.Failure()
	if f.Name != "address" {
		t.Fatalf("plain http off loopback must fail the address step, got %s", f.Name)
	}
}

func TestSystemOneWrongPathOnLiveServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	rep := Run(context.Background(), SystemOneSteps(SystemOneTarget{Name: "home", BaseURL: srv.URL}), &Env{Client: srv.Client()}, nil)
	f, _ := rep.Failure()
	if !strings.Contains(f.Outcome.Detail, "no decision endpoint was found") {
		t.Fatalf("detail %q", f.Outcome.Detail)
	}
}

func TestOllamaPullAfterConfirm(t *testing.T) {
	pulled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if !pulled {
				w.WriteHeader(http.StatusNotFound)
			}
		case "/api/pull":
			pulled = true
			fmt.Fprintln(w, `{"status":"pulling","total":100,"completed":40}`)
			fmt.Fprintln(w, `{"status":"pulling","total":100,"completed":100}`)
			fmt.Fprintln(w, `{"status":"success"}`)
		}
	}))
	defer srv.Close()
	var last int64
	rep := Run(context.Background(), []Step{EnsureOllamaModel(srv.URL+"/v1", "qwen3:4b")},
		&Env{Client: srv.Client(), Confirm: func(context.Context, DownloadOffer) bool { return true }},
		func(e Event) {
			if e.Kind == EventProgress {
				last = e.Done
			}
		})
	if !rep.Passed || !pulled || last != 100 {
		t.Fatalf("passed=%v pulled=%v last=%d", rep.Passed, pulled, last)
	}
}

func chatServer(t *testing.T, h func(w http.ResponseWriter, body map[string]any)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		h(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func reply(w http.ResponseWriter, text string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "c", "object": "chat.completion",
		"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": text}, "finish_reason": "stop"}},
	})
}

func chatCfg(url string) run.Config {
	return run.Config{Provider: "local-test", BaseURL: url, APIKey: "k", Model: "m", API: wire.SurfaceOpenAIChat, Auth: "bearer"}
}

func TestChatProbeRetriesWithoutRejectedOptions(t *testing.T) {
	var calls atomic.Int32
	srv := chatServer(t, func(w http.ResponseWriter, body map[string]any) {
		calls.Add(1)
		if _, ok := body["reasoning_effort"]; ok || body["max_tokens"] != nil || body["max_completion_tokens"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: max_tokens"}}`))
			return
		}
		reply(w, "OK")
	})
	rep := Run(context.Background(), ChatSteps(ChatTarget{Role: "agent", Cfg: chatCfg(srv.URL), Configured: true}), &Env{Client: srv.Client()}, nil)
	if !rep.Passed || len(rep.Warnings()) != 1 {
		f, _ := rep.Failure()
		t.Fatalf("passed=%v warnings=%d failure=%+v calls=%d", rep.Passed, len(rep.Warnings()), f, calls.Load())
	}
}

func TestChatProbeMapsAuthAndMissingCredentials(t *testing.T) {
	srv := chatServer(t, func(w http.ResponseWriter, _ map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	rep := Run(context.Background(), ChatSteps(ChatTarget{Role: "agent", Cfg: chatCfg(srv.URL), Configured: true}), &Env{Client: srv.Client()}, nil)
	f, _ := rep.Failure()
	if !strings.Contains(f.Outcome.Detail, "refused the credentials") || f.Outcome.Hints[0].Action != ActProviders {
		t.Fatalf("auth outcome %+v", f.Outcome)
	}
	rep = Run(context.Background(), ChatSteps(ChatTarget{Role: "agent", Cfg: chatCfg(srv.URL), Missing: []string{"api_key"}}), &Env{}, nil)
	f, _ = rep.Failure()
	if f.Name != "agent · credentials" {
		t.Fatalf("missing key must fail before any request, got %s", f.Name)
	}
}

func TestSentinelProbeRejectsProse(t *testing.T) {
	srv := chatServer(t, func(w http.ResponseWriter, _ map[string]any) { reply(w, "The content looks safe to me.") })
	rep := Run(context.Background(), ChatSteps(ChatTarget{Role: "classifier", Cfg: chatCfg(srv.URL), Configured: true, Sentinel: true}), &Env{Client: srv.Client()}, nil)
	f, found := rep.Failure()
	if !found || f.Name != "classifier · sentinel" {
		t.Fatalf("a prose-answering classifier must fail the sentinel step, got %+v", rep.Steps)
	}
	ok := chatServer(t, func(w http.ResponseWriter, _ map[string]any) { reply(w, "SAFE") })
	rep = Run(context.Background(), ChatSteps(ChatTarget{Role: "classifier", Cfg: chatCfg(ok.URL), Configured: true, Sentinel: true}), &Env{Client: ok.Client()}, nil)
	if !rep.Passed {
		f, _ := rep.Failure()
		t.Fatalf("SAFE classifier failed: %+v", f)
	}
}

func TestLaunchFailureHints(t *testing.T) {
	cases := map[localinfer.LogClass]Action{
		localinfer.LogCorrupt: ActRedownload,
		localinfer.LogGPU:     ActCPU,
	}
	for class, want := range cases {
		o := launchFailure(&localinfer.LaunchError{ExitCode: 1, Class: class, Output: "x"})
		found := false
		for _, h := range o.Hints {
			if h.Action == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: hints %+v lack %s", class, o.Hints, want)
		}
	}
	if o := launchFailure(&localinfer.LaunchError{ExitCode: 1, Class: localinfer.LogUpgrade}); !strings.Contains(o.Detail, "architecture") {
		t.Fatalf("upgrade detail %q", o.Detail)
	}
}

func TestHFModelID(t *testing.T) {
	if r, q, ok := hfModelID("unsloth/Qwen3-4B-GGUF:Q8_0"); !ok || r != "unsloth/Qwen3-4B-GGUF" || q != "Q8_0" {
		t.Fatalf("%s %s %v", r, q, ok)
	}
	if _, q, ok := hfModelID("org/repo"); !ok || q != "Q4_K_M" {
		t.Fatal("default quant")
	}
	if _, _, ok := hfModelID("default"); ok {
		t.Fatal("a bare id is not a repo")
	}
}

var timeZero = func() (z time.Time) { return }()
