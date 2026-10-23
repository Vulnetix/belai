package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A2A messaging end to end (docs/a2a.md): two headless runs on one host and
// one repository. A edits a file and broadcasts it; B hears the broadcast as
// an a2a_broadcast_in entry and a directive, sends A an A2AMessage and acks
// the broadcast with ignore; A answers in a side-turn while its own turn is
// still running; B receives the reply as a2a_reply_in tied to its question.
// No Vulnetix login: same-host messaging needs none.

// a2aChat is one chat request's messages and tools.
type a2aChat struct {
	Messages []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"messages"`
	Tools []json.RawMessage `json:"tools"`
}

func (c a2aChat) text() (system, all string) {
	var b strings.Builder
	for _, m := range c.Messages {
		s := fmt.Sprint(m.Content)
		if m.Role == "system" {
			system = s
		}
		b.WriteString(s)
		b.WriteString("\n")
	}
	return system, b.String()
}

// a2aMock is a provider that answers the classifiers and hands each chat
// request to script.
func a2aMock(t *testing.T, script func(all string) (tool string, args map[string]any, text string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req a2aChat
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, all := req.text()
		switch {
		case strings.Contains(system, "security classifier"):
			writeChat(w, "SAFE")
			return
		case strings.Contains(system, "operating-mode classifier"):
			writeChat(w, "AGENT")
			return
		case !strings.Contains(system, `nonce="`):
			// Any other role-manager call (naming, evaluators): only the
			// agent's own turns carry a sealed system prompt.
			writeChat(w, "SAFE")
			return
		}
		tool, args, text := script(all)
		if tool != "" {
			writeToolCallChat(w, tool, args, text)
			return
		}
		writeChat(w, text)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// a2aEntries reads every a2a_* entry of the sessions under home.
func a2aEntries(t *testing.T, home string) []map[string]any {
	t.Helper()
	var out []map[string]any
	_ = filepath.Walk(filepath.Join(home, "sessions"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			var e map[string]any
			if json.Unmarshal(sc.Bytes(), &e) == nil {
				if typ, _ := e["type"].(string); strings.HasPrefix(typ, "a2a_") {
					e["session"] = strings.TrimSuffix(filepath.Base(p), ".jsonl")
					out = append(out, e)
				}
			}
		}
		return nil
	})
	return out
}

func a2aWaitFor(t *testing.T, what string, d time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// a2aRepo is a git checkout with an origin, so both runs share a repo key.
func a2aRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"remote", "add", "origin", "https://github.com/acme/widgets.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("widgets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func startBelai(t *testing.T, home, dir, base string, extraEnv []string, prompt string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(belaiBin, "-trust-dir", "-allow-ask-without-tty", "-no-git-sync", "-mode", "agent", "-provider", "openai", "-model", "test", "-prompt", prompt)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "BELAI_BASE_URL="+base, "OPENAI_API_KEY=test", "BELAI_HOME="+home), extraEnv...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, &out
}

var agentIDInDirective = regexp.MustCompile(`A2A broadcast (a2a-[0-9a-z]{26}) from agent (@[^ ]+) \(id ([0-9a-f-]{36}:[0-9a-f-]{36})`)

func TestA2ATwoHeadlessSessions(t *testing.T) {
	home := t.TempDir()
	dir := a2aRepo(t)

	var aBroadcastSeen, bAsked, aAnswered, bDone atomic.Bool
	hasEntry := func(typ string, pred func(map[string]any) bool) bool {
		for _, e := range a2aEntries(t, home) {
			if e["type"] == typ && (pred == nil || pred(e)) {
				return true
			}
		}
		return false
	}
	block := func(cond *atomic.Bool, d time.Duration) {
		deadline := time.Now().Add(d)
		for !cond.Load() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// A: write a file, then hold its turn open until B is done, answering
	// B's message in a side-turn meanwhile.
	aSrv := a2aMock(t, func(all string) (string, map[string]any, string) {
		switch {
		case strings.Contains(all, "side-turn"):
			aAnswered.Store(true)
			return "", nil, "Yes, I hold widget.go until the tests pass; take docs/."
		case !fileExists(filepath.Join(dir, "widget.go")):
			return "Write", map[string]any{"file_path": "widget.go", "content": "package widgets\n"}, ""
		default:
			block(&bDone, 60*time.Second)
			return "", nil, "A done"
		}
	})
	// B: wait for A's broadcast, then message A and ack the broadcast, then
	// wait for the reply.
	var mu sync.Mutex
	var question string
	bSrv := a2aMock(t, func(all string) (string, map[string]any, string) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(all, "An A2A reply is attached"):
			bDone.Store(true)
			return "", nil, "B done"
		case bAsked.Load():
			block(&aAnswered, 60*time.Second)
			// The reply reaches B's bus within a poll.
			time.Sleep(3 * time.Second)
			return "Glob", map[string]any{"pattern": "*.md"}, ""
		case agentIDInDirective.MatchString(all):
			m := agentIDInDirective.FindStringSubmatch(all)
			bAsked.Store(true)
			question = "Are you editing widget.go?"
			return "A2AMessage", map[string]any{"to": m[2], "text": question}, ""
		default:
			block(&aBroadcastSeen, 60*time.Second)
			time.Sleep(2 * time.Second)
			return "Glob", map[string]any{"pattern": "*.md"}, ""
		}
	})

	bCmd, bOut := startBelai(t, home, dir, bSrv.URL, nil, "review the widgets repository")
	defer func() { _ = bCmd.Process.Kill() }()
	time.Sleep(time.Second)
	aCmd, aOut := startBelai(t, home, dir, aSrv.URL, nil, "add widget.go")
	defer func() { _ = aCmd.Process.Kill() }()

	defer func() {
		if t.Failed() {
			for _, e := range a2aEntries(t, home) {
				t.Logf("entry %v", e)
			}
			t.Logf("widget.go exists: %v", fileExists(filepath.Join(dir, "widget.go")))
			_ = aCmd.Process.Kill()
			_ = bCmd.Process.Kill()
			time.Sleep(500 * time.Millisecond)
			t.Logf("A output: %s", aOut)
			t.Logf("B output: %s", bOut)
		}
	}()
	a2aWaitFor(t, "A's broadcast of widget.go", 60*time.Second, func() bool {
		return hasEntry("a2a_broadcast_out", func(e map[string]any) bool { return strings.Contains(fmt.Sprint(e["content"]), "widget.go") })
	})
	aBroadcastSeen.Store(true)

	errA, errB := make(chan error, 1), make(chan error, 1)
	go func() { errA <- aCmd.Wait() }()
	go func() { errB <- bCmd.Wait() }()
	for name, ch := range map[string]chan error{"A": errA, "B": errB} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s: %v\nA: %s\nB: %s", name, err, aOut, bOut)
			}
		case <-time.After(150 * time.Second):
			t.Fatalf("%s did not finish\nA: %s\nB: %s", name, aOut, bOut)
		}
	}

	entries := a2aEntries(t, home)
	byType := map[string][]map[string]any{}
	for _, e := range entries {
		typ := e["type"].(string)
		byType[typ] = append(byType[typ], e)
	}
	if len(byType["a2a_broadcast_in"]) == 0 {
		t.Fatalf("B recorded no a2a_broadcast_in: %v", entries)
	}
	out := byType["a2a_message_out"]
	in := byType["a2a_message_in"]
	if len(out) == 0 || len(in) == 0 || fmt.Sprint(in[0]["content"]) != question {
		t.Fatalf("message out %v, in %v", out, in)
	}
	qid := out[0]["meta"].(map[string]any)["messageId"]
	replyOut := byType["a2a_reply_out"]
	if len(replyOut) == 0 || !strings.Contains(fmt.Sprint(replyOut[0]["content"]), "widget.go") {
		t.Fatalf("A's side-turn reply %v", replyOut)
	}
	replyIn := byType["a2a_reply_in"]
	if len(replyIn) == 0 || replyIn[0]["meta"].(map[string]any)["referenceMessageId"] != qid {
		t.Fatalf("B's reply is not tied to %v: %v", qid, replyIn)
	}
	if replyIn[0]["session"] != out[0]["session"] {
		t.Fatal("the reply landed in another session than the question")
	}
	// The local audit chain holds facts for every envelope, never text.
	audit, err := os.ReadFile(filepath.Join(home, "a2a", "audit.jsonl"))
	if err != nil || !strings.Contains(string(audit), `"a2a.sent"`) || !strings.Contains(string(audit), `"a2a.received"`) {
		t.Fatalf("local audit: %v %s", err, audit)
	}
	if strings.Contains(string(audit), question) {
		t.Fatal("the audit holds message text")
	}
}

// B acks a broadcast with ignore: the decision is recorded as an a2a_ack in
// its transcript and nothing is sent to A.
func TestA2AAckIgnoreIsRecorded(t *testing.T) {
	home := t.TempDir()
	dir := a2aRepo(t)
	var seen atomic.Bool
	aSrv := a2aMock(t, func(all string) (string, map[string]any, string) {
		if !fileExists(filepath.Join(dir, "widget.go")) {
			return "Write", map[string]any{"file_path": "widget.go", "content": "package widgets\n"}, ""
		}
		deadline := time.Now().Add(60 * time.Second)
		for !seen.Load() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		return "", nil, "A done"
	})
	var acked atomic.Bool
	bSrv := a2aMock(t, func(all string) (string, map[string]any, string) {
		if m := agentIDInDirective.FindStringSubmatch(all); m != nil && !acked.Load() {
			acked.Store(true)
			return "A2AAck", map[string]any{"messageId": m[1], "decision": "ignore", "reason": "I only read docs; no overlap."}, ""
		}
		if acked.Load() {
			seen.Store(true)
			return "", nil, "B done"
		}
		time.Sleep(500 * time.Millisecond)
		return "Glob", map[string]any{"pattern": "*.md"}, ""
	})
	bCmd, bOut := startBelai(t, home, dir, bSrv.URL, nil, "review the widgets repository")
	defer func() { _ = bCmd.Process.Kill() }()
	time.Sleep(time.Second)
	aCmd, aOut := startBelai(t, home, dir, aSrv.URL, nil, "add widget.go")
	defer func() { _ = aCmd.Process.Kill() }()
	done := make(chan error, 2)
	go func() { done <- aCmd.Wait() }()
	go func() { done <- bCmd.Wait() }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("run: %v\nA: %s\nB: %s", err, aOut, bOut)
			}
		case <-time.After(150 * time.Second):
			t.Fatalf("runs did not finish\nA: %s\nB: %s", aOut, bOut)
		}
	}
	var ack map[string]any
	for _, e := range a2aEntries(t, home) {
		if e["type"] == "a2a_ack" {
			ack = e
		}
	}
	if ack == nil || !strings.HasPrefix(fmt.Sprint(ack["content"]), "ignore: I only read docs") {
		t.Fatalf("ack entry %v", ack)
	}
	if m := ack["meta"].(map[string]any); m["direction"] != "out" || m["decision"] != "ignore" {
		t.Fatalf("ack meta %v", m)
	}
}

// With a Vulnetix login the run relays its envelopes; during the
// organisation's A2A blackout it makes no relay call, and the local audit
// still records each envelope (withheld) and the switch.
func TestA2ABlackoutStopsRelayNotTheLocalAudit(t *testing.T) {
	for _, blackout := range []bool{false, true} {
		t.Run(fmt.Sprintf("blackout=%v", blackout), func(t *testing.T) {
			home := t.TempDir()
			dir := a2aRepo(t)
			var relayPosts atomic.Int32
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/a2a") {
					relayPosts.Add(1)
					w.Write([]byte(`{"accepted":1,"blackout":false}`))
					return
				}
				w.Write([]byte(`{}`))
			}))
			defer site.Close()
			if blackout {
				if err := os.MkdirAll(filepath.Join(home, "a2a"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, "a2a", "blackout.json"), []byte(`{"on":true,"at":1}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			srv := a2aMock(t, func(all string) (string, map[string]any, string) {
				if !fileExists(filepath.Join(dir, "widget.go")) {
					return "Write", map[string]any{"file_path": "widget.go", "content": "package widgets\n"}, ""
				}
				// Long enough for the broadcast and an upload.
				time.Sleep(4 * time.Second)
				return "", nil, "done"
			})
			cmd, out := startBelai(t, home, dir, srv.URL, []string{"VULNETIX_WEB_URL=" + site.URL, "VULNETIX_API_KEY=test-key",
				"VULNETIX_ORG_ID=11111111-2222-4333-8444-555555555555", "VULNETIX_API_TOKEN=", "HOME=" + t.TempDir()}, "add widget.go")
			if err := cmd.Wait(); err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			audit, err := os.ReadFile(filepath.Join(home, "a2a", "audit.jsonl"))
			if err != nil {
				t.Fatalf("no local audit: %v", err)
			}
			if blackout {
				if n := relayPosts.Load(); n != 0 {
					t.Fatalf("%d relay calls during a blackout", n)
				}
				if !strings.Contains(string(audit), `"a2a.withheld"`) {
					t.Fatalf("no withheld fact: %s", audit)
				}
				return
			}
			if relayPosts.Load() == 0 {
				t.Fatalf("no relay call with a login\n%s\naudit: %s", out, audit)
			}
		})
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
