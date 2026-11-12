package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/testutil"
)

// replyServer is a mock provider whose main-model replies come from a queue
// (the last one repeats), with the classifier answering SAFE. It records the
// system and user text of each main-model request.
type replyServer struct {
	mu      sync.Mutex
	replies []string
	calls   int
	users   []string
	systems []string
}

func newReplyServer(t *testing.T, replies ...string) (*httptest.Server, *replyServer) {
	t.Helper()
	rs := &replyServer{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var system, all string
		for _, m := range req.Messages {
			if m.Role == "system" {
				system = m.Content
			}
			all += m.Role + ": " + m.Content + "\n"
		}
		rs.mu.Lock()
		defer rs.mu.Unlock()
		switch {
		case strings.Contains(system, "security classifier"):
			testutil.WriteChat(w, "SAFE")
		case strings.Contains(system, "operating-mode classifier"):
			testutil.WriteChat(w, "UNDETERMINED")
		default:
			rs.users = append(rs.users, all)
			rs.systems = append(rs.systems, system)
			i := rs.calls
			if i >= len(rs.replies) {
				i = len(rs.replies) - 1
			}
			rs.calls++
			testutil.WriteChat(w, rs.replies[i])
		}
	}))
	return srv, rs
}

// runBelaiStdin is runBelaiHome with standard input and separate streams.
func runBelaiStdin(t *testing.T, dir, home, baseURL, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, append([]string{"-trust-dir"}, args...)...)
	cmd.Dir = dir
	cmd.Env = belaiEnv(baseURL, home)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run belai: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func jsonLines(t *testing.T, out string) []map[string]any {
	t.Helper()
	var res []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("stdout line %q is not JSON: %v\nfull stdout:\n%s", l, err, out)
		}
		res = append(res, m)
	}
	return res
}

var headlessArgs = []string{"-provider", "openai", "-model", "test"}

func TestOutputFormatJSONPrintsOneResult(t *testing.T) {
	srv, _ := newReplyServer(t, "mock reply")
	defer srv.Close()
	home := newHome(t, "")
	args := append([]string{"-output-format", "json", "-prompt", "say hi"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, t.TempDir(), home, srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	ls := jsonLines(t, out)
	if len(ls) != 1 {
		t.Fatalf("json format wrote %d lines: %s", len(ls), out)
	}
	r := ls[0]
	if r["type"] != "result" || r["subtype"] != "success" || r["is_error"] != false || r["reply"] != "mock reply" || r["structured"] != nil {
		t.Fatalf("result = %v", r)
	}
	if id, _ := r["session_id"].(string); id == "" {
		t.Fatalf("no session id: %v", r)
	}
	if u, _ := r["usage"].(map[string]any); u == nil || u["calls"].(float64) < 1 {
		t.Fatalf("no usage: %v", r)
	}
}

func TestOutputFormatStreamJSONHasInitThenResult(t *testing.T) {
	srv, _ := newReplyServer(t, "line one\nline two\n")
	defer srv.Close()
	home := newHome(t, "")
	args := append([]string{"-output-format", "stream-json", "-prompt", "say hi"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, t.TempDir(), home, srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	ls := jsonLines(t, out)
	if len(ls) < 2 || ls[0]["type"] != "init" || ls[len(ls)-1]["type"] != "result" {
		t.Fatalf("stream = %s", out)
	}
	for _, m := range ls {
		switch m["type"] {
		case "init", "text", "tool_start", "tool_result", "warning", "result":
		default:
			t.Fatalf("event type %v is outside the closed set", m["type"])
		}
	}
	// Stdout carries only the protocol: human lines such as the banner are on stderr.
	if strings.Contains(out, "guardrails") {
		t.Fatalf("a banner reached stdout: %s", out)
	}
}

func TestPromptFromStdin(t *testing.T) {
	srv, rs := newReplyServer(t, "mock reply")
	defer srv.Close()
	for name, args := range map[string][]string{
		"dash":  append([]string{"-prompt", "-"}, headlessArgs...),
		"piped": headlessArgs,
	} {
		home := newHome(t, "")
		out, errOut, code := runBelaiStdin(t, t.TempDir(), home, srv.URL, "explain the stdin sentinel\n", args...)
		if code != 0 || !strings.Contains(out, "mock reply") {
			t.Fatalf("%s: exit %d, stdout %q, stderr %q", name, code, out, errOut)
		}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.users) != 2 || !strings.Contains(rs.users[0], "explain the stdin sentinel") || !strings.Contains(rs.users[1], "explain the stdin sentinel") {
		t.Fatalf("the model did not receive the stdin prompt: %v", rs.users)
	}
}

func TestStdinPromptLimits(t *testing.T) {
	srv, rs := newReplyServer(t, "mock reply")
	defer srv.Close()
	home := newHome(t, `{"headless":{"max_stdin_bytes":1024}}`)
	for name, in := range map[string]string{"oversize": strings.Repeat("a", 2000), "nul": "a\x00b", "blank": "\n \n"} {
		args := append([]string{"-prompt", "-"}, headlessArgs...)
		_, errOut, code := runBelaiStdin(t, t.TempDir(), home, srv.URL, in+"\n", args...)
		if code != 2 || !strings.Contains(errOut, "stdin") {
			t.Errorf("%s: exit %d, stderr %q, want usage exit 2", name, code, errOut)
		}
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 0 {
		t.Fatalf("a refused stdin prompt reached the model (%d calls)", rs.calls)
	}
}

func TestBadOutputFormatIsAUsageError(t *testing.T) {
	srv, _ := newReplyServer(t, "mock reply")
	defer srv.Close()
	_, errOut, code := runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", append([]string{"-output-format", "xml", "-prompt", "hi"}, headlessArgs...)...)
	if code != 2 || !strings.Contains(errOut, "output-format") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", "-output-format", "json")
	if code != 2 {
		t.Fatalf("a format with no prompt: exit %d, stderr %q", code, errOut)
	}
}

func TestProjectLayerCannotSetTheOutputFormat(t *testing.T) {
	srv, _ := newReplyServer(t, "mock reply")
	defer srv.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".vulnetix", "belai"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".vulnetix", "belai", "settings.json"), []byte(`{"headless":{"output_format":"json"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := runBelaiStdin(t, dir, newHome(t, ""), srv.URL, "", append([]string{"-prompt", "hi"}, headlessArgs...)...)
	if code != 0 || strings.TrimSpace(out) != "mock reply" {
		t.Fatalf("exit %d, stdout %q, stderr %q: the project layer changed the output", code, out, errOut)
	}
	// The user's own layer does.
	out, _, code = runBelaiStdin(t, dir, newHome(t, `{"headless":{"output_format":"json"}}`), srv.URL, "", append([]string{"-prompt", "hi"}, headlessArgs...)...)
	if code != 0 || !strings.HasPrefix(out, `{"type":"result"`) {
		t.Fatalf("exit %d, stdout %q: the user's setting was not applied", code, out)
	}
}

const e2eSchema = `{"type":"object","properties":{"answer":{"type":"string"},"n":{"type":"integer"}},"required":["answer","n"],"additionalProperties":false}`

func writeSchema(t *testing.T, doc string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "schema.json")
	if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJSONSchemaValidAnswer(t *testing.T) {
	srv, rs := newReplyServer(t, "```json\n{\"answer\":\"yes\",\"n\":2}\n```")
	defer srv.Close()
	args := append([]string{"-output-format", "json", "-json-schema", writeSchema(t, e2eSchema), "-prompt", "decide"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q, stdout %q", code, errOut, out)
	}
	r := jsonLines(t, out)[0]
	s, _ := r["structured"].(map[string]any)
	if s["answer"] != "yes" || s["n"] != float64(2) {
		t.Fatalf("structured = %v", r["structured"])
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 1 {
		t.Fatalf("a valid answer took %d model calls, want 1", rs.calls)
	}
	if !strings.Contains(rs.users[0], "satisfies this JSON schema") || !strings.Contains(rs.users[0], `"answer"`) {
		t.Fatalf("the model was not told the schema:\n%s", rs.users[0])
	}
}

func TestJSONSchemaOneRepairThenSuccess(t *testing.T) {
	srv, rs := newReplyServer(t, "I think the answer is yes.", `{"answer":"yes","n":1}`)
	defer srv.Close()
	args := append([]string{"-output-format", "json", "-json-schema", writeSchema(t, e2eSchema), "-prompt", "decide"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q, stdout %q", code, errOut, out)
	}
	if s, _ := jsonLines(t, out)[0]["structured"].(map[string]any); s["answer"] != "yes" {
		t.Fatalf("structured = %v", s)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 2 {
		t.Fatalf("%d model calls, want 2 (one repair)", rs.calls)
	}
	repair := rs.users[1]
	if !strings.Contains(repair, "not_json") {
		t.Fatalf("the repair turn does not name the failure code:\n%s", repair)
	}
	// The repair message is the harness's: the reply's words are in the
	// assistant turn only, never in the user message that asks for the fix.
	last := repair[strings.LastIndex(repair, "user: "):]
	if strings.Contains(last, "I think") {
		t.Fatalf("the repair message repeats the reply:\n%s", last)
	}
}

func TestJSONSchemaFailureExits5AfterOneRepair(t *testing.T) {
	srv, rs := newReplyServer(t, "not json at all")
	defer srv.Close()
	args := append([]string{"-output-format", "json", "-json-schema", writeSchema(t, e2eSchema), "-prompt", "decide"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", args...)
	if code != 5 {
		t.Fatalf("exit %d, want 5; stderr %q", code, errOut)
	}
	r := jsonLines(t, out)[0]
	se, _ := r["schema_error"].(map[string]any)
	if r["subtype"] != "schema_failure" || r["is_error"] != true || r["structured"] != nil || se["code"] != "not_json" {
		t.Fatalf("result = %v", r)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 2 {
		t.Fatalf("%d model calls, want exactly 2 (the run and one repair)", rs.calls)
	}
}

func TestJSONSchemaUnsupportedKeywordIsAUsageError(t *testing.T) {
	srv, rs := newReplyServer(t, "mock reply")
	defer srv.Close()
	args := append([]string{"-json-schema", writeSchema(t, `{"type":"string","pattern":"^a"}`), "-prompt", "x"}, headlessArgs...)
	_, errOut, code := runBelaiStdin(t, t.TempDir(), newHome(t, ""), srv.URL, "", args...)
	if code != 2 || !strings.Contains(errOut, "pattern") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 0 {
		t.Fatal("a refused schema still reached the model")
	}
}

func TestResumeWithAPromptContinuesTheSession(t *testing.T) {
	srv, rs := newReplyServer(t, "first answer", "second answer")
	defer srv.Close()
	dir := t.TempDir()
	home := newHome(t, "")
	args := append([]string{"-output-format", "json", "-prompt", "remember the word pelican"}, headlessArgs...)
	out, errOut, code := runBelaiStdin(t, dir, home, srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("first run: exit %d, stderr %q", code, errOut)
	}
	id := jsonLines(t, out)[0]["session_id"].(string)

	args = append([]string{"-output-format", "json", "-resume", id[:8], "-prompt", "what was the word"}, headlessArgs...)
	out, errOut, code = runBelaiStdin(t, dir, home, srv.URL, "", args...)
	if code != 0 {
		t.Fatalf("resumed run: exit %d, stderr %q", code, errOut)
	}
	r := jsonLines(t, out)[0]
	if r["session_id"] != id || r["reply"] != "second answer" {
		t.Fatalf("resumed result = %v, want session %s", r, id)
	}
	rs.mu.Lock()
	second := rs.users[len(rs.users)-1]
	rs.mu.Unlock()
	if !strings.Contains(second, "remember the word pelican") || !strings.Contains(second, "first answer") {
		t.Fatalf("the resumed turn did not carry the stored conversation:\n%s", second)
	}
	files := sessionFiles(t, home)
	if len(files) != 1 {
		t.Fatalf("transcripts = %v, want the one session continued", files)
	}
	b, _ := os.ReadFile(files[0])
	if n := strings.Count(string(b), `"type":"user"`); n != 2 {
		t.Fatalf("the transcript holds %d user prompts, want 2", n)
	}
}

func TestResumeFailuresExitNonZero(t *testing.T) {
	srv, rs := newReplyServer(t, "mock reply")
	defer srv.Close()
	dir := t.TempDir()
	home := newHome(t, "")
	_, errOut, code := runBelaiStdin(t, dir, home, srv.URL, "", append([]string{"-resume", "nope", "-prompt", "x"}, headlessArgs...)...)
	if code != 1 || !strings.Contains(errOut, "no session matching") {
		t.Fatalf("missing id: exit %d, stderr %q", code, errOut)
	}
	_, errOut, code = runBelaiStdin(t, dir, home, srv.URL, "", append([]string{"-continue", "-prompt", "x"}, headlessArgs...)...)
	if code != 1 || !strings.Contains(errOut, "no sessions to continue") {
		t.Fatalf("empty project: exit %d, stderr %q", code, errOut)
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.calls != 0 {
		t.Fatal("a failed resume reached the model")
	}
}

func TestStreamJSONFailureStillEndsWithAResult(t *testing.T) {
	srv, _ := newReplyServer(t, "mock reply")
	defer srv.Close()
	// No credentials for the provider: the run fails before any model call.
	var out, errb bytes.Buffer
	cmd := exec.Command(belaiBin, "-trust-dir", "-provider", "anthropic", "-model", "x", "-output-format", "json", "-prompt", "hi")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "BELAI_BASE_URL="+srv.URL, "BELAI_HOME="+newHome(t, ""), "ANTHROPIC_API_KEY=", "BELAI_NO_TUI=1")
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 {
		t.Fatalf("err = %v, stderr %q", err, errb.String())
	}
	r := jsonLines(t, out.String())
	if len(r) != 1 || r[0]["subtype"] != "error" || r[0]["is_error"] != true {
		t.Fatalf("stdout = %s", out.String())
	}
}

// A call that needs an approval nobody can give is withheld, and a structured
// run says so in its result and its exit code. A plain text run still exits 0.
func TestWithheldAskExitsFourInAStructuredFormat(t *testing.T) {
	srv, _ := newToolMockServerFor(t, "Write", map[string]any{"path": "out.txt", "content": "hello"})
	defer srv.Close()
	dir := t.TempDir()
	args := []string{"-tools", "-provider", "openai", "-model", "test"}
	out, errOut, code := runBelaiStdin(t, dir, newHome(t, ""), srv.URL, "", append([]string{"-output-format", "json", "-prompt", "write a file"}, args...)...)
	if code != 4 {
		t.Fatalf("exit %d, want 4; stdout %q, stderr %q", code, out, errOut)
	}
	r := jsonLines(t, out)[0]
	asks, _ := r["asks_withheld"].([]any)
	if r["subtype"] != "denied" || len(asks) != 1 || asks[0] != "Write" {
		t.Fatalf("result = %v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err == nil {
		t.Fatal("the withheld write ran")
	}
	_, _, code = runBelaiStdin(t, dir, newHome(t, ""), srv.URL, "", append([]string{"-prompt", "write a file"}, args...)...)
	if code != 0 {
		t.Fatalf("a text run exited %d, want its original 0", code)
	}
}

func TestStreamJSONCarriesToolEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "safe.txt"), []byte("hello world"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, _ := newToolMockServer(t, "safe.txt")
	defer srv.Close()
	out, errOut, code := runBelaiStdin(t, dir, newHome(t, `{"permissions":{"allow":["Read"]}}`), srv.URL, "",
		"-tools", "-provider", "openai", "-model", "test", "-output-format", "stream-json", "-prompt", "read the file")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	var start, result map[string]any
	for _, m := range jsonLines(t, out) {
		switch m["type"] {
		case "tool_start":
			start = m
		case "tool_result":
			result = m
		}
	}
	if start == nil || result == nil {
		t.Fatalf("no tool events in %s", out)
	}
	if start["name"] != "Read" || start["id"] == "" || start["args"] != nil {
		t.Fatalf("tool_start = %v (arguments are names-only without -verbose)", start)
	}
	if !strings.Contains(result["text"].(string), "hello world") || result["id"] != start["id"] {
		t.Fatalf("tool_result = %v", result)
	}
}
