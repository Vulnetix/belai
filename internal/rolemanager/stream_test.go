package rolemanager

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type streamingFake struct {
	pieces []string
	err    error
}

func (s streamingFake) Classify(context.Context, ClassifierPayload) (string, error) {
	return strings.Join(s.pieces, ""), s.err
}

func (s streamingFake) ClassifyStream(ctx context.Context, _ ClassifierPayload, onText func(string)) (string, error) {
	var b strings.Builder
	for _, p := range s.pieces {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		b.WriteString(p)
		if onText != nil {
			onText(p)
		}
	}
	return b.String(), s.err
}

func TestClassifyStreamingUsesAStreamingClassifier(t *testing.T) {
	var got []string
	out, err := ClassifyStreaming(context.Background(), streamingFake{pieces: []string{"Add ", "a ", "retry."}}, ClassifierPayload{}, func(d string) { got = append(got, d) })
	if err != nil || out != "Add a retry." {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if len(got) != 3 || got[0] != "Add " {
		t.Fatalf("pieces = %q, want three", got)
	}
}

func TestClassifyStreamingFallsBackToOnePiece(t *testing.T) {
	var got []string
	c := ClassifierFunc(func(context.Context, ClassifierPayload) (string, error) { return "whole answer", nil })
	out, err := ClassifyStreaming(context.Background(), c, ClassifierPayload{}, func(d string) { got = append(got, d) })
	if err != nil || out != "whole answer" || len(got) != 1 || got[0] != "whole answer" {
		t.Fatalf("out = %q, pieces = %q, err = %v", out, got, err)
	}
	// An error delivers nothing, and a nil callback is fine.
	boom := ClassifierFunc(func(context.Context, ClassifierPayload) (string, error) { return "", errors.New("boom") })
	got = nil
	if _, err := ClassifyStreaming(context.Background(), boom, ClassifierPayload{}, func(d string) { got = append(got, d) }); err == nil || len(got) != 0 {
		t.Fatalf("error case: err = %v, pieces = %q", err, got)
	}
	if _, err := ClassifyStreaming(context.Background(), c, ClassifierPayload{}, nil); err != nil {
		t.Fatal(err)
	}
}
