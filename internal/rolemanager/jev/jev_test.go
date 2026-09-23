package jev

import (
	"context"
	"errors"
	"testing"

	"github.com/vulnetix/signet/internal/rolemanager"
)

func TestParseVerdictValid(t *testing.T) {
	cases := []struct {
		in   string
		want Sentinel
	}{
		{"ALLOW", Allow},
		{"allowed", Allow},
		{"DENY", Deny},
		{"denied", Deny},
		{"INCONCLUSIVE", Inconclusive},
		{"refused", Inconclusive},
		{"<thinking>…</thinking>\nALLOW", Allow},
		{"```\nDENY\n```", Deny},
		{"allowed.", Allow},
	}
	for _, tc := range cases {
		got, err := ParseVerdict(tc.in)
		if err != nil {
			t.Fatalf("ParseVerdict(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseVerdict(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseVerdictMalformed(t *testing.T) {
	bad := []string{
		"",
		"maybe",
		"maybe ALLOW",
		"ALLOW\nDENY", // two different tokens is ambiguous
	}
	for _, in := range bad {
		if _, err := ParseVerdict(in); err == nil {
			t.Fatalf("ParseVerdict(%q) expected error", in)
		}
	}
}

func TestClassifyReturnsVerdict(t *testing.T) {
	llm := rolemanager.ClassifierFunc(func(_ context.Context, p rolemanager.ClassifierPayload) (string, error) {
		if p.Tools != nil || p.Skills != nil || p.Agent != "" {
			t.Fatalf("gate payload must carry no tools/skills/agent: %+v", p)
		}
		return "allowed", nil
	})
	c := New(llm)
	got, err := c.Classify(context.Background(), BuildPayload(`{"name":"bash","args":{"command":"rm -rf /"}}`))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(Allow) {
		t.Fatalf("Classify = %q, want %q", got, Allow)
	}
}

func TestClassifyMalformedIsInconclusive(t *testing.T) {
	llm := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		return "I think this is fine", nil
	})
	c := New(llm)
	got, err := c.Classify(context.Background(), BuildPayload("bash rm -rf /"))
	if err != nil {
		t.Fatalf("Classify must not error on a malformed verdict: %v", err)
	}
	if got != string(Inconclusive) {
		t.Fatalf("Classify = %q, want %q", got, Inconclusive)
	}
}

func TestClassifyRefusedIsInconclusive(t *testing.T) {
	llm := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		return "refused", nil
	})
	c := New(llm)
	got, err := c.Classify(context.Background(), BuildPayload("read secret.txt"))
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got != string(Inconclusive) {
		t.Fatalf("Classify = %q, want %q", got, Inconclusive)
	}
}

func TestClassifyTransportErrorPropagates(t *testing.T) {
	llm := rolemanager.ClassifierFunc(func(context.Context, rolemanager.ClassifierPayload) (string, error) {
		return "", errors.New("upstream down")
	})
	c := New(llm)
	if _, err := c.Classify(context.Background(), BuildPayload("bash ls")); err == nil {
		t.Fatal("Classify must propagate the underlying LLM error")
	}
}

func TestBuildPayloadIsToolless(t *testing.T) {
	p := BuildPayload(`{"name":"read","args":{"path":"a.txt"}}`)
	if p.Tools != nil || p.Skills != nil || p.Agent != "" {
		t.Fatalf("BuildPayload must be tool-less, skill-less, agent-less: %+v", p)
	}
	if p.System == "" || p.User == "" {
		t.Fatalf("BuildPayload must set system and user: %+v", p)
	}
}
