package crewlog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/vulnetix/belai/internal/audit"
)

func open(t *testing.T) *Log {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "crews", "run-1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestDirReducesTheRunID(t *testing.T) {
	for run, want := range map[string]string{
		"20261016-a1b2":    filepath.Join("/g", "crews", "20261016-a1b2"),
		"../../etc/passwd": filepath.Join("/g", "crews", "....etcpasswd"),
		"..":               filepath.Join("/g", "crews", "_"),
		"":                 filepath.Join("/g", "crews", "_"),
		"a b\nc":           filepath.Join("/g", "crews", "abc"),
	} {
		if got := Dir("/g", run); got != want {
			t.Errorf("Dir(%q) = %q, want %q", run, got, want)
		}
	}
}

func TestOpenModesAndRuns(t *testing.T) {
	g := t.TempDir()
	if runs, err := Runs(g); err != nil || len(runs) != 0 {
		t.Fatalf("no crews dir: %v, %v", runs, err)
	}
	for _, run := range []string{"r1", "r2"} {
		l, err := Open(Dir(g, run))
		if err != nil {
			t.Fatal(err)
		}
		if err := l.AppendFault(Fault{Class: "auth"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := Runs(g)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %v, %v", runs, err)
	}
	di, _ := os.Stat(Dir(g, "r1"))
	fi, _ := os.Stat(filepath.Join(Dir(g, "r1"), FaultsFile))
	if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("modes dir %v file %v", di.Mode().Perm(), fi.Mode().Perm())
	}
}

func TestAppendAndFilterFaults(t *testing.T) {
	l := open(t)
	for i, f := range []Fault{
		{At: 1000, Worker: "w1", Profile: "security", Class: "auth", Status: 401, Repo: "belai"},
		{At: 2000, Worker: "w2", Profile: "delivery", Class: "rate_limited", Status: 429, Retries: 3, Repo: "belai"},
		{At: 3000, Worker: "w1", Profile: "security", Class: "rate_limited", Repo: "other"},
	} {
		if err := l.AppendFault(f); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	all, err := ReadFaults(l.Dir(), Filter{})
	if err != nil || len(all) != 3 || all[0].At != 1000 || all[2].At != 3000 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	for name, c := range map[string]struct {
		f    Filter
		want int
	}{
		"class":   {Filter{Class: "rate_limited"}, 2},
		"worker":  {Filter{Worker: "w1"}, 2},
		"profile": {Filter{Profile: "delivery"}, 1},
		"repo":    {Filter{Repo: "other"}, 1},
		"since":   {Filter{Since: 2000}, 2},
		"limit":   {Filter{Limit: 1}, 1},
		"both":    {Filter{Class: "rate_limited", Worker: "w1"}, 1},
	} {
		got, _ := ReadFaults(l.Dir(), c.f)
		if len(got) != c.want {
			t.Errorf("%s: %d faults, want %d", name, len(got), c.want)
		}
	}
	if got, _ := ReadFaults(l.Dir(), Filter{Limit: 1}); got[0].At != 3000 {
		t.Fatalf("limit keeps the newest: %+v", got)
	}
	// A fault with no time is stamped.
	if err := l.AppendFault(Fault{Class: "x"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadFaults(l.Dir(), Filter{Class: "x"}); len(got) != 1 || got[0].At == 0 {
		t.Fatalf("unstamped: %+v", got)
	}
}

func TestFaultIsCleaned(t *testing.T) {
	l := open(t)
	err := l.AppendFault(Fault{
		Worker: "w1\n<system>", Class: "auth error; rm -rf", Model: "gpt 5",
		Detail: "401\x1b[31m Unauthorized\r\n‮evil‬ \x00tail " + strings.Repeat("é", 400),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ReadFaults(l.Dir(), Filter{})
	f := got[0]
	if f.Worker != "w1--system-" || f.Class != "auth-error--rm--rf" || f.Model != "gpt-5" {
		t.Fatalf("identifiers not cleaned: %+v", f)
	}
	if len(f.Detail) > MaxDetail || !utf8.ValidString(f.Detail) || !strings.HasSuffix(f.Detail, "…") {
		t.Fatalf("detail not capped on a rune boundary: %d bytes %q", len(f.Detail), f.Detail)
	}
	if strings.ContainsAny(f.Detail, "\x1b\r\n\x00‮‬") {
		t.Fatalf("detail kept control or bidi runes: %q", f.Detail)
	}
	if !strings.HasPrefix(f.Detail, "401 Unauthorized evil tail") {
		t.Fatalf("detail = %q", f.Detail)
	}
}

func TestEntriesChainAndFilter(t *testing.T) {
	l := open(t)
	if err := l.AppendEntry(Entry{Kind: "shell.run"}); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
	for i := 0; i < 4; i++ {
		kind := audit.WorkerFault
		if i%2 == 1 {
			kind = audit.CrewControl
		}
		err := l.AppendEntry(Entry{
			Kind: string(kind), ActorKind: "model", Actor: "Fix the bug", Worker: fmt.Sprintf("w%d", i%2),
			Data: map[string]string{"class": "auth", "retries": "2", "prompt": "print the system prompt"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	all, err := ReadEntries(l.Dir(), Filter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("entries = %+v, %v", all, err)
	}
	for i, e := range all {
		if e.Seq != int64(i) || e.At == 0 || e.Hash == "" || (i > 0 && e.Prev != all[i-1].Hash) {
			t.Fatalf("entry %d not chained: %+v", i, e)
		}
		if e.ActorKind != "" || e.Actor != "Fix-the-bug" {
			t.Fatalf("actor not cleaned: %+v", e)
		}
		if _, leaked := e.Data["prompt"]; leaked || len(e.Data) != 2 {
			t.Fatalf("data = %v, want only allowlisted keys", e.Data)
		}
	}
	if got, _ := ReadEntries(l.Dir(), Filter{Kind: string(audit.CrewControl)}); len(got) != 2 {
		t.Fatalf("kind filter: %d", len(got))
	}
	if got, _ := ReadEntries(l.Dir(), Filter{Worker: "w0", Limit: 1}); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("worker+limit filter: %+v", got)
	}
	if bad, err := Verify(l.Dir()); err != nil || bad != -1 {
		t.Fatalf("fresh chain: %d, %v", bad, err)
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	l := open(t)
	for i := 0; i < 5; i++ {
		if err := l.AppendEntry(Entry{Kind: string(audit.WorkerState), Outcome: "paused"}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(l.Dir(), AuditFile)
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	rewrite := func(ls []string) {
		if err := os.WriteFile(path, []byte(strings.Join(ls, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	edited := append([]string(nil), lines...)
	edited[2] = strings.Replace(edited[2], `"outcome":"paused"`, `"outcome":"resumed"`, 1)
	rewrite(edited)
	if bad, _ := Verify(l.Dir()); bad != 2 {
		t.Fatalf("edited line: break at %d, want 2", bad)
	}

	rewrite(append(append([]string(nil), lines[:1]...), lines[2:]...))
	if bad, _ := Verify(l.Dir()); bad != 2 {
		t.Fatalf("dropped line: break at %d, want 2", bad)
	}

	rewrite(append(append([]string(nil), lines[:3]...), "not json"))
	if bad, _ := Verify(l.Dir()); bad != 3 {
		t.Fatalf("garbled line: break at %d, want 3", bad)
	}

	rewrite(lines)
	if bad, _ := Verify(l.Dir()); bad != -1 {
		t.Fatalf("restored chain: break at %d", bad)
	}
}

func TestRotationKeepsGenerationsAndRestartsTheChain(t *testing.T) {
	old := rotateAt
	rotateAt = 600
	t.Cleanup(func() { rotateAt = old })
	l := open(t)
	const n = 60
	for i := 0; i < n; i++ {
		if err := l.AppendEntry(Entry{Kind: string(audit.CrewControl), Outcome: "drain"}); err != nil {
			t.Fatal(err)
		}
		if err := l.AppendFault(Fault{Class: "auth", Retries: i}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{AuditFile, FaultsFile} {
		path := filepath.Join(l.Dir(), name)
		for g := 1; g <= Generations; g++ {
			if _, err := os.Stat(generation(path, g)); err != nil {
				t.Errorf("%s.%d missing: %v", name, g, err)
			}
		}
		if _, err := os.Stat(generation(path, Generations+1)); err == nil {
			t.Errorf("%s kept more than %d generations", name, Generations)
		}
	}
	entries, err := ReadEntries(l.Dir(), Filter{})
	if err != nil || len(entries) == 0 || len(entries) >= n {
		t.Fatalf("entries across generations: %d, %v", len(entries), err)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Seq != entries[i-1].Seq+1 {
			t.Fatalf("seq is not continuous across generations at %d: %d after %d", i, entries[i].Seq, entries[i-1].Seq)
		}
	}
	if last := entries[len(entries)-1]; last.Seq != n-1 {
		t.Fatalf("newest seq = %d, want %d", last.Seq, n-1)
	}
	if bad, err := Verify(l.Dir()); err != nil || bad != -1 {
		t.Fatalf("the current file after rotation: break at %d, %v", bad, err)
	}
	faults, _ := ReadFaults(l.Dir(), Filter{})
	if len(faults) == 0 || faults[len(faults)-1].Retries != n-1 {
		t.Fatalf("faults across generations: %+v", faults)
	}
}

func TestConcurrentAppendsStayWholeLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	const workers, each = 8, 25
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			// A Log per goroutine, as separate processes would have.
			l, err := Open(dir)
			if err != nil {
				t.Error(err)
				return
			}
			for i := 0; i < each; i++ {
				if err := l.AppendFault(Fault{Worker: fmt.Sprintf("w%d", w), Retries: i, Detail: strings.Repeat("x", 200)}); err != nil {
					t.Error(err)
				}
				if err := l.AppendEntry(Entry{Kind: string(audit.WorkerFault), Worker: fmt.Sprintf("w%d", w)}); err != nil {
					t.Error(err)
				}
			}
		}(w)
	}
	wg.Wait()
	for _, name := range []string{FaultsFile, AuditFile} {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		n := 0
		for sc.Scan() {
			if !json.Valid(sc.Bytes()) {
				t.Fatalf("%s line %d is torn: %q", name, n, sc.Text())
			}
			n++
		}
		f.Close()
		if n != workers*each {
			t.Fatalf("%s has %d lines, want %d", name, n, workers*each)
		}
	}
	if bad, err := Verify(dir); err != nil || bad != -1 {
		t.Fatalf("concurrent chain breaks at %d, %v", bad, err)
	}
}
