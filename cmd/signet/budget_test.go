package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/vulnetix/signet/internal/budget"
	"github.com/vulnetix/signet/internal/config"
	"github.com/vulnetix/signet/internal/run"
)

// R13: a headless run records its usage in the shared ledger, so day and month
// budgets include it.
func TestBudgetRule13_HeadlessRunRecordsUsage(t *testing.T) {
	t.Setenv("SIGNET_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":70,"completion_tokens":7,"total_tokens":77}}`))
	}))
	defer srv.Close()

	done := recordUsage("headless-session", config.Settings{})
	cfg := run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "sk", Model: "budget-r13"}
	if _, err := run.SendTurns(context.Background(), cfg, "sys", []run.Turn{{Role: "user", Content: "hi"}}, srv.Client()); err != nil {
		t.Fatal(err)
	}
	done()

	path, err := budget.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	r, err := budget.Open(path, "headless-session", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	day := config.TokenBudget{Provider: "openai", Model: "budget-r13", Scope: config.BudgetScopeDay, Tokens: 1}
	session := day
	session.Scope = config.BudgetScopeSession
	if got := r.Used(day); got != 77 {
		t.Fatalf("day usage = %d, want 77", got)
	}
	if got := r.Used(session); got != 77 {
		t.Fatalf("session usage = %d, want 77", got)
	}
}
