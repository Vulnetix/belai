package run

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/copilotauth"
	"github.com/vulnetix/belai/internal/provider"
)

func userImgTurn(text string, data string) Turn {
	return Turn{
		Role: "user", Content: text,
		Attachments: []Attachment{{Kind: AttachmentImage, Label: "shot.png", MediaType: "image/png", Data: []byte(data)}},
	}
}

func TestPrepareImagesUserTurnTextOnlyModel(t *testing.T) {
	in := []Turn{userImgTurn("what is this", "PX")}
	got := prepareImages(in, false)
	if len(got[0].Attachments) != 0 || !strings.HasPrefix(got[0].Content, "what is this") || !strings.Contains(got[0].Content, "an image was attached but this model does not accept image input") {
		t.Fatalf("user turn = %+v", got[0])
	}
	if len(in[0].Attachments) != 1 || in[0].Content != "what is this" {
		t.Fatal("the caller's turn was modified")
	}
}

func TestPrepareImagesKeepsTextAttachmentsWhenDroppingImages(t *testing.T) {
	turn := userImgTurn("hi", "PX")
	turn.Attachments = append(turn.Attachments, Attachment{Kind: "file", Label: "@a.go", Body: "package a"})
	got := prepareImages([]Turn{turn}, false)
	if len(got[0].Attachments) != 1 || got[0].Attachments[0].Kind != "file" {
		t.Fatalf("attachments = %+v", got[0].Attachments)
	}
}

func TestPrepareImagesNewestWinsAcrossRoles(t *testing.T) {
	// A user image, then a screenshot taken while working on it: the screenshot
	// is the one that stays.
	a := prepareImages([]Turn{userImgTurn("look", "U"), assistantCall("a"), imgTurn("a", "shot")}, true)
	if len(a[0].Attachments) != 0 || !strings.Contains(a[0].Content, "earlier image is not sent again") || len(a[2].Attachments) != 1 {
		t.Fatalf("screenshot should win: %+v", a)
	}
	// A screenshot, then the user attaches a new image: the user's wins.
	b := prepareImages([]Turn{assistantCall("a"), imgTurn("a", "shot"), userImgTurn("now this", "U")}, true)
	if len(b[1].Attachments) != 0 || len(b[2].Attachments) != 1 {
		t.Fatalf("user image should win: %+v", b)
	}
}

func TestOpenAIUserImageRidesTheMessage(t *testing.T) {
	msgs := buildOpenAIMessages("", []Turn{userImgTurn("describe", "PIXELS")}, "")
	if len(msgs) != 1 || msgs[0].Role != "user" || len(msgs[0].Images) != 1 {
		t.Fatalf("msgs = %+v", msgs)
	}
	b, _ := json.Marshal(msgs[0])
	var got struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if len(got.Content) != 2 || got.Content[0].Text != "describe" || !strings.HasPrefix(got.Content[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("wire = %s", b)
	}
}

func TestOpenAIUserImageOnlyTurn(t *testing.T) {
	msgs := buildOpenAIMessages("", []Turn{userImgTurn("", "P")}, "")
	b, _ := json.Marshal(msgs[0])
	if strings.Contains(string(b), `"type":"text"`) || !strings.Contains(string(b), `"type":"image_url"`) {
		t.Fatalf("an image-only message must not carry an empty text part: %s", b)
	}
}

func TestAnthropicUserImageBlocks(t *testing.T) {
	msgs := buildAnthropicMessages([]Turn{userImgTurn("describe", "PIXELS")}, "")
	b, _ := json.Marshal(msgs[0])
	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if got.Role != "user" || len(got.Content) != 2 || got.Content[0].Type != "image" || got.Content[0].Source.MediaType != "image/png" || got.Content[0].Source.Type != "base64" || got.Content[1].Text != "describe" {
		t.Fatalf("wire = %s", b)
	}
	only := buildAnthropicMessages([]Turn{userImgTurn("", "P")}, "")
	b, _ = json.Marshal(only[0])
	if strings.Contains(string(b), `"type":"text"`) {
		t.Fatalf("an image-only message must not carry an empty text block: %s", b)
	}
	plain := buildAnthropicMessages([]Turn{{Role: "user", Content: "hi"}}, "")
	b, _ = json.Marshal(plain[0])
	if string(b) != `{"role":"user","content":"hi"}` {
		t.Fatalf("a text turn changed shape: %s", b)
	}
}

func TestKiroNamesAnImageItRefuses(t *testing.T) {
	huge := userImgTurn("look", strings.Repeat("x", kiroMaxImageBytes+1))
	req := buildKiroRequest("m", "", []Turn{huge}, nil, "", true)
	cur := req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 || !strings.Contains(cur.Content, kiroImageRefused) {
		t.Fatalf("current = %+v", cur)
	}
	shot := imgTurn("a", "shot")
	shot.Attachments[0].Data = []byte(strings.Repeat("x", kiroMaxImageBytes+1))
	req = buildKiroRequest("m", "", []Turn{{Role: "user", Content: "go"}, assistantCall("a"), shot}, nil, "", true)
	cur = req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 || !strings.Contains(cur.UserInputMessageContext.ToolResults[0].Content[0].Text, kiroImageRefused) {
		t.Fatalf("tool result should name the refused image: %+v", cur.UserInputMessageContext.ToolResults)
	}
}

func TestKiroUserImageRidesTheUserMessage(t *testing.T) {
	req := buildKiroRequest("m", "", []Turn{userImgTurn("look", "PX")}, nil, "", true)
	cur := req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 1 || cur.Images[0].Format != "png" {
		t.Fatalf("current = %+v", cur)
	}
}

func TestAcceptsImagesProfileDeclarationWins(t *testing.T) {
	yes, no := true, false
	if !(Config{Provider: "custom", Model: "my-local-vlm", Vision: &yes}).AcceptsImages() {
		t.Fatal("a declared true was ignored")
	}
	if (Config{Provider: "custom", Model: "claude-opus-5", Vision: &no}).AcceptsImages() {
		t.Fatal("a declared false was ignored")
	}
	if !(Config{Provider: "anthropic", Model: "claude-opus-5"}).AcceptsImages() {
		t.Fatal("the id rule should say yes for a Claude model")
	}
	if (Config{Provider: "custom", Model: "my-local-vlm"}).AcceptsImages() {
		t.Fatal("an unknown id is text-only")
	}
}

func TestResolveCustomTakesTheModelsVisionDeclaration(t *testing.T) {
	prof := provider.Profile{BaseURL: "http://127.0.0.1:1/v1", API: "openai-chat", Models: []string{"a", "b"}, Vision: map[string]bool{"b": true}}
	var cfg Config
	status := Status{Origins: map[string]string{}}
	resolveCustom(&cfg, &status, "mine", prof, fakeSource{})
	if cfg.Model != "a" || cfg.Vision != nil {
		t.Fatalf("model a has no declaration: %+v", cfg)
	}
	cfg = Config{Model: "b"}
	resolveCustom(&cfg, &status, "mine", prof, fakeSource{})
	if cfg.Vision == nil || !*cfg.Vision {
		t.Fatalf("model b is declared as accepting images: %+v", cfg.Vision)
	}
}

func TestImageTokens(t *testing.T) {
	mk := func(w, h int) []byte {
		var b bytes.Buffer
		_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
		return b.Bytes()
	}
	if got := ImageTokens(mk(1000, 750)); got != 1000 {
		t.Fatalf("1000x750 = %d, want 1000", got)
	}
	if got := ImageTokens(mk(1568, 1568)); got != imageTokenCap {
		t.Fatalf("large image = %d, want the cap %d", got, imageTokenCap)
	}
	if got := ImageTokens(mk(8, 8)); got != imageTokenFloor {
		t.Fatalf("tiny image = %d, want the floor %d", got, imageTokenFloor)
	}
	if got := ImageTokens([]byte("not an image")); got != imageTokenCap {
		t.Fatalf("unreadable image = %d, want the cap", got)
	}
}

func TestRequestShapeAndCallTokensCountImages(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1000, 750)))
	turn := Turn{Role: "user", Content: "", Attachments: []Attachment{{Kind: AttachmentImage, MediaType: "image/png", Data: b.Bytes()}}}
	base := requestShape("", []Turn{{Role: "user"}}, 0).History
	if got := requestShape("", []Turn{turn}, 0).History; got-base != 1000 {
		t.Fatalf("history grew by %d, want 1000", got-base)
	}
	plain, _ := callTokens("", []Turn{{Role: "user"}}, Assistant{})
	with, estimated := callTokens("", []Turn{turn}, Assistant{})
	if !estimated || with-plain != 1000 {
		t.Fatalf("estimate grew by %d (estimated=%v), want 1000", with-plain, estimated)
	}
}

func TestClearToolResultLetsGoOfItsImage(t *testing.T) {
	tt := imgTurn("a", "shot")
	if !tt.ClearToolResult() || len(tt.Attachments) != 0 {
		t.Fatalf("turn = %+v", tt)
	}
}

type imgRoundTrip func(*http.Request) (*http.Response, error)

func (f imgRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCopilotVisionHeaderOnlyWithImages(t *testing.T) {
	old := copilotExchanger
	defer func() { copilotExchanger = old }()
	expires := time.Now().Add(time.Hour).Unix()
	copilotExchanger = copilotauth.NewExchanger(&http.Client{Transport: imgRoundTrip(func(r *http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"token": "session", "expires_at": expires})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})})
	cfg, status := Prepare("", "github-copilot", fakeSource{vals: map[string]string{"github-copilot:oauth_token": "gho_x"}})
	if !status.Configured {
		t.Fatalf("missing %v", status.Missing)
	}
	cfg.Model = "gpt-4o"

	req, _, err := buildRequest(context.Background(), cfg, "sys", []Turn{userImgTurn("look", "PX")}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Copilot-Vision-Request"); got != "true" {
		t.Fatalf("header = %q with an image", got)
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.Contains(string(body), "data:image/png;base64,") {
		t.Fatalf("the image did not reach the body: %s", body)
	}

	req, _, err = buildRequest(context.Background(), cfg, "sys", []Turn{{Role: "user", Content: "hi"}}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Copilot-Vision-Request"); got != "" {
		t.Fatalf("header = %q without an image", got)
	}
}

func TestTextOnlyModelNeverGetsImageBytesOnTheWire(t *testing.T) {
	cfg := Config{Provider: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "gpt-3.5-turbo"}
	req, _, err := buildRequest(context.Background(), cfg, "", []Turn{userImgTurn("look", "SECRETPIXELS")}, false, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	if strings.Contains(string(body), "image_url") || strings.Contains(string(body), "U0VDUkVUUElYRUxT") {
		t.Fatalf("image bytes reached a text-only model: %s", body)
	}
	if !strings.Contains(string(body), "does not accept image input") {
		t.Fatalf("the model was not told: %s", body)
	}
}

func TestVisionModelGetsImageBytesOnTheWire(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"openai chat", Config{Provider: "openai", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "gpt-4o"}, `"type":"image_url"`},
		{"anthropic", Config{Provider: "anthropic", BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "claude-opus-5"}, `"type":"image"`},
	} {
		req, _, err := buildRequest(context.Background(), tc.cfg, "", []Turn{userImgTurn("look", "PX")}, false, nil, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), tc.want) {
			t.Errorf("%s: body = %s", tc.name, body)
		}
	}
}
