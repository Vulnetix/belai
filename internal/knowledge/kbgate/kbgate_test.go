package kbgate

import (
	"context"
	"net/http"
	"testing"

	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

type failingTransport struct{ calls int }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.calls++
	return nil, http.ErrHandlerTimeout
}

// TestGuardrailsOffNeverCallsTheClassifier pins the rule AGENTS.md states: the
// level is checked before the classifier, so an ignored verdict sends nothing.
func TestGuardrailsOffNeverCallsTheClassifier(t *testing.T) {
	tr := &failingTransport{}
	g := New(run.Config{}, &http.Client{Transport: tr}, nil, posture.AllIgnore(), tools.KindRead)
	ok, err := g(context.Background(), "any chunk")
	if err != nil || !ok || tr.calls != 0 {
		t.Fatalf("ok=%v err=%v calls=%d", ok, err, tr.calls)
	}
}

func TestNilLevelsIsTheOrdinaryPath(t *testing.T) {
	g := New(run.Config{}, &http.Client{Transport: &failingTransport{}}, nil, nil, tools.KindRemote)
	if ok, err := g(context.Background(), "chunk"); ok || err == nil {
		t.Fatalf("with a classifier that cannot answer a chunk is never admitted: ok=%v err=%v", ok, err)
	}
}

func TestClassifierFailureFailsClosed(t *testing.T) {
	tr := &failingTransport{}
	pol := posture.Policy{posture.ToolResultUnsafe: posture.Enforce}
	g := New(run.Config{Provider: "openai", Model: "x", APIKey: "k"}, &http.Client{Transport: tr}, nil, pol, tools.KindRead)
	ok, err := g(context.Background(), "chunk")
	if ok || err == nil {
		t.Fatalf("an error must not admit: ok=%v err=%v", ok, err)
	}
}
