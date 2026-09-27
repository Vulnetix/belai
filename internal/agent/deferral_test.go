package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// frob is a non-core tool: it is deferred behind ToolSearch.
type frob struct{ calls *int }

func (frob) Definition() tools.Definition {
	return tools.Definition{Name: "Frob", Description: "Frobnicate a widget."}
}
func (frob) Kind() tools.Kind              { return tools.KindNative }
func (frob) Subject(map[string]any) string { return "" }
func (f frob) Execute(context.Context, map[string]any) (tools.Result, error) {
	*f.calls++
	return tools.Result{Kind: tools.KindNative, Content: "frobbed"}, nil
}

// deferralServer scripts: call 1 → ToolSearch select:Frob, call 2 → Frob,
// call 3 → final text. It records the advertised tool names per call.
func deferralServer(t *testing.T, direct bool) (*httptest.Server, *[][]string) {
	var mu sync.Mutex
	var seen [][]string
	main := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		if len(req.Messages) > 0 && contains(req.Messages[0].Content, "security classifier") {
			writeChatJSON(w, "SAFE")
			return
		}
		mu.Lock()
		seen = append(seen, advertised(t, string(raw)))
		main++
		n := main
		mu.Unlock()
		switch {
		case n == 1 && !direct:
			writeToolCallJSON(w, "ToolSearch", `{"query":"select:Frob"}`)
		case n == 1 || n == 2 && !direct:
			writeToolCallJSON(w, "Frob", `{}`)
		default:
			writeChatJSON(w, "done")
		}
	}))
	return srv, &seen
}

func deferralSession(t *testing.T, srv *httptest.Server, calls *int, deferOn bool) *Session {
	t.Helper()
	root := t.TempDir()
	reg := tools.NewRegistry(&tools.Read{Root: root, MaxBytes: 1024}, frob{calls: calls})
	settings := config.Settings{}
	if !deferOn {
		f := false
		settings.DeferTools = &f
	}
	sess, err := NewSession(Options{
		Cfg:    run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"},
		Client: srv.Client(), Registry: reg, Posture: posture.Defaults(), Workdir: root,
		Settings: settings, SkipNonceSeed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestDeferredToolLoadsOnDemand(t *testing.T) {
	srv, seen := deferralServer(t, false)
	defer srv.Close()
	calls := 0
	sess := deferralSession(t, srv, &calls, true)
	res, err := sess.RunInput(context.Background(), TurnInput{Prompt: "frob it", ForceMode: "agent"})
	if err != nil || res.Reply != "done" {
		t.Fatalf("run: %+v %v", res, err)
	}
	first, second := strings.Join((*seen)[0], ","), strings.Join((*seen)[1], ",")
	if first != "Read,ToolSearch" {
		t.Fatalf("first request advertised %s, want the core tools and ToolSearch", first)
	}
	if second != "Read,ToolSearch,Frob" {
		t.Fatalf("after ToolSearch the request advertised %s", second)
	}
	if calls != 1 {
		t.Fatalf("Frob ran %d times", calls)
	}
}

func TestDeferredToolRunsWithoutLoading(t *testing.T) {
	srv, _ := deferralServer(t, true)
	defer srv.Close()
	calls := 0
	sess := deferralSession(t, srv, &calls, true)
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "frob it", ForceMode: "agent"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("a deferred tool called by name did not run")
	}
}

func TestDeferToolsOffAdvertisesEverything(t *testing.T) {
	srv, seen := deferralServer(t, true)
	defer srv.Close()
	calls := 0
	sess := deferralSession(t, srv, &calls, false)
	if _, err := sess.RunInput(context.Background(), TurnInput{Prompt: "frob it", ForceMode: "agent"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join((*seen)[0], ","); got != "Read,Frob" {
		t.Fatalf("defer_tools=false advertised %s", got)
	}
}
