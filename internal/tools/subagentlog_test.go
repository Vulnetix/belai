package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type fakeProcessLog struct {
	logPath string
}

func (f *fakeProcessLog) LogGrep(id, pattern string, maxMatches, context int) (string, error) {
	if f.logPath == "" {
		return "", nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}
	file, err := os.Open(f.logPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var lines []string
	var matchLines []int
	scanner := bufio.NewScanner(file)
	for n := 1; scanner.Scan(); n++ {
		line := scanner.Text()
		lines = append(lines, line)
		if re.MatchString(line) && len(matchLines) < maxMatches {
			matchLines = append(matchLines, n)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}

	emitted := make(map[int]bool)
	var out []string
	for _, m := range matchLines {
		start := m - context
		if start < 1 {
			start = 1
		}
		end := m + context
		if end > len(lines) {
			end = len(lines)
		}
		for ln := start; ln <= end; ln++ {
			if emitted[ln] {
				continue
			}
			emitted[ln] = true
			out = append(out, fmt.Sprintf("%s:%d: %s", id, ln, lines[ln-1]))
		}
	}
	return strings.Join(out, "\n"), nil
}

func (f *fakeProcessLog) LogIDs() []string { return []string{"p1"} }

func makeFakeLog(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p1.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSubAgentLogMatchesWithLineNumbers(t *testing.T) {
	log := makeFakeLog(t, []string{"one", "two", "three", "four"})
	s := &SubAgentLog{Logs: &fakeProcessLog{logPath: log}}
	res, err := s.Execute(context.Background(), map[string]any{
		"process": "p1",
		"pattern": "^t.*",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Content, "p1:2: two") {
		t.Fatalf("expected line 2 match, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "p1:3: three") {
		t.Fatalf("expected line 3 match, got %q", res.Content)
	}
	if strings.Contains(res.Content, "one") || strings.Contains(res.Content, "four") {
		t.Fatalf("did not expect 'one' or 'four', got %q", res.Content)
	}
}

func TestSubAgentLogContext(t *testing.T) {
	log := makeFakeLog(t, []string{"a", "b", "MATCH", "d", "e"})
	s := &SubAgentLog{Logs: &fakeProcessLog{logPath: log}}
	res, err := s.Execute(context.Background(), map[string]any{
		"process": "p1",
		"pattern": "MATCH",
		"context": int64(1),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, want := range []string{"b", "MATCH", "d"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("expected %q in output, got %q", want, res.Content)
		}
	}
	for _, notWant := range []string{"p1:1: a", "p1:5: e"} {
		if strings.Contains(res.Content, notWant) {
			t.Fatalf("did not expect %q, got %q", notWant, res.Content)
		}
	}
}

func TestSubAgentLogMaxMatches(t *testing.T) {
	log := makeFakeLog(t, []string{"x1", "x2", "x3", "x4"})
	s := &SubAgentLog{Logs: &fakeProcessLog{logPath: log}}
	res, err := s.Execute(context.Background(), map[string]any{
		"process":     "p1",
		"pattern":     "x",
		"max_matches": int64(2),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Count(res.Content, "p1:") != 2 {
		t.Fatalf("expected 2 matches, got %q", res.Content)
	}
}

func TestSubAgentLogInvalidRegex(t *testing.T) {
	s := &SubAgentLog{Logs: &fakeProcessLog{}}
	_, err := s.Execute(context.Background(), map[string]any{
		"process": "p1",
		"pattern": "(?P<bad",
	})
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

func TestSubAgentLogCappedAt64KiB(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, strings.Repeat("x", 700)+" match")
	}
	log := makeFakeLog(t, lines)
	s := &SubAgentLog{Logs: &fakeProcessLog{logPath: log}}
	res, err := s.Execute(context.Background(), map[string]any{
		"process": "p1",
		"pattern": "match",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(res.Content) > 66*1024 {
		t.Fatalf("result too large: %d bytes", len(res.Content))
	}
	if !strings.Contains(res.Content, "truncated at") {
		t.Fatalf("expected truncation notice, got %q", res.Content)
	}
}

func TestSubAgentLogMissingArgs(t *testing.T) {
	s := &SubAgentLog{Logs: &fakeProcessLog{}}
	if _, err := s.Execute(context.Background(), map[string]any{"process": "p1"}); err == nil {
		t.Fatal("expected error for missing pattern")
	}
	if _, err := s.Execute(context.Background(), map[string]any{"pattern": "x"}); err == nil {
		t.Fatal("expected error for missing process")
	}
}

func TestSubAgentLogStreamsLargeLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write 20 MiB total with targeted lines near the end.
	for i := 0; i < 20*1024; i++ {
		line := strings.Repeat("a", 1023) + "\n"
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.WriteString("TARGET LINE\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	flog := &fakeProcessLog{logPath: path}
	s := &SubAgentLog{Logs: flog}
	res, err := s.Execute(context.Background(), map[string]any{
		"process": "p1",
		"pattern": "TARGET LINE",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(res.Content, "TARGET LINE") {
		t.Fatalf("expected target line, got %q", res.Content)
	}
}
