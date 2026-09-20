package tools

import (
	"context"
	"strings"
	"testing"
)

type fakeProcessControl struct {
	command  string
	restarts int
	errors   bool
}

func (f *fakeProcessControl) RestartProcess(id, command string) error {
	if f.errors {
		return context.Canceled
	}
	f.restarts++
	f.command = command
	return nil
}

func (f *fakeProcessControl) ProcessCommand(id string) (string, bool) {
	if id != "p1" {
		return "", false
	}
	return f.command, true
}

func TestProcessRestartAcceptsSameArgvZero(t *testing.T) {
	ctl := &fakeProcessControl{command: "llama-server --port 18080"}
	p := &ProcessRestart{Ctl: ctl}
	res, err := p.Execute(context.Background(), map[string]any{
		"process": "p1",
		"command": "llama-server --port 18081",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ctl.restarts != 1 {
		t.Fatalf("expected 1 restart, got %d", ctl.restarts)
	}
	if ctl.command != "llama-server --port 18081" {
		t.Fatalf("command = %q, want amended flags", ctl.command)
	}
	if !strings.Contains(res.Content, "restarted") {
		t.Fatalf("expected restarted in result, got %q", res.Content)
	}
}

func TestProcessRestartRefusesDifferentArgvZero(t *testing.T) {
	ctl := &fakeProcessControl{command: "llama-server --port 18080"}
	p := &ProcessRestart{Ctl: ctl}
	res, err := p.Execute(context.Background(), map[string]any{
		"process": "p1",
		"command": "nginx --port 18080",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ctl.restarts != 0 {
		t.Fatal("expected no restart")
	}
	if !strings.Contains(res.Content, "refused") {
		t.Fatalf("expected refusal, got %q", res.Content)
	}
}

func TestProcessRestartReusesOriginal(t *testing.T) {
	ctl := &fakeProcessControl{command: "sleep 30"}
	p := &ProcessRestart{Ctl: ctl}
	_, err := p.Execute(context.Background(), map[string]any{"process": "p1"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if ctl.restarts != 1 || ctl.command != "sleep 30" {
		t.Fatalf("expected reuse of original command, got %d/%q", ctl.restarts, ctl.command)
	}
}

func TestProcessRestartMissingProcess(t *testing.T) {
	ctl := &fakeProcessControl{command: "sleep 30"}
	p := &ProcessRestart{Ctl: ctl}
	_, err := p.Execute(context.Background(), map[string]any{"process": "p2"})
	if err == nil {
		t.Fatal("expected error for unknown process")
	}
}

func TestProcessRestartSubject(t *testing.T) {
	ctl := &fakeProcessControl{command: "llama-server --port 18080"}
	p := &ProcessRestart{Ctl: ctl}
	if got := p.Subject(map[string]any{"process": "p1"}); got != "llama-server --port 18080" {
		t.Fatalf("subject = %q, want original command", got)
	}
	if got := p.Subject(map[string]any{"process": "p1", "command": "llama-server --port 18081"}); got != "llama-server --port 18081" {
		t.Fatalf("subject = %q, want amended command", got)
	}
}
