package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/testutil"
)

func TestLengthStopReasonRefusesTools(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		count := requests
		mu.Unlock()
		// First call is nonce pool seeding; return empty success.
		if count == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
			return
		}

		msgs := decodeMessages(r.Body)

		// Security classifier.
		for _, m := range msgs {
			if m.Role == "system" && strings.Contains(m.Content, "security classifier") {
				testutil.WriteChat(w, "SAFE")
				return
			}
			if m.Role == "system" && strings.Contains(m.Content, "operating-mode") {
				testutil.WriteChat(w, "AGENT")
				return
			}
		}

		// If a tool result is already present, finish with plain text.
		for _, m := range msgs {
			if m.Role == "tool" {
				testutil.WriteChat(w, "done")
				return
			}
		}

		// Model turn: emit one tool call with stop_reason length.
		writeLengthRepairJSON(w, "Read", "{}")
	}))
	defer srv.Close()

	root := t.TempDir()
	sess := testSessionWithRead(t, root, posture.Defaults(), srv)

	var results []string
	res, err := sess.run(context.Background(), []run.Turn{}, TurnInput{Prompt: "read the file"}, false, func(e Event) {
		if e.Kind == EventToolResultKind {
			results = append(results, e.ToolResult)
		}
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || !strings.Contains(results[0], "truncated") {
		t.Fatalf("expected truncated tool result, got %v", results)
	}
	_ = res
}
