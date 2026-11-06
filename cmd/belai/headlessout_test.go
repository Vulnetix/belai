package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/headless"
	"github.com/vulnetix/belai/internal/modes"
	"github.com/vulnetix/belai/internal/run"
	"github.com/vulnetix/belai/internal/session"
)

func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatal(err)
	}
	w.Close()
	t.Cleanup(func() { r.Close() })
	return r
}

func TestResolveOutputFormat(t *testing.T) {
	s := config.Settings{Headless: &config.HeadlessSettings{OutputFormat: "json"}}
	if f, err := resolveOutputFormat("", s); err != nil || f != headless.FormatJSON {
		t.Errorf("settings default = %q, %v", f, err)
	}
	if f, err := resolveOutputFormat("stream-json", s); err != nil || f != headless.FormatStreamJSON {
		t.Errorf("the flag must win: %q, %v", f, err)
	}
	if f, err := resolveOutputFormat("", config.Settings{}); err != nil || f != headless.FormatText {
		t.Errorf("default = %q, %v", f, err)
	}
	if _, err := resolveOutputFormat("xml", s); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestResolvePrompt(t *testing.T) {
	// A plain -prompt is returned as it is, whatever stdin holds.
	if p, fromStdin, err := resolvePrompt("hello", pipeWith(t, "ignored"), true, 100); err != nil || p != "hello" || fromStdin {
		t.Errorf("plain = %q, %v, %v", p, fromStdin, err)
	}
	// -prompt - reads stdin even when the process is otherwise interactive.
	if p, fromStdin, err := resolvePrompt("-", pipeWith(t, "from stdin\n"), false, 100); err != nil || p != "from stdin" || !fromStdin {
		t.Errorf("dash = %q, %v, %v", p, fromStdin, err)
	}
	// An empty -prompt reads a pipe when the run is not interactive.
	if p, _, err := resolvePrompt("", pipeWith(t, "piped"), true, 100); err != nil || p != "piped" {
		t.Errorf("piped = %q, %v", p, err)
	}
	// Not eligible: left empty, stdin untouched.
	if p, fromStdin, err := resolvePrompt("", pipeWith(t, "piped"), false, 100); err != nil || p != "" || fromStdin {
		t.Errorf("ineligible = %q, %v, %v", p, fromStdin, err)
	}
	// /dev/null is not a prompt source.
	null, _ := os.Open(os.DevNull)
	defer null.Close()
	if p, _, err := resolvePrompt("", null, true, 100); err != nil || p != "" {
		t.Errorf("devnull = %q, %v", p, err)
	}
}

func TestResolvePromptFailuresAreUsageErrors(t *testing.T) {
	for name, in := range map[string]string{"empty": "", "oversize": strings.Repeat("a", 200), "nul": "a\x00b"} {
		_, _, err := resolvePrompt("-", pipeWith(t, in), true, 100)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
}

func TestCheckHeadlessCombo(t *testing.T) {
	schema, _ := headless.ParseSchemaBytes([]byte(`{"type":"object"}`))
	ok := headlessOptions{}
	if err := checkHeadlessCombo(ok, true, false, true, modes.ModePlan); err != nil {
		t.Errorf("a plain run is never refused: %v", err)
	}
	withSchema := headlessOptions{Schema: schema}
	resumed := headlessOptions{ResumeID: "s1"}
	for name, c := range map[string]struct {
		ho                  headlessOptions
		detect, tools, plan bool
		mode                modes.Mode
	}{
		"schema detect":     {withSchema, true, true, false, ""},
		"schema no tools":   {withSchema, false, false, false, ""},
		"schema plan":       {withSchema, false, true, true, ""},
		"schema -mode plan": {withSchema, false, true, false, modes.ModePlan},
		"resume detect":     {resumed, true, true, false, ""},
		"resume no tools":   {resumed, false, false, false, ""},
	} {
		err := checkHeadlessCombo(c.ho, c.detect, c.tools, c.plan, c.mode)
		var ue *usageError
		if !errors.As(err, &ue) {
			t.Errorf("%s: err = %v, want a usage error", name, err)
		}
	}
	if err := checkHeadlessCombo(withSchema, false, true, false, modes.ModeGoal); err != nil {
		t.Errorf("schema in goal mode: %v", err)
	}
	if err := checkHeadlessCombo(resumed, false, true, true, modes.ModePlan); err != nil {
		t.Errorf("resume in plan mode: %v", err)
	}
}

func final(ho headlessOptions, o agentOutcome, runErr error) headless.Final {
	return buildFinal(ho, "sid", false, "", o, runErr, nil, time.Second)
}

func TestBuildFinalSubtypeOrder(t *testing.T) {
	structured := headlessOptions{Format: headless.FormatJSON}
	denied := agentOutcome{Res: run.Result{Reply: "r", AsksWithheld: []string{"Bash"}}}
	if f := final(structured, denied, nil); f.Subtype != headless.SubtypeDenied || !f.IsError {
		t.Errorf("denied = %+v", f)
	}
	// A plain text run has always exited 0 here.
	if f := final(headlessOptions{Format: headless.FormatText}, denied, nil); f.Subtype != headless.SubtypeSuccess {
		t.Errorf("text with withheld asks = %q, want success", f.Subtype)
	}
	schemaFail := agentOutcome{Res: run.Result{AsksWithheld: []string{"Bash"}}, SchemaErr: &headless.SchemaError{Pointer: "/a", Code: headless.CodeRequired}}
	if f := final(structured, schemaFail, nil); f.Subtype != headless.SubtypeSchemaFailure {
		t.Errorf("schema beats denied: %q", f.Subtype)
	}
	capped := agentOutcome{Res: run.Result{StopReason: run.StopRunCap}, SchemaErr: schemaFail.SchemaErr}
	if f := final(structured, capped, nil); f.Subtype != headless.SubtypeRunCap {
		t.Errorf("run cap beats schema: %q", f.Subtype)
	}
	if f := final(structured, capped, errors.New("boom")); f.Subtype != headless.SubtypeError || f.Error != "boom" {
		t.Errorf("error beats run cap: %+v", f)
	}
	if f := final(structured, agentOutcome{Res: run.Result{Reply: "fine"}}, nil); f.Subtype != headless.SubtypeSuccess || f.IsError {
		t.Errorf("success = %+v", f)
	}
}

func TestBuildFinalCarriesUsageAndTextShowsTheStructuredValue(t *testing.T) {
	sum := &run.UsageSummary{}
	sum.Add(run.UsageEvent{Provider: "p", Model: "m", Tokens: 30, Prompt: 20, Completion: 10})
	o := agentOutcome{Res: run.Result{Reply: "```json\n{\"a\":1}\n```"}, Structured: json.RawMessage(`{"a":1}`)}
	f := buildFinal(headlessOptions{Format: headless.FormatText}, "sid", false, "", o, nil, sum, 1500*time.Millisecond)
	if f.Reply != `{"a":1}` {
		t.Errorf("text reply = %q", f.Reply)
	}
	if f.Usage == nil || f.Usage.TotalTokens != 30 || f.Usage.PromptTokens != 20 || f.Usage.Calls != 1 || f.DurationMS != 1500 {
		t.Errorf("usage/duration = %+v %d", f.Usage, f.DurationMS)
	}
	f = buildFinal(headlessOptions{Format: headless.FormatJSON}, "sid", false, "", o, nil, nil, 0)
	if !strings.Contains(f.Reply, "```json") {
		t.Errorf("json format must keep the model's reply: %q", f.Reply)
	}
}

func TestReportHeadlessExitCodes(t *testing.T) {
	cases := []struct {
		f    headless.Final
		code int
	}{
		{headless.Final{Subtype: headless.SubtypeSuccess}, 0},
		{headless.Final{Subtype: headless.SubtypeError, Error: "x"}, 1},
		{headless.Final{Subtype: headless.SubtypeRunCap}, 3},
		{headless.Final{Subtype: headless.SubtypeDenied, AsksWithheld: []string{"Bash"}}, 4},
		{headless.Final{Subtype: headless.SubtypeSchemaFailure, SchemaError: &headless.SchemaError{Pointer: "/", Code: headless.CodeNotJSON}}, 5},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		em := headless.NewEmitter(headless.FormatJSON, &buf, headless.EmitterOptions{})
		code, err := reportHeadless(em, c.f)
		if code != c.code || (c.code != 0) != (err != nil) {
			t.Errorf("%s: code %d, err %v; want code %d", c.f.Subtype, code, err, c.code)
		}
		if !strings.Contains(buf.String(), `"subtype":"`+c.f.Subtype+`"`) {
			t.Errorf("%s: result not written: %s", c.f.Subtype, buf.String())
		}
	}
	if !errors.Is(func() error {
		_, e := reportHeadless(headless.NewEmitter(headless.FormatText, &bytes.Buffer{}, headless.EmitterOptions{}), headless.Final{Subtype: headless.SubtypeRunCap})
		return e
	}(), errRunCapReached) {
		t.Error("a run cap must still report errRunCapReached")
	}
}

func TestContinueLatestStillWorksThroughTheHeadlessHelper(t *testing.T) {
	st := session.NewStoreAt(t.TempDir())
	cur, _ := session.KeyFor(t.TempDir())
	if _, _, err := continueLatest(st, cur); err == nil {
		t.Error("no sessions: want an error")
	}
}

func TestContinueMayBeBareAndDoesNotSwallowTheNextFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
		rest string
	}{
		{[]string{"-continue", "-prompt", "x"}, "1", "x"},
		{[]string{"-c", "-prompt", "x"}, "1", "x"},
		{[]string{"-continue=true", "-prompt", "x"}, "1", "x"},
		{[]string{"-continue=false", "-prompt", "x"}, "", "x"},
		{[]string{"-prompt", "x"}, "", "x"},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		cont := new(string)
		fs.Var(bareFlag{cont}, "continue", "")
		fs.Var(bareFlag{cont}, "c", "")
		prompt := fs.String("prompt", "", "")
		if err := fs.Parse(tc.args); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if *cont != tc.want || *prompt != tc.rest {
			t.Errorf("%v: continue = %q, prompt = %q", tc.args, *cont, *prompt)
		}
	}
}
