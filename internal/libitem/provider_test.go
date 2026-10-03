package libitem

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/docparity"
)

// prov wraps one provider profile in a document.
func prov(profile string) string {
	return `{"name":"mine","providers":{"my-llm":` + profile + `}}`
}

const okProfile = `{"base_url":"https://llm.example.com/v1","api":"openai-chat"}`

func TestValidateProvider(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	patch := func(old, new string) string { return prov(strings.Replace(okProfile, old, new, 1)) }
	with := func(extra string) string { return prov(strings.TrimSuffix(okProfile, "}") + "," + extra + "}") }
	model := func(m string) string { return with(`"models":[` + m + `]`) }
	models := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = fmt.Sprintf(`{"id":"m%d"}`, i)
		}
		return with(`"models":[` + strings.Join(parts, ",") + `]`)
	}
	fw := func(f string) string { return `{"name":"mine","providers":{},"firewall":` + f + `}` }
	inst := func(i string) string { return fw(`{"instances":{"gw":` + i + `}}`) }
	cases := []struct {
		name string
		doc  string
		err  string
	}{
		{"a minimal provider", prov(okProfile), ""},
		{"no providers at all", `{"name":"mine","providers":{}}`, ""},
		{"every profile field", prov(`{"base_url":"https://llm.example.com/v1","api":"anthropic-messages","auth":"x-api-key","api_key_env":"MY_LLM_KEY","kind":"openai-compatible","protocol":"https","host":"llm.example.com","port":"443","decision_path":"/v1/x","models":[{"id":"m1","name":"M one","context_window":128000,"max_tokens":4096,"images":true}]}`), ""},
		{"a jev provider needs no api", prov(`{"base_url":"https://jev.example.com","kind":"jev","decision_path":"/v1/systemone"}`), ""},
		{"an ollama provider on plain http", prov(`{"base_url":"http://10.0.0.5:11434","api":"openai-chat","kind":"ollama"}`), ""},

		{"no name", `{"providers":{}}`, "name is required"},
		{"no providers key", `{"name":"mine"}`, "provider.providers is required"},
		{"an unknown top-level key", `{"name":"mine","providers":{},"api_key":"sk-x"}`, `unknown field "api_key"`},
		{"providers as a list", `{"name":"mine","providers":[]}`, "must be an object"},
		{"a provider that is not an object", `{"name":"mine","providers":{"a":1}}`, "must be an object"},
		{"32 providers", `{"name":"mine","providers":{` + providersN(32) + `}}`, ""},
		{"33 providers", `{"name":"mine","providers":{` + providersN(33) + `}}`, "has 33 entries"},

		// Names.
		{"a built-in name", `{"name":"mine","providers":{"openai":` + okProfile + `}}`, "collides with a built-in provider"},
		{"typesafe", `{"name":"mine","providers":{"typesafe":` + okProfile + `}}`, "collides with a built-in provider"},
		{"an uppercase provider name", `{"name":"mine","providers":{"My":` + okProfile + `}}`, "must be lowercase letters"},
		{"a provider name starting with a dash", `{"name":"mine","providers":{"-x":` + okProfile + `}}`, "must be lowercase letters"},
		{"a provider name at the limit", `{"name":"mine","providers":{"` + long(64) + `":` + okProfile + `}}`, ""},
		{"a provider name over the limit", `{"name":"mine","providers":{"` + long(65) + `":` + okProfile + `}}`, "must be lowercase letters"},

		// Keys never travel.
		{"an api_key field", with(`"api_key":"sk-ant-abc"`), `unknown field "api_key"`},
		{"a key field", with(`"key":"sk-abc"`), `unknown field "key"`},
		{"a headers field", with(`"headers":{"x-api-key":"sk"}`), `unknown field "headers"`},
		{"a key pasted as api_key_env", patch(`"api":`, `"api_key_env":"sk-ant-abc123","api":`), "never a key"},
		{"a lower-case api_key_env", patch(`"api":`, `"api_key_env":"my_key","api":`), "never a key"},
		{"an api_key_env with a dash", patch(`"api":`, `"api_key_env":"MY-KEY","api":`), "never a key"},
		{"an api_key_env at the limit", patch(`"api":`, `"api_key_env":"`+strings.Repeat("K", 128)+`","api":`), ""},
		{"an api_key_env over the limit", patch(`"api":`, `"api_key_env":"`+strings.Repeat("K", 129)+`","api":`), "api_key_env is 129 bytes"},

		// URLs.
		{"no base_url", prov(`{"api":"openai-chat"}`), "base_url is required"},
		{"a base_url with a user", patch("https://llm", "https://bob@llm"), "must not carry credentials"},
		{"a base_url with user and password", patch("https://llm", "https://bob:pw@llm"), "must not carry credentials"},
		{"a base_url with a fragment", patch("/v1", "/v1#x"), "must not carry a fragment"},
		{"a base_url with a space", patch("/v1", "/v 1"), "holds a space"},
		{"a base_url with a bad port", patch("llm.example.com", "llm.example.com:99999"), "invalid port"},
		{"a base_url that is ftp", patch("https://llm.example.com/v1", "ftp://llm.example.com/v1"), "must be an http or https URL"},
		{"a base_url with no host", patch("https://llm.example.com/v1", "https:///v1"), "not an absolute URL with a host"},
		{"a base_url at the limit", patch("https://llm.example.com/v1", "https://llm.example.com/"+long(MaxProviderURL-len("https://llm.example.com/"))), ""},
		{"a base_url over the limit", patch("https://llm.example.com/v1", "https://llm.example.com/"+long(MaxProviderURL)), "base_url is"},
		{"a jev provider on plain http to a remote host", prov(`{"base_url":"http://jev.example.com","kind":"jev"}`), "sends tool output in the clear"},
		{"a jev provider on plain http to localhost", prov(`{"base_url":"http://localhost:8080","kind":"jev"}`), ""},
		{"a jev provider on plain http to 127.0.0.1", prov(`{"base_url":"http://127.0.0.1:8080","kind":"jev"}`), ""},
		{"a jev provider on plain http to ::1", prov(`{"base_url":"http://[::1]:8080","kind":"jev"}`), ""},

		// Enumerations.
		{"no api", prov(`{"base_url":"https://llm.example.com"}`), "api is required"},
		{"an unknown api", patch("openai-chat", "grpc"), "is unknown (want openai-chat"},
		{"an unknown auth", patch(`"api":"openai-chat"`, `"api":"openai-chat","auth":"basic"`), "is unknown (want bearer"},
		{"an unknown kind", patch(`"api":"openai-chat"`, `"api":"openai-chat","kind":"vllm"`), "is unknown (want"},
		{"a bad protocol", patch(`"api":"openai-chat"`, `"api":"openai-chat","protocol":"ws"`), "protocol must be http or https"},
		{"port 0", patch(`"api":"openai-chat"`, `"api":"openai-chat","port":"0"`), "is not a TCP port"},
		{"port 65536", patch(`"api":"openai-chat"`, `"api":"openai-chat","port":"65536"`), "is not a TCP port"},
		{"port 65535", patch(`"api":"openai-chat"`, `"api":"openai-chat","port":"65535"`), ""},
		{"a port with letters", patch(`"api":"openai-chat"`, `"api":"openai-chat","port":"80a"`), "is not a TCP port"},
		{"a port as a number", patch(`"api":"openai-chat"`, `"api":"openai-chat","port":443`), "must be a string"},
		{"a host with a slash", patch(`"api":"openai-chat"`, `"api":"openai-chat","host":"a/b"`), "is not a host name"},
		{"a host at the limit", patch(`"api":"openai-chat"`, `"api":"openai-chat","host":"`+long(253)+`"`), ""},
		{"a host over the limit", patch(`"api":"openai-chat"`, `"api":"openai-chat","host":"`+long(254)+`"`), "host is 254 bytes"},
		{"a decision path with ..", prov(`{"base_url":"https://j.example.com","kind":"jev","decision_path":"/a/../b"}`), "must be a plain absolute path"},
		{"a relative decision path", prov(`{"base_url":"https://j.example.com","kind":"jev","decision_path":"x"}`), "must be a plain absolute path"},
		{"a decision path with a query", prov(`{"base_url":"https://j.example.com","kind":"jev","decision_path":"/x?y=1"}`), "must be a plain absolute path"},
		{"a decision path with a space", prov(`{"base_url":"https://j.example.com","kind":"jev","decision_path":"/x y"}`), "must be a plain absolute path"},

		// Models.
		{"256 models", models(256), ""},
		{"257 models", models(257), "has 257 entries"},
		{"a model with every field", model(`{"id":"a","name":"A","context_window":10000000,"max_tokens":0,"images":false}`), ""},
		{"a model id at the limit", model(`{"id":"` + long(128) + `"}`), ""},
		{"a model id over the limit", model(`{"id":"` + long(129) + `"}`), "id is 129 bytes"},
		{"a model name over the limit", model(`{"id":"a","name":"` + long(129) + `"}`), "name is 129 bytes"},
		{"a model with no id", model(`{"name":"A"}`), "id is required"},
		{"a model listed twice", model(`{"id":"a"},{"id":"a"}`), "listed twice"},
		{"a context window over the limit", model(`{"id":"a","context_window":10000001}`), "from 0 to 10000000"},
		{"a negative max_tokens", model(`{"id":"a","max_tokens":-1}`), "from 0 to 10000000"},
		{"a fractional context window", model(`{"id":"a","context_window":1.5}`), "whole number"},
		{"images as a string", model(`{"id":"a","images":"yes"}`), "must be true or false"},
		{"a model with an unknown key", model(`{"id":"a","price":1}`), `unknown field "price"`},
		{"a model that is not an object", model(`"a"`), "must be an object"},

		// Firewall.
		{"an empty firewall block", fw(`{}`), ""},
		{"a firewall with the vulnetix instance active", fw(`{"enabled":true,"active":"vulnetix"}`), ""},
		{"a firewall with a custom instance", inst(`{"adapter":"custom","url":"https://gw.example.com/{provider}","mode":"header","header":"X-Gw-Key","providers":["my-llm"]}`), ""},
		{"an unknown firewall key", fw(`{"token":"x"}`), `unknown field "token"`},
		{"enabled not a bool", fw(`{"enabled":"yes"}`), "must be true or false"},
		{"an active instance that is not configured", fw(`{"active":"gw"}`), "not a configured firewall"},
		{"an active instance that is configured", `{"name":"mine","providers":{},"firewall":{"active":"gw","instances":{"gw":{"adapter":"custom","url":"https://gw.example.com"}}}}`, ""},
		{"17 instances", fw(`{"instances":{` + instancesN(17) + `}}`), "has 17 entries"},
		{"16 instances", fw(`{"instances":{` + instancesN(16) + `}}`), ""},
		{"an instance name with a capital", fw(`{"instances":{"Gw":{"adapter":"custom","url":"https://gw.example.com"}}}`), "must be lowercase letters, digits and dashes"},
		{"an unknown adapter", inst(`{"adapter":"nginx","url":"https://gw.example.com"}`), "is unknown (want one of"},
		{"no adapter", inst(`{"url":"https://gw.example.com"}`), "adapter is required"},
		{"the vulnetix adapter under another name", inst(`{"adapter":"vulnetix"}`), "configured only as the vulnetix instance"},
		{"another adapter named vulnetix", fw(`{"instances":{"vulnetix":{"adapter":"custom","url":"https://gw.example.com"}}}`), "reserved for the Vulnetix AI Firewall"},
		{"the vulnetix instance with its adapter", fw(`{"instances":{"vulnetix":{"adapter":"vulnetix"}}}`), ""},
		{"the vulnetix adapter with a mode", fw(`{"instances":{"vulnetix":{"adapter":"vulnetix","mode":"header","header":"X-A"}}}`), "takes no mode or header"},
		{"a custom adapter with no url", inst(`{"adapter":"custom"}`), "url is required"},
		{"an aisg adapter with no url", inst(`{"adapter":"aisg"}`), ""},
		{"a firewall url with a user", inst(`{"adapter":"custom","url":"https://u:p@gw.example.com"}`), "must not carry credentials"},
		{"a firewall url with a query", inst(`{"adapter":"custom","url":"https://gw.example.com/x?k=v"}`), "must not carry a query"},
		{"a firewall url on plain http to a remote host", inst(`{"adapter":"custom","url":"http://gw.example.com"}`), "must use https"},
		{"a firewall url on plain http to localhost", inst(`{"adapter":"custom","url":"http://localhost:9000/{provider}"}`), ""},
		{"a firewall url that is ftp", inst(`{"adapter":"custom","url":"ftp://gw.example.com"}`), "must use https"},
		{"an unknown mode", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"bearer"}`), "is unknown (want transparent"},
		{"a header outside header mode", inst(`{"adapter":"custom","url":"https://gw.example.com","header":"X-Key"}`), "only used in header mode"},
		{"header mode with no header", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"header"}`), "cannot carry a firewall key"},
		{"a forbidden header", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"header","header":"Cookie"}`), "cannot carry a firewall key"},
		{"a forbidden header in capitals", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"header","header":"HOST"}`), "cannot carry a firewall key"},
		{"a harness header", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"header","header":"X-Belai-Nonce"}`), "cannot carry a firewall key"},
		{"a header that is not a token", inst(`{"adapter":"custom","url":"https://gw.example.com","mode":"header","header":"X Key"}`), "cannot carry a firewall key"},
		{"64 firewall providers", inst(`{"adapter":"custom","url":"https://gw.example.com","providers":[` + quotedN(64) + `]}`), ""},
		{"65 firewall providers", inst(`{"adapter":"custom","url":"https://gw.example.com","providers":[` + quotedN(65) + `]}`), "has 65 entries"},
		{"a firewall provider that is not a name", inst(`{"adapter":"custom","url":"https://gw.example.com","providers":["Bad Name"]}`), "is not a provider name"},
		{"a firewall provider that is not a string", inst(`{"adapter":"custom","url":"https://gw.example.com","providers":[1]}`), "is not a provider name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			it, err := Validate(Provider, []byte(c.doc))
			if c.err == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if it.Kind != Provider || it.Name == "" {
					t.Errorf("item = %+v", it)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err = %v, want one naming %q", err, c.err)
			}
		})
	}
}

func providersN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"p%d":%s`, i, okProfile)
	}
	return strings.Join(parts, ",")
}

func instancesN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"gw%d":{"adapter":"custom","url":"https://gw.example.com"}`, i)
	}
	return strings.Join(parts, ",")
}

func quotedN(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"p%d"`, i)
	}
	return strings.Join(parts, ",")
}

// A document the library accepts can still be one Belai refuses to load. Belai's
// own validators run after the library's, so such a document is never written as a
// setting that breaks every start.
func TestBelaiOwnProviderValidationRunsToo(t *testing.T) {
	// The library's name pattern allows a digit first; Belai's provider names do not.
	doc := `{"name":"mine","providers":{"1st":` + okProfile + `}}`
	if _, err := validateProvider([]byte(doc)); err != nil {
		t.Fatalf("the library's own check refused it: %v", err)
	}
	if _, err := Validate(Provider, []byte(doc)); err == nil || !strings.Contains(err.Error(), "invalid provider name") {
		t.Fatalf("Belai's own check did not refuse it: %v", err)
	}
}

func TestParseProviderAndProviderDocument(t *testing.T) {
	src := `{"firewall":{"active":"gw","enabled":true,"instances":{"gw":{"adapter":"custom","header":"X-Gw-Key","mode":"header","providers":["my-llm"],"url":"https://gw.example.com/{provider}"}}},"name":"mine","providers":{"my-llm":{"api":"openai-chat","api_key_env":"MY_LLM_KEY","base_url":"https://llm.example.com/v1","models":[{"context_window":128000,"id":"m1","images":true,"max_tokens":4096,"name":"M one"}]}}}`
	it, err := Validate(Provider, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseProvider(it.Doc)
	if err != nil {
		t.Fatal(err)
	}
	p := d.Providers["my-llm"]
	if d.Name != "mine" || p.BaseURL != "https://llm.example.com/v1" || p.APIKeyEnv != "MY_LLM_KEY" || len(p.Models) != 1 || p.Models[0].Images == nil || !*p.Models[0].Images ||
		d.Firewall == nil || d.Firewall.Active != "gw" || d.Firewall.Instances["gw"].Header != "X-Gw-Key" {
		t.Fatalf("doc = %+v", d)
	}
	// The settings that produced the document export it back byte for byte.
	exp, err := ProviderDocument("mine", config.Settings{Providers: d.Providers, Firewall: d.Firewall})
	if err != nil {
		t.Fatal(err)
	}
	b, err := encodeAny(exp)
	if err != nil || string(b) != string(it.Doc) {
		t.Fatalf("export = %q (%v), want %q", b, err, it.Doc)
	}
	// With no providers and no firewall the export is an empty set.
	empty, _ := ProviderDocument("x", config.Settings{})
	b, _ = encodeAny(empty)
	if string(b) != `{"name":"x","providers":{}}`+"\n" {
		t.Errorf("empty = %s", b)
	}
	if _, err := Validate(Provider, b); err != nil {
		t.Errorf("an empty export does not validate: %v", err)
	}
	// A firewall block with nothing in it is left out.
	nothing, _ := ProviderDocument("x", config.Settings{Firewall: &config.FirewallSettings{}})
	if _, has := nothing["firewall"]; has {
		t.Error("an empty firewall block was exported")
	}
}

// encodeAny canonicalises any value the way a document is: through JSON.
func encodeAny(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(b)
}

func TestLibraryItemsPageStatesTheProviderRules(t *testing.T) {
	doc := strings.Join(strings.Fields(docparity.Read(t, "docs/library-items.md")), " ")
	for _, want := range []string{
		"never holds an API key",
		"`api_key_env` is the *name* of an environment variable and must match `^[A-Z_][A-Z0-9_]*$`",
		"`providers` (required object, 0 to 32 entries)",
		"at most 256 of `{id, name?, context_window?, max_tokens?, images?}`",
		"whole numbers 0 to 10000000",
		"0 to 16",
		"at most 64 provider names",
		"`^[a-z0-9][a-z0-9-]{0,39}$`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/library-items.md does not say %q", want)
		}
	}
	for name := range builtinProviders {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/library-items.md does not name the reserved provider %q", name)
		}
	}
	if MaxProviders != 32 || MaxProviderKey != 64 || MaxProviderURL != 1024 || MaxProviderModels != 256 || MaxProviderText != 128 ||
		MaxProviderTokens != 10_000_000 || MaxProviderHost != 253 || MaxProviderPath != 128 || MaxProviderInstance != 16 ||
		MaxProviderFWNames != 64 || MaxProviderEnvName != 128 {
		t.Error("a provider limit changed; update docs/library-items.md and this test")
	}
}

// A provider set may name the systemone kind, or jev, its name before the
// rename: both validate unchanged (so a hash the library holds still matches),
// and the parsed set installs as systemone.
func TestProviderSetSystemOneKind(t *testing.T) {
	for _, kind := range []string{"systemone", "jev"} {
		src := `{"name":"mine","providers":{"home":{"base_url":"http://127.0.0.1:8000","kind":"` + kind + `"}}}`
		it, err := Validate(Provider, []byte(src))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if !strings.Contains(string(it.Doc), `"kind":"`+kind+`"`) {
			t.Errorf("%s: canonical bytes changed the kind: %s", kind, it.Doc)
		}
		d, err := ParseProvider(it.Doc)
		if err != nil {
			t.Fatal(err)
		}
		if d.Providers["home"].Kind != config.SystemOneKind {
			t.Errorf("%s installs as %q", kind, d.Providers["home"].Kind)
		}
	}
	if _, err := Validate(Provider, []byte(`{"name":"mine","providers":{"strands-decider":{"base_url":"http://127.0.0.1:8000","kind":"systemone"}}}`)); err == nil {
		t.Error("a provider set may not take the strands-decider name")
	}
}
