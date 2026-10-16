package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/audit"
	"github.com/vulnetix/belai/internal/crewlog"
)

// crewHome makes a global dir with one run of the security crew holding two
// faults and two audit entries.
func crewHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("BELAI_HOME", home)
	l, err := crewlog.Open(crewlog.Dir(home, "belai-security-20261016"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []crewlog.Fault{
		{At: 1000, Crew: "belai:security", Worker: "security-1a2b", Profile: "security", Item: "K-000001", Class: "auth", Status: 401, Detail: "401 Unauthorized\x1b[2J"},
		{At: 2000, Crew: "belai:security", Worker: "security-3c4d", Profile: "security", Item: "K-000002", Class: "rate_limited", Status: 429, Retries: 3},
	} {
		if err := l.AppendFault(f); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []crewlog.Entry{
		{At: 1000, Kind: string(audit.WorkerFault), Worker: "security-1a2b", Outcome: "failed", Data: map[string]string{"class": "auth"}},
		{At: 3000, Kind: string(audit.CrewControl), ActorKind: audit.ActorHuman, Outcome: "drain"},
	} {
		if err := l.AppendEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func runCrew(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCrewCLI(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestCrewLogPrintsFaults(t *testing.T) {
	crewHome(t)
	code, out, errs := runCrew(t, "log")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errs)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "security-1a2b") || !strings.Contains(lines[0], "401 Unauthorized") ||
		!strings.Contains(lines[1], "rate_limited") {
		t.Fatalf("faults:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatalf("an escape sequence reached the terminal: %q", out)
	}
	_, out, _ = runCrew(t, "log", "-class", "auth", "-json")
	var f crewlog.Fault
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &f); err != nil || f.Item != "K-000001" {
		t.Fatalf("-class -json: %q, %v", out, err)
	}
	_, out, _ = runCrew(t, "log", "-crew", "belai:security", "-worker", "security-3c4d", "-limit", "5")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "K-000002") {
		t.Fatalf("-crew -worker:\n%s", out)
	}
}

func TestCrewLogAuditAndVerify(t *testing.T) {
	home := crewHome(t)
	_, out, _ := runCrew(t, "log", "-audit", "-run", "belai-security-20261016")
	if !strings.Contains(out, "worker.fault") || !strings.Contains(out, "class=auth") || !strings.Contains(out, "crew.control") {
		t.Fatalf("-audit:\n%s", out)
	}
	if code, out, _ := runCrew(t, "log", "-verify"); code != 0 || !strings.Contains(out, "intact") {
		t.Fatalf("-verify on an intact chain: %d %q", code, out)
	}
	path := filepath.Join(crewlog.Dir(home, "belai-security-20261016"), crewlog.AuditFile)
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(data, []byte(`"drain"`), []byte(`"pause"`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runCrew(t, "log", "-verify"); code != 1 || !strings.Contains(out, "broken at seq 1") {
		t.Fatalf("-verify on a tampered chain: %d %q", code, out)
	}
}

func TestCrewLogFollowPrintsNewLinesOnce(t *testing.T) {
	home := crewHome(t)
	old := crewFollowEvery
	crewFollowEvery = 20 * time.Millisecond
	t.Cleanup(func() { crewFollowEvery = old })
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		runCrewCLI(ctx, []string{"log", "-follow"}, &out, &bytes.Buffer{})
	}()
	time.Sleep(60 * time.Millisecond)
	l, err := crewlog.Open(crewlog.Dir(home, "belai-security-20261016"))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.AppendFault(crewlog.Fault{At: 2000, Worker: "security-5e6f", Class: "timeout"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	if n := strings.Count(out.String(), "\n"); n != 3 || !strings.Contains(out.String(), "timeout") {
		t.Fatalf("follow printed %d lines:\n%s", n, out.String())
	}
}

func TestCrewLogErrors(t *testing.T) {
	t.Setenv("BELAI_HOME", t.TempDir())
	if code, _, errs := runCrew(t, "log"); code != 1 || !strings.Contains(errs, "no crew runs") {
		t.Fatalf("no runs: %d %q", code, errs)
	}
	if code, _, _ := runCrew(t, "tail"); code != 2 {
		t.Fatalf("unknown verb: %d", code)
	}
	if code, _, _ := runCrew(t, "log", "-run", "a", "-crew", "b"); code != 2 {
		t.Fatalf("-run with -crew: %d", code)
	}
}
