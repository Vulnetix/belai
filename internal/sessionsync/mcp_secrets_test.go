package sessionsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMCPSecretsReadsTheSecretsAndTheMissing(t *testing.T) {
	s := &itemSite{t: t, respond: `{"secrets":[{"key":"token","value":"TOPSECRET1"},{"key":"region","value":"eu"}],"missing":["extra"]}`}
	c, stop := s.start()
	defer stop()
	got, missing, err := c.MCPSecrets(context.Background(), testHost, "d1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "token" || got[0].Reveal() != "TOPSECRET1" || got[1].Reveal() != "eu" || strings.Join(missing, ",") != "extra" {
		t.Fatalf("secrets %v missing %v", got, missing)
	}
	if s.method != "GET" || s.path != "/hosts/"+testHost+"/library/mcp-secrets" || s.query != "dispatch=d1" {
		t.Errorf("%s %s ?%s", s.method, s.path, s.query)
	}
}

// A secret is a secret: no fmt verb, no JSON encoder and no error text carries it.
func TestMCPSecretNeverPrintsItself(t *testing.T) {
	k := MCPSecret{Key: "token", value: "TOPSECRETVALUE"}
	outputs := []string{
		k.String(), k.GoString(), fmt.Sprint(k), fmt.Sprintf("%v", k), fmt.Sprintf("%+v", k), fmt.Sprintf("%#v", k), fmt.Sprintf("%s", k),
		fmt.Sprint([]MCPSecret{k}), fmt.Sprintf("%v", &k), fmt.Sprintf("%+v", struct{ K MCPSecret }{k}),
	}
	b, err := json.Marshal([]MCPSecret{k})
	if err != nil {
		t.Fatal(err)
	}
	outputs = append(outputs, string(b))
	for _, o := range outputs {
		if strings.Contains(o, "TOPSECRETVALUE") {
			t.Errorf("a secret was printed: %q", o)
		}
		if !strings.Contains(o, "redacted") {
			t.Errorf("%q does not say it is redacted", o)
		}
	}
	if k.Reveal() != "TOPSECRETVALUE" {
		t.Error("Reveal changed the secret")
	}
}

func TestMCPSecretsMapsTheServersRefusalsAndNeverEchoesABody(t *testing.T) {
	for status, want := range map[int]error{404: ErrNotFound, 409: ErrConflict, 401: ErrUnauthorized, 403: ErrKeysNotOverTLS, 502: ErrKeysUnavailable, 503: ErrKeysUnavailable} {
		s := &itemSite{t: t, status: status, respond: `{"error":"x","secrets":[{"key":"a","value":"LEAK"}]}`}
		if status == 403 {
			s.respond = `{"error":"mcp secrets are only sent over TLS","secrets":[{"key":"a","value":"LEAK"}]}`
		}
		c, stop := s.start()
		got, _, err := c.MCPSecrets(context.Background(), testHost, "d1")
		stop()
		if err != want || len(got) != 0 {
			t.Errorf("status %d: err %v secrets %v, want %v", status, err, got, want)
		}
	}
	// Any other status names a route and a status, never the body.
	s := &itemSite{t: t, status: 418, respond: `{"secrets":[{"key":"a","value":"LEAK"}]}`}
	c, stop := s.start()
	defer stop()
	if _, _, err := c.MCPSecrets(context.Background(), testHost, "d1"); err == nil || strings.Contains(err.Error(), "LEAK") {
		t.Errorf("err = %v", err)
	}
}

func TestMCPSecretsBoundsWhatItReads(t *testing.T) {
	long := strings.Repeat("v", MaxMCPSecretBytes+1)
	many := `{"secrets":[` + strings.Repeat(`{"key":"k","value":"v"},`, MaxMCPSecrets) + `{"key":"k","value":"v"}]}`
	for name, body := range map[string]string{
		"a long secret": `{"secrets":[{"key":"k","value":"` + long + `"}]}`,
		"too many":      many,
	} {
		s := &itemSite{t: t, respond: body}
		c, stop := s.start()
		got, _, err := c.MCPSecrets(context.Background(), testHost, "d1")
		stop()
		if err == nil || len(got) != 0 {
			t.Errorf("%s: err %v secrets %d", name, err, len(got))
		}
	}
}

func TestDispatchCarriesAnMCPSecretsRequest(t *testing.T) {
	var d Dispatch
	if err := json.Unmarshal([]byte(`{"id":"d","kind":"mcp_secrets_install","server":"Acme","keys":["token","region"]}`), &d); err != nil {
		t.Fatal(err)
	}
	if d.Kind != "mcp_secrets_install" || d.Server != "Acme" || strings.Join(d.Keys, ",") != "token,region" {
		t.Fatalf("dispatch = %+v", d)
	}
}
