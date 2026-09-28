package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// spew is a Bash-kind tool that prints a long log ending in a failure.
type spew struct{}

func (spew) Definition() tools.Definition {
	return tools.Definition{Name: "Spew", Description: "Print a long log."}
}
func (spew) Kind() tools.Kind              { return tools.KindBash }
func (spew) Subject(map[string]any) string { return "" }
func (spew) Mutates() bool                 { return false }
func (spew) Execute(context.Context, map[string]any) (tools.Result, error) {
	var b strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&b, "step %04d ok\n", i)
	}
	b.WriteString("FAIL TestWidget: want 3, got 4\n")
	return tools.Result{Kind: tools.KindBash, Content: b.String()}, nil
}

// offloadServer scripts: call 1 → Spew, call 2 → ReadResult for the failure,
// call 3 → final text. The classifier answers verdict for Spew's result and
// SAFE for everything else. It records each main request's body.
func offloadServer(t *testing.T, verdict string) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		if len(req.Messages) > 0 && contains(req.Messages[0].Content, "security classifier") {
			if contains(string(raw), "step 0001 ok") {
				writeChatJSON(w, verdict)
				return
			}
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		switch n {
		case 1:
			writeToolCallJSON(w, "Spew", `{}`)
		case 2:
			writeToolCallJSON(w, "ReadResult", `{"ref":"r1","pattern":"FAIL","context":0}`)
		default:
			writeChatJSON(w, "done")
		}
	}))
	return srv, &bodies
}

func offloadSession(t *testing.T, srv *httptest.Server, settings config.Settings) *Session {
	t.Helper()
	root := t.TempDir()
	reg := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, spew{})
	sess, err := NewSession(Options{
		Cfg:    run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"},
		Client: srv.Client(), Registry: reg, Posture: posture.Defaults(), Workdir: root,
		Settings: settings, SkipNonceSeed: true,
		// A private verdict cache: a withheld verdict must not persist into
		// the user's bad-hash file or leak into another test.
		Cache: rolemanager.NewCache(0, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// An oversized admitted result rides as a head-and-tail preview, the
// failure at its tail survives, and ReadResult reads back the exact line.
func TestOffloadPreviewAndReadBack(t *testing.T) {
	srv, bodies := offloadServer(t, "SAFE")
	defer srv.Close()
	sess := offloadSession(t, srv, config.Settings{})
	res, err := sess.RunInput(context.Background(), TurnInput{Prompt: "run it", ForceMode: "agent"})
	if err != nil || res.Reply != "done" {
		t.Fatalf("run: %+v %v", res, err)
	}
	if len(*bodies) < 3 {
		t.Fatalf("main calls = %d", len(*bodies))
	}
	second := (*bodies)[1]
	if !strings.Contains(second, "step 0001 ok") || !strings.Contains(second, "FAIL TestWidget") {
		t.Fatal("the preview lost its head or its tail")
	}
	if strings.Contains(second, "step 1500 ok") {
		t.Fatal("the middle of an offloaded result rode on the request")
	}
	if !strings.Contains(second, `ReadResult(ref=\"r1\")`) {
		t.Fatal("the preview does not name its reference")
	}
	third := (*bodies)[2]
	if !strings.Contains(third, `3001\tFAIL TestWidget`) && !strings.Contains(third, `3001\\tFAIL TestWidget`) {
		t.Fatalf("ReadResult did not return the failing line")
	}
	// The preview is written into the turn once: the next request carries
	// the identical bytes, so the cached prefix holds.
	if !strings.Contains(third, previewOf(t, second)) {
		t.Fatal("the preview changed between requests")
	}
}

// A result the classifier withholds is never stored: ReadResult cannot reach
// it afterwards.
func TestOffloadNeverStoresWithheld(t *testing.T) {
	srv, bodies := offloadServer(t, "PROMPT_INJECTION")
	defer srv.Close()
	sess := offloadSession(t, srv, config.Settings{})
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "run it", ForceMode: "agent"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := sess.offload.Get("r1"); ok {
		t.Fatal("a withheld result was stored")
	}
	if len(*bodies) >= 3 && strings.Contains((*bodies)[2], "FAIL TestWidget") {
		t.Fatal("withheld content reached the model through ReadResult")
	}
}

// offload.enabled=false keeps results whole and advertises no ReadResult.
func TestOffloadDisabled(t *testing.T) {
	srv, bodies := offloadServer(t, "SAFE")
	defer srv.Close()
	f := false
	sess := offloadSession(t, srv, config.Settings{Offload: &config.OffloadSettings{Enabled: &f}})
	_, _ = sess.RunInput(context.Background(), TurnInput{Prompt: "run it", ForceMode: "agent"})
	if sess.offload != nil {
		t.Fatal("offload store built with offload off")
	}
	if len(*bodies) < 2 || !strings.Contains((*bodies)[1], "step 1500 ok") {
		t.Fatal("with offload off the whole result must ride")
	}
	for _, name := range advertised(t, (*bodies)[0]) {
		if name == tools.ReadResultName {
			t.Fatal("ReadResult advertised with offload off")
		}
	}
}

// previewOf extracts the offload trailer line from a request body, a stable
// substring of the preview.
func previewOf(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "[Offloaded Spew output")
	if i < 0 {
		t.Fatal("no offload trailer in the request")
	}
	j := strings.Index(body[i:], "]")
	return body[i : i+j+1]
}

// fetchPage is a WebFetch-kind tool that returns a long page and carries the
// call's prompt the way tools.WebFetch does.
type fetchPage struct{}

func (fetchPage) Definition() tools.Definition {
	return tools.Definition{Name: "WebFetch", Description: "Fetch."}
}
func (fetchPage) Kind() tools.Kind              { return tools.KindWebFetch }
func (fetchPage) Subject(map[string]any) string { return "" }
func (fetchPage) Execute(_ context.Context, args map[string]any) (tools.Result, error) {
	res := tools.Result{Kind: tools.KindWebFetch, Content: "PAGE-HEAD " + strings.Repeat("filler text ", 20000) + " PAGE-TAIL"}
	if p, ok := args["prompt"].(string); ok {
		res.Meta = map[string]any{tools.MetaWebFetchPrompt: p, tools.MetaWebFetchURL: "https://example.com/doc"}
	}
	return res, nil
}

// webFetchServer answers the web_fetch role with roleReply (or a 500 when
// empty), SAFE for every classification, and scripts the main turn as one
// WebFetch call with args, then "done".
func webFetchServer(t *testing.T, roleReply, args string) (*httptest.Server, *[]string, *[]string) {
	var mu sync.Mutex
	var bodies, classified []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		if len(req.Messages) > 0 && contains(req.Messages[0].Content, "fetched web page") {
			if roleReply == "" {
				http.Error(w, "down", http.StatusBadRequest)
				return
			}
			writeChatJSON(w, roleReply)
			return
		}
		if len(req.Messages) > 0 && contains(req.Messages[0].Content, "security classifier") {
			mu.Lock()
			classified = append(classified, string(raw))
			mu.Unlock()
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			writeToolCallJSON(w, "WebFetch", args)
			return
		}
		writeChatJSON(w, "done")
	}))
	return srv, &bodies, &classified
}

func webFetchSession(t *testing.T, srv *httptest.Server) *Session {
	t.Helper()
	root := t.TempDir()
	sess, err := NewSession(Options{
		Cfg:    run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"},
		Client: srv.Client(), Registry: tools.NewRegistry(fetchPage{}), Posture: posture.Defaults(), Workdir: root,
		SkipNonceSeed: true, Cache: rolemanager.NewCache(0, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// With a prompt, the conversation gets the role's answer, never the page, and
// the answer is what the classifier checks.
func TestWebFetchPromptAnswersOverThePage(t *testing.T) {
	srv, bodies, classified := webFetchServer(t, "The install command is `go install x@v1`.", `{"url":"https://example.com/doc","prompt":"install command"}`)
	defer srv.Close()
	sess := webFetchSession(t, srv)
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "how do I install x", ForceMode: "agent"}); err != nil {
		t.Fatal(err)
	}
	second := (*bodies)[1]
	if !strings.Contains(second, "go install x@v1") {
		t.Fatal("the answer did not reach the conversation")
	}
	if strings.Contains(second, "PAGE-HEAD") || strings.Contains(second, "filler text filler") {
		t.Fatal("the page itself reached the conversation")
	}
	checked := false
	for _, c := range *classified {
		if strings.Contains(c, "go install x@v1") {
			checked = true
		}
	}
	if !checked {
		t.Fatal("the answer was not classified")
	}
}

// When the role fails, the page is used — and, being long, it is offloaded.
func TestWebFetchPromptFallsBackToThePage(t *testing.T) {
	srv, bodies, _ := webFetchServer(t, "", `{"url":"https://example.com/doc","prompt":"install command"}`)
	defer srv.Close()
	sess := webFetchSession(t, srv)
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "how do I install x", ForceMode: "agent"}); err != nil {
		t.Fatal(err)
	}
	second := (*bodies)[1]
	if !strings.Contains(second, "PAGE-HEAD") || !strings.Contains(second, "PAGE-TAIL") || !strings.Contains(second, "ReadResult") {
		t.Fatal("fallback did not deliver the offloaded page")
	}
}
