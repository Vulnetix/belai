package run

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
)

// textSSE serves an Anthropic-shaped stream that writes the given pieces and
// records the request body.
func textSSE(t *testing.T, pieces []string, body *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if body != nil {
			*body = string(b)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		evs := []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text"}}`}
		for _, p := range pieces {
			evs = append(evs, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, p))
		}
		evs = append(evs, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, `{"type":"message_stop"}`)
		for _, ev := range evs {
			fmt.Fprintf(w, "data: %s\n\n", ev)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatClassifierStreamsTheAnswerAsItIsWritten(t *testing.T) {
	var body string
	srv := textSSE(t, []string{"Add ", "a retry", " to the fetch."}, &body)
	c := classifierFromConfig(Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-haiku-4-5"}, srv.Client(), nil)
	sc, ok := c.(rolemanager.StreamClassifier)
	if !ok {
		t.Fatal("the chat classifier does not stream")
	}
	var pieces []string
	out, err := sc.ClassifyStream(context.Background(), rolemanager.BuildVoiceCleanupPayload("um add a retry"), func(d string) { pieces = append(pieces, d) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "Add a retry to the fetch." || len(pieces) != 3 || pieces[0] != "Add " {
		t.Fatalf("out = %q, pieces = %q", out, pieces)
	}
	if !strings.Contains(body, `"stream":true`) || !strings.Contains(body, "Transcript:") {
		t.Fatalf("the request was not a streaming voice_cleanup call: %s", body)
	}
}

func TestVoiceCleanupStreamsEndToEnd(t *testing.T) {
	srv := textSSE(t, []string{"Run ", "the tests."}, nil)
	c := classifierFromConfig(Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "claude-haiku-4-5"}, srv.Client(), nil)
	var seen []string
	got, err := rolemanager.CleanVoiceStream(context.Background(), c, "uh run the tests", func(s string) { seen = append(seen, s) })
	if err != nil || got != "Run the tests." {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(seen) != 2 || seen[0] != "Run" || seen[1] != "Run the tests." {
		t.Fatalf("the text so far = %q", seen)
	}
}

func TestChatClassifierStreamReportsAProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := classifierFromConfig(Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "m"}, srv.Client(), nil)
	if _, err := rolemanager.ClassifyStreaming(context.Background(), c, rolemanager.ClassifierPayload{System: "s", User: "u"}, nil); err == nil {
		t.Fatal("a 401 did not surface as an error")
	}
}

func TestChatClassifierStreamStopsWhenTheContextIsCancelled(t *testing.T) {
	srv := textSSE(t, []string{"never shown"}, nil)
	c := classifierFromConfig(Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "m"}, srv.Client(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rolemanager.ClassifyStreaming(ctx, c, rolemanager.ClassifierPayload{System: "s", User: "u"}, nil); err == nil {
		t.Fatal("a cancelled context did not stop the call")
	}
}

func TestWrappersPassStreamingThrough(t *testing.T) {
	srv := textSSE(t, []string{"a", "b"}, nil)
	inner := classifierFromConfig(Config{Provider: "anthropic", BaseURL: srv.URL, APIKey: "sk", Model: "m"}, srv.Client(), nil)
	p := rolemanager.ClassifierPayload{System: "s", User: "u", UseCase: rolemanager.UseCaseVoiceCleanup}
	tier := tieredClassifier{main: inner, fast: inner}
	var n int
	out, err := rolemanager.ClassifyStreaming(context.Background(), tier, p, func(string) { n++ })
	if err != nil || out != "ab" || n != 2 {
		t.Fatalf("tiered: out %q pieces %d err %v", out, n, err)
	}
	// A main-only use case goes to the main classifier, still streaming.
	p.UseCase = rolemanager.UseCaseMain
	n = 0
	if out, err := rolemanager.ClassifyStreaming(context.Background(), tier, p, func(string) { n++ }); err != nil || out != "ab" || n != 2 {
		t.Fatalf("tiered main: out %q pieces %d err %v", out, n, err)
	}
	r := &routedClassifier{main: inner, fast: inner, cache: map[string]rolemanager.Classifier{}}
	p.UseCase = rolemanager.UseCaseVoiceCleanup
	n = 0
	if out, err := rolemanager.ClassifyStreaming(context.Background(), r, p, func(string) { n++ }); err != nil || out != "ab" || n != 2 {
		t.Fatalf("routed: out %q pieces %d err %v", out, n, err)
	}
}
