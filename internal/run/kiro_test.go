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
	"github.com/vulnetix/belai/internal/kiromodels"
	"github.com/vulnetix/belai/internal/nonce"
	"github.com/vulnetix/belai/internal/provider"
	"github.com/vulnetix/belai/internal/rolemanager"
	"github.com/vulnetix/belai/internal/transcript"
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
	req := buildKiroRequest("claude-sonnet-4.5", "SYSTEM", turns, tools, "arn:p", false)

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
	again := buildKiroRequest("claude-sonnet-4.5", "SYSTEM", turns[:1], nil, "", false)
	if again.ConversationState.ConversationID != cs.ConversationID {
		t.Fatal("conversation id changed between requests")
	}
}

func TestBuildKiroRequestEndsOnUser(t *testing.T) {
	req := buildKiroRequest("m", "", []Turn{{Role: "assistant", Content: "hello"}}, nil, "", false)
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

	kiromodels.Forget()
	t.Cleanup(kiromodels.Forget)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == kiromodels.Path {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, kiroTestCatalog)
			return
		}
		api(w, r)
	}))
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

// kiroTestCatalog is a ListAvailableModels reply: a Claude model with an
// effort schema, image input and a max_tokens range, and a text-only model
// with no schema.
const kiroTestCatalog = `{"models":[
 {"modelId":"claude-sonnet-4.5","modelName":"Claude Sonnet 4.5","supportedInputTypes":["TEXT","IMAGE"],
  "tokenLimits":{"maxInputTokens":200000,"maxOutputTokens":64000},
  "additionalModelRequestFieldsSchema":{"type":"object","additionalProperties":false,"properties":{
    "output_config":{"type":"object","properties":{"effort":{"type":"string","enum":["low","medium","high"]}}},
    "max_tokens":{"type":"integer","minimum":1024,"maximum":64000}}}},
 {"modelId":"auto","modelName":"Auto","supportedInputTypes":["TEXT"],"tokenLimits":{"maxInputTokens":100000}}
]}`

func TestKiroRequestFieldsFollowTheSchema(t *testing.T) {
	var got []wire.KiroRequest
	srv, login := kiroTestServers(t, func(w http.ResponseWriter, r *http.Request) {
		var req wire.KiroRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req)
		_, _ = w.Write(kiroStreamBody())
	})
	send := func(model, effort string, maxTokens int) wire.KiroRequest {
		t.Helper()
		cfg, _ := Prepare(model, "kiro", EnvSource(func(k string) string {
			if k == "KIRO_LOGIN" {
				return login
			}
			return ""
		}))
		cfg.BaseURL, cfg.Effort, cfg.MaxTokens = srv.URL, effort, maxTokens
		if _, err := SendTurns(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, srv.Client()); err != nil {
			t.Fatalf("SendTurns: %v", err)
		}
		return got[len(got)-1]
	}

	req := send("claude-sonnet-4.5", "high", 999999)
	oc, _ := req.AdditionalModelRequestFields["output_config"].(map[string]any)
	if oc["effort"] != "high" || req.AdditionalModelRequestFields["max_tokens"] != float64(64000) {
		t.Fatalf("fields = %v", req.AdditionalModelRequestFields)
	}
	// An effort outside the model's enum is not sent; max_tokens clamps up.
	req = send("claude-sonnet-4.5", "max", 10)
	if _, ok := req.AdditionalModelRequestFields["output_config"]; ok || req.AdditionalModelRequestFields["max_tokens"] != float64(1024) {
		t.Fatalf("fields = %v", req.AdditionalModelRequestFields)
	}
	// A model with no schema gets no extra fields at all.
	if req = send("auto", "high", 5000); req.AdditionalModelRequestFields != nil {
		t.Fatalf("schema-less model got %v", req.AdditionalModelRequestFields)
	}
	if !ResolveDialect(Config{Provider: "kiro"}).UsesEffort() {
		t.Fatal("kiro should report that effort is sent")
	}
}

func TestBuildKiroRequestImages(t *testing.T) {
	png := Attachment{Kind: AttachmentImage, MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}
	huge := Attachment{Kind: AttachmentImage, MediaType: "image/png", Data: make([]byte, kiroMaxImageBytes+1)}
	bmp := Attachment{Kind: AttachmentImage, MediaType: "image/bmp", Data: []byte{1}}
	turns := []Turn{
		{Role: "user", Content: "first", Attachments: []Attachment{png}},
		{Role: "assistant", Content: "ok"},
		{Role: "user", Content: "look", Attachments: []Attachment{png, huge, bmp, {Kind: "file", Label: "a.go", Body: "x"}}},
	}
	req := buildKiroRequest("m", "", turns, nil, "", true)
	cur := req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 || cur.Images[0].Format != "png" || string(cur.Images[0].Source.Bytes) != "\x89PNG" {
		t.Fatalf("current images = %+v", cur.Images)
	}
	if h := req.ConversationState.History[0].UserInputMessage; len(h.Images) != 0 {
		t.Fatal("history kept an image")
	}
	b, _ := json.Marshal(cur.Images[0])
	if !strings.Contains(string(b), `"bytes":"iVBORw=="`) {
		t.Fatalf("image not base64 on the wire: %s", b)
	}

	req = buildKiroRequest("m", "", turns, nil, "", false)
	cur = req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 || !strings.HasSuffix(cur.Content, kiroImageOmitted) {
		t.Fatalf("text-only model: %+v", cur)
	}
}

func TestEgressKeepsImageBytesOutOfText(t *testing.T) {
	turns := []Turn{{Role: "user", Content: "hi", Attachments: []Attachment{
		{Kind: AttachmentImage, MediaType: "image/png", Label: "shot.png", Data: []byte("RAWBYTES")},
		{Kind: "file", Label: "a.txt", Body: "text body"},
	}}}
	out := egressTurns(turns, nonce.New())
	if strings.Contains(out[0].Content, "RAWBYTES") || strings.Contains(out[0].Content, "shot.png") {
		t.Fatalf("image leaked into text: %q", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "text body") {
		t.Fatal("file attachment lost")
	}
	if len(out[0].Attachments) != 1 || out[0].Attachments[0].Kind != AttachmentImage {
		t.Fatalf("image not carried beside the text: %+v", out[0].Attachments)
	}
}

func TestKiroUsage(t *testing.T) {
	meta := wire.EncodeKiroEvent("metadataEvent", []byte(`{"tokenUsage":{"uncachedInputTokens":100,"cacheReadInputTokens":50,"cacheWriteInputTokens":5,"outputTokens":20,"totalTokens":175}}`))
	pct := wire.EncodeKiroEvent("contextUsageEvent", []byte(`{"contextUsagePercentage":10}`))

	// metadataEvent wins, and a repeated event replaces rather than adds.
	body := append(append(append([]byte{}, kiroStreamBody()...), pct...), append(meta, meta...)...)
	a, err := parseKiro(body, 200, 200000)
	if err != nil || a.Usage == nil || a.Usage.PromptTokens != 155 || a.Usage.CompletionTokens != 20 || a.Usage.TotalTokens != 175 {
		t.Fatalf("usage = %+v, %v", a.Usage, err)
	}
	// Without metadata, the context percentage of the model's input limit.
	a, _ = parseKiro(append(append([]byte{}, kiroStreamBody()...), pct...), 200, 200000)
	if a.Usage == nil || a.Usage.PromptTokens != 20000 {
		t.Fatalf("fallback usage = %+v", a.Usage)
	}
	// Neither known: no usage.
	if a, _ = parseKiro(append(append([]byte{}, kiroStreamBody()...), pct...), 200, 0); a.Usage != nil {
		t.Fatalf("unknown limit gave %+v", a.Usage)
	}

	// The streaming path reports the same on its Done chunk.
	srv, login := kiroTestServers(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write(append(append([]byte{}, kiroStreamBody()...), meta...))
	})
	cfg := Config{Provider: "kiro", Auth: provider.AuthKiro, BaseURL: srv.URL, APIKey: login, Model: "claude-sonnet-4.5"}
	ch, err := StreamTurnsWithTools(context.Background(), cfg, "", []Turn{{Role: "user", Content: "hi"}}, srv.Client(), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var usage *transcript.Usage
	for c := range ch {
		if c.Err != nil {
			t.Fatal(c.Err)
		}
		if c.Done {
			usage = c.Usage
		}
	}
	if usage == nil || usage.PromptTokens != 155 || usage.CompletionTokens != 20 {
		t.Fatalf("stream usage = %+v", usage)
	}
}
