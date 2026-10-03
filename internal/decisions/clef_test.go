package decisions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostedClefIsADecisionBackend(t *testing.T) {
	for _, p := range []string{CloudflareWorkersAIProvider, CloudflareGatewayProvider} {
		for _, m := range ClefModels {
			if b, ok := BackendOf(p, "", m.ID); !ok || b != BackendSystemOne {
				t.Errorf("BackendOf(%s, %s) = %q, %v", p, m.ID, b, ok)
			}
		}
	}
	for _, c := range [][2]string{{CloudflareWorkersAIProvider, "@cf/moonshotai/kimi-k2.6"}, {"openrouter", "@cf/cloudflare/clef"}, {"cloudflare-workers-ai", "clef"}} {
		if _, ok := BackendOf(c[0], "", c[1]); ok {
			t.Errorf("BackendOf(%s, %s) is a decision backend", c[0], c[1])
		}
	}
	if m, ok := ClefByID("@cf/cloudflare/clef-flash"); !ok || m.Wire != "clef-flash" {
		t.Errorf("ClefByID = %+v, %v", m, ok)
	}
}

// Workers AI wraps answers in Cloudflare's envelope and needs the short model
// name in the body; Identity keeps the catalogue id.
func TestSystemOneCloudflareEnvelope(t *testing.T) {
	var body map[string]any
	var auth, aig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ai/run/@cf/cloudflare/clef-flash" {
			t.Errorf("path %s", r.URL.Path)
		}
		auth, aig = r.Header.Get("Authorization"), r.Header.Get("cf-aig-authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"result":{"model":"clef-flash","answers":{"q":{"type":"noul","noul":0.04}},"usage":{"input_tokens":30}},"success":true,"errors":[],"messages":[]}`))
	}))
	t.Cleanup(srv.Close)
	s := &SystemOne{
		Name: CloudflareWorkersAIProvider, Model: "@cf/cloudflare/clef-flash", WireModel: "clef-flash", SendModel: true,
		BaseURL: srv.URL, Path: "/ai/run/@cf/cloudflare/clef-flash", Envelope: true, CriteriaObject: true,
		Key:          func() (string, error) { return "cf-token", nil },
		ExtraHeaders: func() (map[string]string, error) { return map[string]string{"cf-aig-authorization": "Bearer gw"}, nil },
	}
	res, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answers["q"].Noul != 0.04 || body["model"] != "clef-flash" || auth != "Bearer cf-token" || aig != "Bearer gw" {
		t.Fatalf("answer %+v, model %v, auth %q, aig %q", res.Answers["q"], body["model"], auth, aig)
	}
	if s.Identity() != "cloudflare-workers-ai/@cf/cloudflare/clef-flash" {
		t.Errorf("identity %q", s.Identity())
	}
}

// success:false and an error status carry Cloudflare's first message, cleaned;
// a bare answer body (a gateway that unwrapped it) is read as it is.
func TestSystemOneCloudflareEnvelopeFailures(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
		class  Class
	}{
		{200, `{"result":null,"success":false,"errors":[{"code":5007,"message":"No such model \u001b[31m@cf/x"}]}`, "No such model", ClassSchema},
		{400, `{"success":false,"errors":[{"code":7001,"message":"model: must match clef|clef-flash"}]}`, "must match", ClassSchema},
		{401, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`, "Authentication error", ClassAuth},
		{200, `{"success":true,"result":null}`, "no result", ClassProtocol},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte(c.body))
		}))
		s := &SystemOne{BaseURL: srv.URL, Path: "/ai/run/@cf/cloudflare/clef", Envelope: true}
		_, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) || ClassOf(err) != c.class || strings.ContainsRune(err.Error(), 0x1b) {
			t.Errorf("%d %s: err = %v (class %q)", c.status, c.body, err, ClassOf(err))
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"q":{"type":"noul","noul":0.5}}}`))
	}))
	defer srv.Close()
	s := &SystemOne{BaseURL: srv.URL, Envelope: true}
	if res, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}}); err != nil || res.Answers["q"].Noul != 0.5 {
		t.Fatalf("bare body: %+v, %v", res, err)
	}
}

// The gateway header goes only to BaseURL: a redirect is refused, so it
// never reaches the host the redirect names.
func TestSystemOneExtraHeadersNeverFollowARedirect(t *testing.T) {
	elsewhere := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { elsewhere = true }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/systemone", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	s := &SystemOne{BaseURL: srv.URL, Envelope: true, ExtraHeaders: func() (map[string]string, error) { return map[string]string{"cf-aig-authorization": "Bearer gw"}, nil }}
	if _, err := s.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}}); err == nil || elsewhere {
		t.Fatalf("err = %v, followed = %v", err, elsewhere)
	}
}

func TestSystemOneMaxQuestionsNeverSent(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	qs := map[string]Question{}
	for i := 0; i < 3; i++ {
		qs[string(rune('a'+i))] = Noul("p")
	}
	s := &SystemOne{BaseURL: srv.URL, MaxQuestions: 2}
	if _, err := s.Decide(context.Background(), Request{State: "x", Questions: qs}); !IsUnavailable(err) || called {
		t.Fatalf("err = %v, called = %v", err, called)
	}
}

// A local Clef is answered by llama-server's own /v1/systemone, with object
// criteria and no prompt layout; a refused connection still asks to recover.
func TestLlamaSystemOneTemplate(t *testing.T) {
	var path string
	var crit map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		var b struct {
			Questions map[string]struct {
				Criteria map[string]any `json:"criteria"`
			} `json:"questions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		crit = b.Questions["m"].Criteria
		_, _ = w.Write([]byte(`{"model":"belai-clef-flash","answers":{"m":{"type":"choice","choice":"plan","probabilities":{"agent":0.2,"plan":0.8},"confidence":0.6}}}`))
	}))
	defer srv.Close()
	m, _ := LocalModelByID("clef-flash")
	l := &Llama{Model: m, Resolve: func() string { return srv.URL }}
	res, err := l.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"m": {Type: TypeChoice, Instructions: "mode?", Options: []string{"agent", "plan"}}}})
	if err != nil || path != "/v1/systemone" || res.Answers["m"].Choice != "plan" || len(crit) != 2 {
		t.Fatalf("res %+v err %v path %q crit %v", res, err, path, crit)
	}
	if l.Identity() != "decision-local/clef-flash" || m.MinBuild != ClefMinBuild || m.Template != TemplateSystemOne {
		t.Errorf("identity %q model %+v", l.Identity(), m)
	}
	recovered := false
	l2 := &Llama{Model: m, BaseURL: "http://127.0.0.1:1", OnConnRefused: func() { recovered = true }}
	if _, err := l2.Decide(context.Background(), Request{State: "x", Questions: map[string]Question{"q": Noul("p")}}); !IsUnavailable(err) || !recovered {
		t.Fatalf("refused: err %v recovered %v", err, recovered)
	}
}
