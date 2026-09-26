package run

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/kiroauth"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/wire"
)

func TestBuildKiroRequestFoldsRoles(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "read go.mod"},
		{Role: "assistant", Content: "", ToolCalls: []rolemanager.ToolCall{{ID: "t1", Name: "Read", RawArgs: `{"file_path":"go.mod"}`}}},
		{Role: "tool", ToolCallID: "t1", ToolName: "Read", Content: "module x"},
		{Role: "assistant", Content: "It is module x."},
		{Role: "user", Content: "thanks"},
		{Role: "user", Content: "and more"},
	}
	tools := []wire.OpenAITool{{Type: "function", Function: wire.OpenAIFunctionDef{Name: "Read", Description: "read a file", Parameters: map[string]any{"type": "object"}}}}
	req := buildKiroRequest("claude-sonnet-4.5", "SYSTEM", turns, tools, "arn:p")

	cs := req.ConversationState
	if req.ProfileArn != "arn:p" || cs.ChatTriggerType != "MANUAL" || cs.ConversationID == "" {
		t.Fatalf("envelope = %+v", req)
	}
	h := cs.History
	if len(h) != 4 {
		t.Fatalf("history len = %d: %+v", len(h), h)
	}
	if u := h[0].UserInputMessage; u == nil || u.Content != "SYSTEM\n\nread go.mod" || u.ModelID != "claude-sonnet-4.5" || u.Origin != wire.KiroOrigin {
		t.Fatalf("history[0] = %+v", h[0])
	}
	a := h[1].AssistantResponseMessage
	if a == nil || a.Content != kiroEmptyAssistant || len(a.ToolUses) != 1 || a.ToolUses[0].ToolUseID != "t1" || a.ToolUses[0].Input["file_path"] != "go.mod" {
		t.Fatalf("history[1] = %+v", h[1])
	}
	u := h[2].UserInputMessage
	if u == nil || u.Content != kiroEmptyUser || u.UserInputMessageContext == nil || len(u.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("history[2] = %+v", h[2])
	}
	if tr := u.UserInputMessageContext.ToolResults[0]; tr.ToolUseID != "t1" || tr.Content[0].Text != "module x" || tr.Status != "success" {
		t.Fatalf("tool result = %+v", tr)
	}
	if h[3].AssistantResponseMessage == nil || h[3].AssistantResponseMessage.Content != "It is module x." {
		t.Fatalf("history[3] = %+v", h[3])
	}
	cur := cs.CurrentMessage.UserInputMessage
	if cur == nil || cur.Content != "thanks\n\nand more" {
		t.Fatalf("current = %+v", cs.CurrentMessage)
	}
	if cur.UserInputMessageContext == nil || len(cur.UserInputMessageContext.Tools) != 1 || cur.UserInputMessageContext.Tools[0].ToolSpecification.Name != "Read" {
		t.Fatalf("tools not on the current message: %+v", cur.UserInputMessageContext)
	}
	// Tools are advertised on the current message only.
	if h[0].UserInputMessage.UserInputMessageContext != nil {
		t.Fatal("tools leaked into history")
	}

	// The id is stable across requests of one conversation.
	again := buildKiroRequest("claude-sonnet-4.5", "SYSTEM", turns[:1], nil, "")
	if again.ConversationState.ConversationID != cs.ConversationID {
		t.Fatal("conversation id changed between requests")
	}
}

func TestBuildKiroRequestEndsOnUser(t *testing.T) {
	req := buildKiroRequest("m", "", []Turn{{Role: "assistant", Content: "hello"}}, nil, "")
	cs := req.ConversationState
	if len(cs.History) != 2 || cs.History[0].UserInputMessage == nil || cs.History[1].AssistantResponseMessage == nil {
		t.Fatalf("history = %+v", cs.History)
	}
	if cs.CurrentMessage.UserInputMessage == nil || cs.CurrentMessage.UserInputMessage.Content != kiroContinue {
		t.Fatalf("current = %+v", cs.CurrentMessage)
	}
}

// kiroTestServers starts an SSO-OIDC mock and a Kiro API mock, and points
// the package refresher at the former for the test.
func kiroTestServers(t *testing.T, api http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"accessToken":"kiro-access","expiresIn":3600}`)
	}))
	t.Cleanup(oidc.Close)
	prev := kiroRefresher
	kiroRefresher = kiroauth.NewRefresher(oidc.Client()).WithBaseURL(oidc.URL)
	t.Cleanup(func() { kiroRefresher = prev })

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	login := kiroauth.Login{RefreshToken: "rt", ClientID: "cid", ClientSecret: "cs", Region: "us-east-1", ProfileARN: "arn:p"}.Encode()
	return srv, login
}

func kiroStreamBody() []byte {
	var body []byte
	for _, part := range []string{"Hel", "lo"} {
		p, _ := json.Marshal(map[string]string{"content": part})
		body = append(body, wire.EncodeKiroEvent("assistantResponseEvent", p)...)
	}
	body = append(body, wire.EncodeKiroEvent("toolUseEvent", []byte(`{"toolUseId":"tu1","name":"Read","input":"{\"file_"}`))...)
	body = append(body, wire.EncodeKiroEvent("toolUseEvent", []byte(`{"toolUseId":"tu1","name":"Read","input":"path\":\"a.go\"}"}`))...)
	body = append(body, wire.EncodeKiroEvent("toolUseEvent", []byte(`{"toolUseId":"tu1","name":"Read","stop":true}`))...)
	// A repeat of the finished tool use is ignored.
	body = append(body, wire.EncodeKiroEvent("toolUseEvent", []byte(`{"toolUseId":"tu1","name":"Read","input":"{}","stop":true}`))...)
	body = append(body, wire.EncodeKiroEvent("meteringEvent", []byte(`{"unit":"credit","usage":0.1}`))...)
	return body
}

func TestStreamKiro(t *testing.T) {
	var gotAuth, gotAccept, gotPath string
	var gotReq wire.KiroRequest
	srv, login := kiroTestServers(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept, gotPath = r.Header.Get("Authorization"), r.Header.Get("Accept"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotReq)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(kiroStreamBody())
	})

	cfg, _ := Prepare("claude-sonnet-4.5", "kiro", EnvSource(func(k string) string {
		if k == "KIRO_LOGIN" {
			return login
		}
		return ""
	}))
	if cfg.Auth != provider.AuthKiro || cfg.BaseURL != "https://q.us-east-1.amazonaws.com" {
		t.Fatalf("Prepare = %+v", cfg)
	}
	cfg.BaseURL = srv.URL

	ch, err := StreamTurnsWithTools(context.Background(), cfg, "SYS", []Turn{{Role: "user", Content: "hi"}}, srv.Client(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var text strings.Builder
	var final *Assistant
	for c := range ch {
		if c.Err != nil {
			t.Fatalf("stream error: %v", c.Err)
		}
		text.WriteString(c.Text)
		if c.Done {
			final = c.Assistant
		}
	}
	if gotAuth != "Bearer kiro-access" || gotAccept != "application/vnd.amazon.eventstream" || gotPath != wire.KiroPath {
		t.Fatalf("request auth=%q accept=%q path=%q", gotAuth, gotAccept, gotPath)
	}
	if gotReq.ProfileArn != "arn:p" || gotReq.ConversationState.CurrentMessage.UserInputMessage == nil ||
		!strings.HasSuffix(gotReq.ConversationState.CurrentMessage.UserInputMessage.Content, "hi") {
		t.Fatalf("request body = %+v", gotReq)
	}
	if text.String() != "Hello" || final == nil || final.Text != "Hello" {
		t.Fatalf("text = %q final = %+v", text.String(), final)
	}
	if len(final.ToolCalls) != 1 || final.ToolCalls[0].ID != "tu1" || final.ToolCalls[0].RawArgs != `{"file_path":"a.go"}` {
		t.Fatalf("tool calls = %+v", final.ToolCalls)
	}
}

func TestSendTurnsKiro(t *testing.T) {
	srv, login := kiroTestServers(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(kiroStreamBody())
	})
	cfg := Config{Provider: "kiro", Auth: provider.AuthKiro, BaseURL: srv.URL, APIKey: login, Model: "claude-haiku-4.5"}
	a, err := SendTurns(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err != nil {
		t.Fatalf("SendTurns: %v", err)
	}
	if a.Text != "Hello" || len(a.ToolCalls) != 1 || a.ToolCalls[0].Args["file_path"] != "a.go" {
		t.Fatalf("assistant = %+v", a)
	}
}

func TestKiroExceptionFrame(t *testing.T) {
	srv, login := kiroTestServers(t, func(w http.ResponseWriter, r *http.Request) {
		frame := wire.EncodeEventFrame(
			[]string{":message-type", ":exception-type"},
			map[string]string{":message-type": "exception", ":exception-type": "ThrottlingException"},
			[]byte(`{"message":"slow\ndown"}`),
		)
		_, _ = w.Write(frame)
	})
	cfg := Config{Provider: "kiro", Auth: provider.AuthKiro, BaseURL: srv.URL, APIKey: login, Model: "auto"}
	_, err := SendTurns(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, srv.Client())
	if err == nil || !strings.Contains(err.Error(), "ThrottlingException: slow down") {
		t.Fatalf("got %v", err)
	}
}

func TestKiroRefusesUnpinnedBaseURL(t *testing.T) {
	_, login := kiroTestServers(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("the API must not be reached")
	})
	cfg := Config{Provider: "kiro", Auth: provider.AuthKiro, BaseURL: "https://evil.example", APIKey: login, Model: "auto"}
	_, err := SendTurns(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("got %v", err)
	}
}

func TestKiroAuthReservedForBuiltin(t *testing.T) {
	_, err := provider.NewFromProfile("mykiro", provider.Profile{BaseURL: "https://q.us-east-1.amazonaws.com", API: wire.SurfaceOpenAIChat, Auth: provider.AuthKiro}, "k")
	if err == nil {
		t.Fatal("a custom profile took the kiro auth style")
	}
}
