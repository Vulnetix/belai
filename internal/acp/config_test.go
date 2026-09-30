package acp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vulnetix/belai/internal/agent"
	"github.com/vulnetix/belai/internal/jsonrpc"
	"github.com/vulnetix/belai/internal/turnlog"
)

func configServer(t *testing.T, switched *[]string) (*Server, *acpSession) {
	s, _ := postEndServer(t, nil)
	s.opts.Models = func(cwd, p, m string) []ModelChoice {
		return []ModelChoice{
			{Provider: p, Model: m, Label: p + " · " + m},
			{Provider: "anthropic", Model: "claude-x", Label: "anthropic · X"},
		}
	}
	s.opts.Toggles = func(string) Toggles { return Toggles{Guardrails: true, Ask: true} }
	s.opts.Switch = func(ctx context.Context, cwd, id, p, m string, t Toggles) (*agent.Session, error) {
		*switched = append(*switched, p+"/"+m+" "+t.String())
		return nil, nil
	}
	ss := &acpSession{id: "a", cwd: "/w", mode: modeAuto, provider: "cloudflare", model: "deepseek", toggles: Toggles{Guardrails: true, Ask: true}, always: map[string]bool{}, log: turnlog.New(nil)}
	s.sessions["a"] = ss
	return s, ss
}

func setOpt(s *Server, params string) (any, error) {
	return s.handle(context.Background(), "session/set_config_option", json.RawMessage(params))
}

func TestConfigOptionsOfferModelAndMode(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	opts := s.configOptions(ss)
	if len(opts) != 5 {
		t.Fatalf("options = %v", opts)
	}
	m := opts[0].(map[string]any)
	if m["id"] != "model" || m["category"] != "model" || m["currentValue"] != "cloudflare/deepseek" {
		t.Fatalf("model option = %v", m)
	}
	if opts[1].(map[string]any)["id"] != "mode" {
		t.Fatalf("second option = %v", opts[1])
	}
}

func TestPickingAModelRebuildsTheSession(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	res, err := setOpt(s, `{"sessionId":"a","configId":"model","value":"anthropic/claude-x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(sw) != 1 || sw[0] != "anthropic/claude-x g+a+c-" || ss.provider != "anthropic" || ss.model != "claude-x" {
		t.Fatalf("switched %v, session on %s/%s", sw, ss.provider, ss.model)
	}
	cur := res.(map[string]any)["configOptions"].([]any)[0].(map[string]any)["currentValue"]
	if cur != "anthropic/claude-x" {
		t.Fatalf("reply state = %v", cur)
	}
}

// Only a value Belai listed is accepted; the editor cannot name a provider.
func TestUnlistedModelIsRefused(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	for _, v := range []string{"openai/gpt-5", "anthropic/claude-x ", "", "cloudflare"} {
		_, err := setOpt(s, `{"sessionId":"a","configId":"model","value":`+string(mustJSON(v))+`}`)
		if rpc, ok := err.(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("%q: %v", v, err)
		}
	}
	if len(sw) != 0 || ss.provider != "cloudflare" {
		t.Fatalf("switched %v", sw)
	}
}

func TestModelChangeRefusedMidTurn(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	ss.cancel = func() {}
	_, err := setOpt(s, `{"sessionId":"a","configId":"model","value":"anthropic/claude-x"}`)
	if rpc, ok := err.(*jsonrpc.Error); !ok || rpc.Code != jsonrpc.CodeInvalidRequest || len(sw) != 0 {
		t.Fatalf("err %v switched %v", err, sw)
	}
}

func TestModeThroughConfigOption(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	if _, err := setOpt(s, `{"sessionId":"a","configId":"mode","value":"plan"}`); err != nil || ss.mode != "plan" {
		t.Fatalf("%v %q", err, ss.mode)
	}
	if _, err := setOpt(s, `{"sessionId":"a","configId":"nope","value":"x"}`); err == nil {
		t.Fatal("unknown option accepted")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestTogglesRebuildTheSessionAndKeepTheModel(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	for _, c := range []string{
		`{"sessionId":"a","configId":"guardrails","value":"off"}`,
		`{"sessionId":"a","configId":"ask","value":"off"}`,
		`{"sessionId":"a","configId":"caveman","value":"on"}`,
	} {
		if _, err := setOpt(s, c); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"cloudflare/deepseek g-a+c-", "cloudflare/deepseek g-a-c-", "cloudflare/deepseek g-a-c+"}
	if len(sw) != 3 || sw[0] != want[0] || sw[1] != want[1] || sw[2] != want[2] {
		t.Fatalf("rebuilds = %v", sw)
	}
	if ss.toggles.Guardrails || ss.toggles.Ask || !ss.toggles.Caveman {
		t.Fatalf("toggles = %v", ss.toggles)
	}
	res, _ := setOpt(s, `{"sessionId":"a","configId":"guardrails","value":"on"}`)
	got := map[string]any{}
	for _, o := range res.(map[string]any)["configOptions"].([]any) {
		m := o.(map[string]any)
		got[m["id"].(string)] = m["currentValue"]
	}
	if got["guardrails"] != "on" || got["ask"] != "off" || got["caveman"] != "on" {
		t.Fatalf("state = %v", got)
	}
}

func TestBadToggleValueIsRefused(t *testing.T) {
	var sw []string
	s, _ := configServer(t, &sw)
	for _, v := range []string{"maybe", "", "ON"} {
		if _, err := setOpt(s, `{"sessionId":"a","configId":"ask","value":"`+v+`"}`); err == nil {
			t.Fatalf("%q accepted", v)
		}
	}
	if len(sw) != 0 {
		t.Fatalf("rebuilt: %v", sw)
	}
}

func TestNoTogglesWithoutTheHostHook(t *testing.T) {
	var sw []string
	s, ss := configServer(t, &sw)
	s.opts.Toggles = nil
	if len(s.configOptions(ss)) != 2 {
		t.Fatal("toggles offered without a host hook")
	}
	if _, err := setOpt(s, `{"sessionId":"a","configId":"guardrails","value":"off"}`); err == nil {
		t.Fatal("toggle accepted without a host hook")
	}
}
