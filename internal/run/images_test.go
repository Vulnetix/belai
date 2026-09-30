package run

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/rolemanager"
)

func imgTurn(id, text string) Turn {
	return Turn{
		Role: "tool", Content: text, ToolCallID: id, ToolName: "Screenshot",
		Attachments: []Attachment{{Kind: AttachmentImage, MediaType: "image/png", Data: []byte("PNGDATA-" + id)}},
	}
}

func assistantCall(ids ...string) Turn {
	t := Turn{Role: "assistant"}
	for _, id := range ids {
		t.ToolCalls = append(t.ToolCalls, rolemanager.ToolCall{ID: id, Name: "Screenshot"})
	}
	return t
}

func TestPrepareToolImagesKeepsOnlyTheNewest(t *testing.T) {
	turns := []Turn{{Role: "user", Content: "go"}, assistantCall("a"), imgTurn("a", "one"), assistantCall("b"), imgTurn("b", "two")}
	got := prepareImages(turns, true)
	if len(got[2].Attachments) != 0 || !strings.Contains(got[2].Content, "earlier image is not sent again") {
		t.Fatalf("older image kept: %+v", got[2])
	}
	if len(got[4].Attachments) != 1 || got[4].Content != "two" {
		t.Fatalf("newest image lost: %+v", got[4])
	}
	if len(turns[2].Attachments) != 1 || turns[2].Content != "one" {
		t.Fatal("the caller's conversation was modified")
	}
}

func TestPrepareToolImagesTextOnlyModel(t *testing.T) {
	got := prepareImages([]Turn{assistantCall("a"), imgTurn("a", "shot")}, false)
	if len(got[1].Attachments) != 0 || !strings.Contains(got[1].Content, "does not accept image input") {
		t.Fatalf("text-only model kept the image: %+v", got[1])
	}
}

func TestPrepareToolImagesNoImagesIsIdentity(t *testing.T) {
	in := []Turn{{Role: "user", Content: "x"}, {Role: "tool", Content: "y"}}
	out := prepareImages(in, false)
	if &in[0] != &out[0] {
		t.Fatal("turns without images should be returned as they were")
	}
}

func TestAnthropicToolResultCarriesImage(t *testing.T) {
	msgs := buildAnthropicMessages([]Turn{assistantCall("a"), imgTurn("a", "captured 10x10")}, "")
	b, err := json.Marshal(msgs[1])
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Role    string `json:"role"`
		Content []struct {
			Type      string `json:"type"`
			ToolUseID string `json:"tool_use_id"`
			Content   []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	res := got.Content[0]
	if res.Type != "tool_result" || res.ToolUseID != "a" || len(res.Content) != 2 {
		t.Fatalf("shape: %s", b)
	}
	if res.Content[0].Type != "text" || res.Content[0].Text != "captured 10x10" {
		t.Fatalf("text part: %s", b)
	}
	src := res.Content[1]
	if src.Type != "image" || src.Source.Type != "base64" || src.Source.MediaType != "image/png" || src.Source.Data == "" {
		t.Fatalf("image part: %s", b)
	}
}

func TestAnthropicToolResultWithoutImageStaysAString(t *testing.T) {
	msgs := buildAnthropicMessages([]Turn{assistantCall("a"), {Role: "tool", Content: "plain", ToolCallID: "a"}}, "")
	b, _ := json.Marshal(msgs[1])
	if !strings.Contains(string(b), `"content":"plain"`) {
		t.Fatalf("a text result changed shape: %s", b)
	}
}

func TestOpenAIImageFollowsTheWholeToolRun(t *testing.T) {
	turns := []Turn{
		{Role: "user", Content: "look"},
		assistantCall("a", "b"),
		imgTurn("a", "first"),
		{Role: "tool", Content: "second", ToolCallID: "b", ToolName: "Read"},
		{Role: "user", Content: "next"},
	}
	msgs := buildOpenAIMessages("", turns, "")
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m.Role)
	}
	if got := strings.Join(roles, ","); got != "user,assistant,tool,tool,user,user" {
		t.Fatalf("roles = %s (the image message must wait for the tool run to end)", got)
	}
	img := msgs[4]
	if len(img.Images) != 1 || !strings.Contains(img.Content, "Screenshot call above") {
		t.Fatalf("image message = %+v", img)
	}
	b, _ := json.Marshal(img)
	if !strings.Contains(string(b), `"type":"image_url"`) || !strings.Contains(string(b), "data:image/png;base64,") {
		t.Fatalf("wire = %s", b)
	}
}

func TestOpenAIImageAtEndOfConversationIsFlushed(t *testing.T) {
	msgs := buildOpenAIMessages("", []Turn{assistantCall("a"), imgTurn("a", "shot")}, "")
	if last := msgs[len(msgs)-1]; last.Role != "user" || len(last.Images) != 1 {
		t.Fatalf("last = %+v", last)
	}
}

func TestKiroToolImageRidesTheUserMessage(t *testing.T) {
	// Kiro's builder takes the model's image flag from the live catalogue.
	turns := []Turn{{Role: "user", Content: "go"}, assistantCall("a"), imgTurn("a", "shot")}
	req := buildKiroRequest("m", "", turns, nil, "", true)
	cur := req.ConversationState.CurrentMessage.UserInputMessage
	if cur == nil || len(cur.Images) != 1 {
		t.Fatalf("current message = %+v", cur)
	}
	req = buildKiroRequest("m", "", turns, nil, "", false)
	cur = req.ConversationState.CurrentMessage.UserInputMessage
	if len(cur.Images) != 0 || !strings.Contains(cur.Content, kiroImageOmitted) {
		t.Fatalf("text-only model: %+v", cur)
	}
}
