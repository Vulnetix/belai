package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, url, body string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestChatServerAnswersAndRecords(t *testing.T) {
	s := NewChatServer(t, func(req ChatRequest) string {
		system, _ := req.Text()
		if strings.Contains(system, "classifier") {
			return "SAFE"
		}
		return "hello"
	})

	resp, body := post(t, s.URL+"/v1/chat/completions", `{"messages":[{"role":"system","content":"a classifier"},{"role":"user","content":"hi"}]}`)
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q", ct)
	}
	var env struct {
		Object  string `json:"object"`
		Choices []struct {
			Message      struct{ Content string } `json:"message"`
			FinishReason string                   `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Object != "chat.completion" || len(env.Choices) != 1 || env.Choices[0].Message.Content != "SAFE" || env.Choices[0].FinishReason != "stop" {
		t.Fatalf("envelope %s", body)
	}

	_, body = post(t, s.URL, `{"stream":true,"messages":[{"role":"user","content":"say \"hi\""}]}`)
	if body != "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n" {
		t.Fatalf("stream %q", body)
	}

	reqs := s.Requests()
	if len(reqs) != 2 || reqs[0].Messages[1].Content != "hi" || !reqs[1].Stream || reqs[1].Messages[0].Content != `say "hi"` {
		t.Fatalf("recorded %+v", reqs)
	}
}

func TestChatServerRefusesOtherMethodsAndBadBodies(t *testing.T) {
	s := NewChatServer(t, Reply("x"))
	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status %d", resp.StatusCode)
	}
	if resp, _ := post(t, s.URL, `{"messages":`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body status %d", resp.StatusCode)
	}
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("recorded %d requests", n)
	}
}
