package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/decisions"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/permissions"
	"github.com/vulnetix/belai/internal/posture"
	"github.com/vulnetix/belai/internal/repoindex"
	"github.com/vulnetix/belai/internal/rolemanager/jev"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/tools"
)

// swapDecider rates each candidate by tool name.
type swapDecider struct {
	mu     sync.Mutex
	scores map[string]float64
	calls  int
}

func (d *swapDecider) Decide(_ context.Context, r decisions.Request) (decisions.Result, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	state := r.State.(map[string]any)
	items, _ := state["items"].(map[string]string)
	out := map[string]decisions.Answer{}
	for q := range r.Questions {
		id := strings.TrimPrefix(q, "s:")
		score := d.scores[id]
		_ = items
		out[q] = decisions.Answer{Type: decisions.TypeNoul, Noul: score}
	}
	return decisions.Result{Answers: out}, nil
}
func (d *swapDecider) Identity() string           { return "fake/systemone" }
func (d *swapDecider) Backend() decisions.Backend { return decisions.BackendSystemOne }

type swapHarness struct {
	root      string
	decider   *swapDecider
	sess      *Session
	events    []Event
	mu        sync.Mutex
	replies   []string // what the model sees as tool results, in order
	replans   int
	callIDs   []string // "Name|id" of each assistant tool call the provider was sent
	resultIDs []string // tool_call_id of each tool result the provider was sent
	classify  map[string]int
}

// runSwap runs one turn in which the model calls the scripted Bash commands in
// order. replan is what the fast model answers to a replan request.
func runSwap(t *testing.T, commands []string, replan string, scores map[string]float64, perms permissions.Settings, on func(config.JevJob) bool) *swapHarness {
	t.Helper()
	return runSwapWith(t, commands, replan, scores, perms, on, nil)
}

// runSwapWith is runSwap with the native tools named in natives available.
func runSwapWith(t *testing.T, commands []string, replan string, scores map[string]float64, perms permissions.Settings, on func(config.JevJob) bool, natives []string) *swapHarness {
	t.Helper()
	h := &swapHarness{root: t.TempDir(), decider: &swapDecider{scores: scores}, classify: map[string]int{}}
	if on == nil {
		// These tests are about the swap alone; the other jobs would also call
		// the backend and blur the call counts they assert.
		on = func(j config.JevJob) bool { return j == config.JevBashSwap }
	}
	if err := os.WriteFile(filepath.Join(h.root, "f.txt"), []byte("alpha\nneedle here\nomega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID       string `json:"id"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		system, lastUser := "", ""
		var toolMsgs, callIDs, resultIDs []string
		for _, m := range req.Messages {
			switch m.Role {
			case "system":
				system = m.Content
			case "user":
				lastUser = m.Content
			case "assistant":
				for _, tc := range m.ToolCalls {
					callIDs = append(callIDs, tc.Function.Name+"|"+tc.ID)
				}
			case "tool":
				toolMsgs = append(toolMsgs, m.Content)
				resultIDs = append(resultIDs, m.ToolCallID)
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		switch {
		case strings.Contains(system, "security classifier"):
			if strings.Contains(lastUser, "needle") {
				h.classify["needle"]++
			}
			writeChatJSON(w, "SAFE")
		case strings.Contains(system, "reconsider a shell command"):
			h.replans++
			writeChatJSON(w, replan)
		default:
			h.replies = toolMsgs
			h.callIDs, h.resultIDs = callIDs, resultIDs
			if step < len(commands) {
				c := commands[step]
				step++
				b, _ := json.Marshal(map[string]any{"command": c})
				writeToolCallJSON(w, "Bash", string(b))
				return
			}
			writeChatJSON(w, "done")
		}
	}))
	t.Cleanup(srv.Close)

	sess, err := NewSession(Options{
		Cfg:           run.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "test"},
		Client:        srv.Client(),
		Registry:      tools.DefaultWithCaps(h.root, false, tools.CapabilitiesOf(natives, nil), repoindex.Index{}),
		Perms:         perms,
		Posture:       posture.Defaults(),
		SkipNonceSeed: true,
		Workdir:       h.root,
		MaxIterations: 10,
		Jev:           &jev.Jobs{Client: jev.NewWith(h.decider), On: on},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	h.sess = sess
	if _, err := sess.run(context.Background(), nil, TurnInput{Prompt: "find it", ForceMode: modes.ModeAgent}, false, func(e Event) {
		h.mu.Lock()
		h.events = append(h.events, e)
		h.mu.Unlock()
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	return h
}

func allowAll() permissions.Settings {
	return permissions.Settings{Allow: []string{"Bash", "Grep", "Read", "Cat"}}
}

func (h *swapHarness) started() (from, ran string) {
	for _, e := range h.events {
		if e.Kind == EventToolStartKind && e.Tool != nil {
			return e.SwappedFrom, e.Tool.Name
		}
	}
	return "", ""
}

const grepArgs = `{"pattern":"needle","path":"f.txt"}`

func TestBashIsReplacedByTheOneBuiltinThatRatesAboveTheThreshold(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.97}, allowAll(), nil)
	if len(h.replies) != 1 {
		t.Fatalf("tool results = %q", h.replies)
	}
	got := h.replies[0]
	if !strings.HasPrefix(got, "[harness: your Bash call was replaced by Grep (the decision model rated it 97% equivalent); the Bash command was not run") {
		t.Fatalf("the model was not told about the swap: %q", got)
	}
	if !strings.Contains(got, "needle here") {
		t.Fatalf("result does not carry the match: %q", got)
	}
	from, ran := h.started()
	if from != "Bash" || ran != "Grep" {
		t.Fatalf("start event: swapped from %q, ran %q", from, ran)
	}
	if h.replans != 1 {
		t.Fatalf("replan calls = %d, want 1", h.replans)
	}
	// Swapping never lowers scrutiny: the Grep result was classified.
	if h.classify["needle"] == 0 {
		t.Fatal("the swapped Grep output skipped the classifier")
	}
	for _, e := range h.events {
		if e.Kind == EventToolResultKind && (e.ToolName != "Grep" || e.SwappedFrom != "Bash") {
			t.Fatalf("result event: tool %q swapped from %q", e.ToolName, e.SwappedFrom)
		}
	}
}

func TestTheProviderStillSeesAResultForTheBashCallID(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.97}, allowAll(), nil)
	if len(h.callIDs) != 1 || !strings.HasPrefix(h.callIDs[0], "Bash|") {
		t.Fatalf("the assistant call must stay the model's Bash call: %v", h.callIDs)
	}
	id := strings.TrimPrefix(h.callIDs[0], "Bash|")
	if len(h.resultIDs) != 1 || h.resultIDs[0] != id {
		t.Fatalf("the result must answer the Bash call id %q, got %v", id, h.resultIDs)
	}
}

func TestNoSwapWhenTwoBuiltinsRateAboveTheThreshold(t *testing.T) {
	h := runSwapWith(t, []string{"cat f.txt"}, `{"file_path":"f.txt"}`, map[string]float64{"Read": 0.97, "Cat": 0.96}, allowAll(), nil, []string{"Cat"})
	if len(h.replies) != 1 || strings.Contains(h.replies[0], "[harness:") {
		t.Fatalf("two winners must run Bash: %q", h.replies)
	}
	if h.replans != 0 {
		t.Fatalf("no replan expected, got %d", h.replans)
	}
	if from, ran := h.started(); from != "" || ran != "Bash" {
		t.Fatalf("start: from %q ran %q", from, ran)
	}
}

func TestNoSwapBelowTheThreshold(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.94}, allowAll(), nil)
	if strings.Contains(h.replies[0], "[harness:") || h.replans != 0 {
		t.Fatalf("0.94 must not swap: %q replans %d", h.replies, h.replans)
	}
}

func TestReplanKeepBashRunsBash(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt"}, "KEEP_BASH", map[string]float64{"Grep": 0.99}, allowAll(), nil)
	if strings.Contains(h.replies[0], "[harness:") || !strings.Contains(h.replies[0], "2:needle here") {
		t.Fatalf("Bash should have run: %q", h.replies)
	}
	if h.replans != 1 {
		t.Fatalf("replans = %d", h.replans)
	}
}

func TestReplanArgumentsMustComeFromTheCommand(t *testing.T) {
	// The fast model invents a different target than the command names.
	h := runSwap(t, []string{"grep -n needle f.txt"}, `{"pattern":"needle","path":"secrets.txt"}`, map[string]float64{"Grep": 0.99}, allowAll(), nil)
	if strings.Contains(h.replies[0], "[harness:") {
		t.Fatalf("an invented path was accepted: %q", h.replies)
	}
	// Undeclared arguments and malformed replies are refused too.
	for _, replan := range []string{`{"pattern":"needle","bogus":"x"}`, "no idea", `{"path":"f.txt"}`} {
		h := runSwap(t, []string{"grep -n needle f.txt"}, replan, map[string]float64{"Grep": 0.99}, allowAll(), nil)
		if strings.Contains(h.replies[0], "[harness:") {
			t.Fatalf("replan %q was accepted: %q", replan, h.replies)
		}
	}
}

func TestOnlyASinglePlainCommandIsEverRated(t *testing.T) {
	for _, cmd := range []string{"grep -n needle f.txt | head", "grep needle f.txt > out.txt", "cat f.txt; ls", "grep needle $(echo f.txt)", "echo hi && cat f.txt"} {
		h := runSwap(t, []string{cmd}, grepArgs, map[string]float64{"Grep": 0.99, "Read": 0.99}, allowAll(), nil)
		if h.decider.calls != 0 || h.replans != 0 {
			t.Fatalf("%q was rated (%d) or replanned (%d)", cmd, h.decider.calls, h.replans)
		}
		if strings.Contains(h.replies[0], "[harness:") {
			t.Fatalf("%q was swapped", cmd)
		}
	}
}

func TestADeniedBashCommandIsNeverLaundered(t *testing.T) {
	perms := permissions.Settings{Allow: []string{"Grep"}, Deny: []string{"Bash(grep*)"}}
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.99}, perms, nil)
	if h.decider.calls != 0 {
		t.Fatal("a denied command was rated")
	}
	if strings.Contains(h.replies[0], "needle here") || !strings.Contains(h.replies[0], "withheld") {
		t.Fatalf("the denied command should be withheld: %q", h.replies)
	}
}

func TestTheBuiltinIsHeldToItsOwnPermissions(t *testing.T) {
	perms := permissions.Settings{Allow: []string{"Bash"}, Deny: []string{"Grep"}}
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.99}, perms, nil)
	if strings.Contains(h.replies[0], "[harness:") {
		t.Fatalf("a denied builtin was used: %q", h.replies)
	}
	if !strings.Contains(h.replies[0], "2:needle here") {
		t.Fatalf("Bash should have run when the swap was refused: %q", h.replies)
	}
}

func TestARepeatedCommandRunsAsBash(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt", "grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.99}, allowAll(), nil)
	if len(h.replies) != 2 {
		t.Fatalf("tool results = %q", h.replies)
	}
	if !strings.Contains(h.replies[0], "[harness:") || strings.Contains(h.replies[1], "[harness:") {
		t.Fatalf("first should swap, second should run Bash: %q", h.replies)
	}
	if h.replans != 1 {
		t.Fatalf("replans = %d, want 1", h.replans)
	}
}

func TestSwitchedOffJobNeverCallsTheBackend(t *testing.T) {
	h := runSwap(t, []string{"grep -n needle f.txt"}, grepArgs, map[string]float64{"Grep": 0.99}, allowAll(),
		func(config.JevJob) bool { return false })
	if h.decider.calls != 0 || h.replans != 0 || strings.Contains(h.replies[0], "[harness:") {
		t.Fatalf("job off but calls %d replans %d: %q", h.decider.calls, h.replans, h.replies)
	}
}

func TestNoBackendMeansNoSwap(t *testing.T) {
	s := &Session{}
	if got := s.trySwap(context.Background(), nil, map[string]any{"command": "cat f"}); got != nil {
		t.Fatal("a session with no Jev jobs swapped")
	}
}

func TestUniqueWinner(t *testing.T) {
	cases := []struct {
		scores map[string]float64
		name   string
		pct    int
		ok     bool
	}{
		{map[string]float64{"Grep": 0.96}, "Grep", 96, true},
		{map[string]float64{"Grep": 0.95}, "Grep", 95, true},
		{map[string]float64{"Grep": 0.949}, "", 0, false},
		{map[string]float64{"Grep": 0.99, "Read": 0.96}, "", 0, false},
		{map[string]float64{"Grep": 0.99, "Read": 0.4}, "Grep", 99, true},
		{nil, "", 0, false},
	}
	for _, c := range cases {
		name, pct, ok := uniqueWinner(c.scores)
		if name != c.name || pct != c.pct || ok != c.ok {
			t.Errorf("uniqueWinner(%v) = %q %d %v", c.scores, name, pct, ok)
		}
	}
}

func TestGroundedRejectsInventedValues(t *testing.T) {
	def := tools.Definition{Properties: map[string]tools.Property{
		"pattern": {Type: "string"}, "path": {Type: "string"}, "output_mode": {Type: "string", Enum: []string{"content", "count"}},
		"limit": {Type: "integer"}, "-i": {Type: "boolean"},
	}}
	cmd := "grep -rn foo src --max-count 20"
	ok := []map[string]any{
		{"pattern": "foo", "path": "src"},
		{"pattern": "foo", "path": "."},
		{"pattern": "foo", "output_mode": "content"},
		{"pattern": "foo", "limit": float64(20)},
		{"pattern": "foo", "-i": true},
	}
	for _, a := range ok {
		if !grounded(def, cmd, a) {
			t.Errorf("grounded(%v) = false", a)
		}
	}
	bad := []map[string]any{
		{"pattern": "bar"},
		{"pattern": "foo", "path": "/etc"},
		{"pattern": "foo", "limit": float64(99)},
		{"pattern": "foo", "path": []any{"src"}},
		{"pattern": "foo", "output_mode": "files"},
	}
	for _, a := range bad {
		if grounded(def, cmd, a) {
			t.Errorf("grounded(%v) = true", a)
		}
	}
}
