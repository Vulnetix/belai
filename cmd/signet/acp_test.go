package main

import (
	"context"
	"strings"
	"testing"
)

// An editor cannot open a session in a directory the user never trusted:
// the trust prompt never runs over ACP.
func TestACPRefusesUntrustedDirectory(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	_, err := buildACPSession(context.Background(), t.TempDir(), "id", "", "")
	if err == nil || !strings.Contains(err.Error(), "not trusted") {
		t.Fatalf("err = %v", err)
	}
}
