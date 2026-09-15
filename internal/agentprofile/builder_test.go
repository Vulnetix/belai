package agentprofile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestBuilderSuccessFirstAttempt(t *testing.T) {
	mock := &mockClassifier{replies: []string{
		`{"name":"greeter","description":"greets","system_prompt":"You greet.","mode":"single"}`,
	}}
	b := Builder{Classifier: mock, MaxAttempts: 3}
	p, err := b.Build(context.Background(), "create a greeter agent")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name != "greeter" || mock.calls != 1 {
		t.Fatalf("Name = %q, calls = %d", p.Name, mock.calls)
	}
}

func TestBuilderRetriesOnMalformedJSON(t *testing.T) {
	mock := &mockClassifier{replies: []string{
		`not json`,
		`{"name":"fixer","description":"fixes","system_prompt":"You fix.","mode":"loop"}`,
	}}
	b := Builder{Classifier: mock, MaxAttempts: 3}
	p, err := b.Build(context.Background(), "create a fixer agent")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name != "fixer" || mock.calls != 2 {
		t.Fatalf("Name = %q, calls = %d", p.Name, mock.calls)
	}
	if !strings.Contains(mock.lastPayload.User, "Validation error") {
		t.Fatalf("feedback missing")
	}
}

func TestBuilderRetriesOnValidationError(t *testing.T) {
	mock := &mockClassifier{replies: []string{
		`{"name":"bad","description":"d","system_prompt":"s","mode":"fly"}`,
		`{"name":"good","description":"d","system_prompt":"s","mode":"single"}`,
	}}
	b := Builder{Classifier: mock, MaxAttempts: 3}
	p, err := b.Build(context.Background(), "create an agent")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Name != "good" || mock.calls != 2 {
		t.Fatalf("Name = %q, calls = %d", p.Name, mock.calls)
	}
}

func TestBuilderFailsClosedAfterMaxAttempts(t *testing.T) {
	mock := &mockClassifier{replies: []string{`bad1`, `bad2`, `bad3`}}
	b := Builder{Classifier: mock, MaxAttempts: 3}
	_, err := b.Build(context.Background(), "create an agent")
	if err == nil || !strings.Contains(err.Error(), "failed after 3 attempts") {
		t.Fatalf("expected error, got %v", err)
	}
}

func TestBuilderNilClassifier(t *testing.T) {
	b := Builder{Classifier: nil}
	_, err := b.Build(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "no classifier configured") {
		t.Fatalf("expected 'no classifier configured', got %v", err)
	}
}

func TestParseBuilderReplyStripsFences(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"```json\n{\"name\":\"a\",\"description\":\"d\",\"system_prompt\":\"s\",\"mode\":\"single\"}\n```", "a"},
		{"<thinking>reasoning</thinking>\n{\"name\":\"b\",\"description\":\"d\",\"system_prompt\":\"s\",\"mode\":\"single\"}", "b"},
	}
	for _, c := range cases {
		p, err := parseBuilderReply(c.input)
		if err != nil {
			t.Fatalf("parseBuilderReply(%q): %v", c.input, err)
		}
		if p.Name != c.want {
			t.Fatalf("parseBuilderReply(%q) Name = %q, want %q", c.input, p.Name, c.want)
		}
	}
}

type mockClassifier struct {
	replies     []string
	calls       int
	lastPayload rolemanager.ClassifierPayload
}

func (m *mockClassifier) Classify(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
	m.lastPayload = p
	idx := m.calls
	m.calls++
	if idx < len(m.replies) {
		return m.replies[idx], nil
	}
	return "", errors.New("out of replies")
}
