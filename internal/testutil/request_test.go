package testutil

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeChatReadsEveryContentShape(t *testing.T) {
	body := `{"model":"m","stream":true,"tools":[{"type":"function"}],"messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"b"}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"Read","arguments":"{\"p\":1}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"result"}]}`
	req, err := DecodeChat(httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "m" || !req.Stream || len(req.Tools) != 1 || len(req.Messages) != 4 {
		t.Fatalf("request %+v", req)
	}
	if got := req.Messages[1].Content; got != "a\nb" {
		t.Fatalf("content parts = %q, want the text parts joined", got)
	}
	asst := req.Messages[2]
	if asst.Content != "" || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_1" ||
		asst.ToolCalls[0].Function.Name != "Read" || string(asst.ToolCalls[0].Function.Arguments) != `"{\"p\":1}"` {
		t.Fatalf("assistant %+v", asst)
	}
	if tool := req.Messages[3]; tool.ToolCallID != "call_1" || tool.Content != "result" {
		t.Fatalf("tool %+v", tool)
	}
	system, all := req.Text()
	if system != "sys" || all != "sys\na\nb\n\nresult\n" {
		t.Fatalf("Text() = %q, %q", system, all)
	}
}

func TestParseChatRejectsMalformedBodies(t *testing.T) {
	if _, err := ParseChat([]byte(`{"messages":`)); err == nil {
		t.Fatal("truncated body decoded")
	}
	if _, err := ParseChat([]byte(`{"messages":[{"role":1}]}`)); err == nil {
		t.Fatal("non-string role decoded")
	}
}
