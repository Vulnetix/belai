package agent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/otel"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// A real turn with telemetry on exports the turn and tool spans, and none of
// the prompt, the tool arguments or the file contents.
func TestTelemetryCarriesNoContent(t *testing.T) {
	var mu sync.Mutex
	var bodies strings.Builder
	col := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies.Write(b)
		mu.Unlock()
	}))
	defer col.Close()
	stop := otel.Start(otel.Config{Endpoint: col.URL, Traces: true, Metrics: true, Interval: time.Hour}, "p")

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "secretname.txt"), []byte("SECRETFILEBODY"), 0o600)
	srv := mockSecurityServer("Read", `{"file_path":"secretname.txt"}`, "SECRETREPLY")
	defer srv.Close()
	sess, err := NewSession(Options{
		Cfg:       run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:    srv.Client(),
		Registry:  tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}),
		Posture:   posture.Defaults(),
		Workdir:   root,
		SessionID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Run(context.Background(), "SECRETPROMPT read the file"); err != nil {
		t.Fatal(err)
	}
	stop()

	mu.Lock()
	all := bodies.String()
	mu.Unlock()
	for _, want := range []string{"belai.turn", "belai.tool_call", `"Read"`, "belai.tool_calls"} {
		if !strings.Contains(all, want) {
			t.Errorf("export lacks %s", want)
		}
	}
	for _, secret := range []string{"SECRETPROMPT", "SECRETFILEBODY", "SECRETREPLY", "secretname"} {
		if strings.Contains(all, secret) {
			t.Errorf("export carried %q", secret)
		}
	}
}
