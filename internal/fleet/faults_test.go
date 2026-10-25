package fleet

import (
	"context"
	"errors"
	"testing"
)

func TestEndReason(t *testing.T) {
	if EndReason(nil) != "" {
		t.Error("no error, no reason")
	}
	if got := EndReason(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("deadline = %q", got)
	}
	if got := EndReason(errors.New("provider returned 429 Too Many Requests")); got != "rate_limited" {
		t.Errorf("429 text = %q", got)
	}
	if got := EndReason(errors.New("exit status 2")); got != "error" {
		t.Errorf("plain = %q", got)
	}
}
