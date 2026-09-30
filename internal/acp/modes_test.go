package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/turnlog"
)

func TestSetMode(t *testing.T) {
	s, ed := postEndServer(t, nil)
	s.sessions["a"] = &acpSession{id: "a", mode: modeAuto, always: map[string]bool{}, log: turnlog.New(nil)}
	call := func(params string) error {
		_, err := s.handle(context.Background(), "session/set_mode", json.RawMessage(params))
		return err
	}
	if err := call(`{"sessionId":"a","modeId":"plan"}`); err != nil {
		t.Fatal(err)
	}
	if got := forcedMode(s.sessions["a"].mode); got != "plan" {
		t.Fatalf("forced mode = %q", got)
	}
	if u := waitUpdates(t, ed, 1)[0]; u["sessionUpdate"] != "current_mode_update" || u["currentModeId"] != "plan" {
		t.Fatalf("update = %v", u)
	}
	if err := call(`{"sessionId":"a","modeId":"auto"}`); err != nil || forcedMode(s.sessions["a"].mode) != "" {
		t.Fatalf("auto: %v %q", err, s.sessions["a"].mode)
	}
	for _, bad := range []string{`{"sessionId":"a","modeId":"root"}`, `{"sessionId":"zz","modeId":"plan"}`} {
		if rpc, ok := call(bad).(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("%s accepted", bad)
		}
	}
}

func TestNewSessionOffersModes(t *testing.T) {
	m := modeState(modeAuto)
	if m["currentModeId"] != "auto" || len(m["availableModes"].([]any)) != 4 {
		t.Fatalf("modes = %v", m)
	}
}
