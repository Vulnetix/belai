package run

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// captureUsage registers an observer that keeps the events for model only, so
// a concurrently running test's calls cannot leak in.
func captureUsage(t *testing.T, model string) func() []UsageEvent {
	t.Helper()
	var mu sync.Mutex
	var got []UsageEvent
	cancel := SetUsageObserver(func(ev UsageEvent) {
		if ev.Model != model {
			return
		}
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	t.Cleanup(cancel)
	return func() []UsageEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]UsageEvent(nil), got...)
	}
}

func chatServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// R3: a non-streaming call — the path every classifier and role-manager call
// takes — reports once, under the provider and model that served it.
func TestBudgetRule3_NonStreamingCallReportsServingModel(t *testing.T) {
	events := captureUsage(t, "budget-r3-plain")
	srv := chatServer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}}`)
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r3-plain"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "sys", "hi", srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	got := events()
	if len(got) != 1 {
		t.Fatalf("events = %+v, want exactly one", got)
	}
	if got[0].Provider != "openai" || got[0].Model != "budget-r3-plain" || got[0].Tokens != 150 || got[0].Estimated {
		t.Fatalf("event = %+v, want openai/budget-r3-plain 150 reported", got[0])
	}
}

// R3: a streaming main-turn call reports once, when the stream completes.
func TestBudgetRule3_StreamingCallReportsOnce(t *testing.T) {
	events := captureUsage(t, "budget-r3-stream")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, word := range []string{"hello", " world"} {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": word}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			fl.Flush()
		}
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":40,"completion_tokens":2,"total_tokens":42}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r3-stream"}
	ch, err := StreamTurnsWithTools(context.Background(), cfg, "sys", []Turn{{Role: "user", Content: "hi"}}, srv.Client(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	got := events()
	if len(got) != 1 || got[0].Tokens != 42 || got[0].Estimated {
		t.Fatalf("events = %+v, want one reported event of 42 tokens", got)
	}
}

// R4: the count is the provider's total, prompt plus completion; a provider
// that sends only the components (Anthropic) is summed.
func TestBudgetRule4_TotalIsPromptPlusCompletion(t *testing.T) {
	events := captureUsage(t, "budget-r4")
	srv := chatServer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok","reasoning_content":"thinking"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":250,"total_tokens":1250}}`)
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r4"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "sys", "hi", srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	if got := events(); len(got) != 1 || got[0].Tokens != 1250 {
		t.Fatalf("events = %+v, want 1250 (prompt 1000 + completion 250)", got)
	}
	if n, est := callTokens("", nil, Assistant{Usage: nil}); !est || n <= 0 {
		t.Fatalf("callTokens without usage = %d, %v; want a positive estimate", n, est)
	}
}

// E5: a provider that reports no usage is estimated and flagged.
func TestBudgetEdge5_EstimatedWhenProviderReportsNoUsage(t *testing.T) {
	events := captureUsage(t, "budget-e5")
	reply := "a reply of forty characters exactly okay"
	srv := chatServer(t, `{"choices":[{"index":0,"message":{"role":"assistant","content":"`+reply+`"},"finish_reason":"stop"}]}`)
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-e5"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "system prompt", "user prompt", srv.Client(), nil); err != nil {
		t.Fatal(err)
	}
	got := events()
	if len(got) != 1 || !got[0].Estimated || got[0].Tokens <= 0 {
		t.Fatalf("events = %+v, want one estimated, positive event", got)
	}
}

// E6: a call that fails records nothing.
func TestBudgetEdge6_FailedCallRecordsNothing(t *testing.T) {
	events := captureUsage(t, "budget-e6")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`invalid key`))
	}))
	defer srv.Close()
	cfg := Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-e6"}
	if _, err := chatWithRetryAssistant(context.Background(), cfg, "sys", "hi", srv.Client(), nil); err == nil {
		t.Fatal("expected the call to fail")
	}
	if got := events(); len(got) != 0 {
		t.Fatalf("events = %+v, want none for a failed call", got)
	}
}

// The observer slot: a cancel detaches only its own observer, twice is safe,
// and with no observer a call reports to no one.
func TestSetUsageObserverCancelSemantics(t *testing.T) {
	var first, second int
	c1 := SetUsageObserver(func(UsageEvent) { first++ })
	c2 := SetUsageObserver(func(UsageEvent) { second++ })
	c1() // stale cancel: must not detach the second observer
	reportUsage(Config{Model: "x"}, "", nil, Assistant{Text: "abcd"})
	if first != 0 || second != 1 {
		t.Fatalf("first=%d second=%d, want 0 and 1", first, second)
	}
	c2()
	c2()
	reportUsage(Config{Model: "x"}, "", nil, Assistant{Text: "abcd"})
	if second != 1 {
		t.Fatalf("detached observer still called: %d", second)
	}
}
