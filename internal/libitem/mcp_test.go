package libitem

import (
	"strings"
	"testing"
)

const mcpGood = `{"name":"github","transport":"stdio","command":"github-mcp-server","args":["stdio"],"env":{"GITHUB_PERSONAL_ACCESS_TOKEN":"cred:token","LOG":"info"},"tools":["get_issue"],"sandbox":true,"secrets":{"token":"GH_VAULT"}}`
const mcpRemote = `{"name":"Docs","transport":"http","url":"https://mcp.example.com/mcp","headers":{"Authorization":"vault:DOCS_TOKEN","X-Region":"eu"},"timeout_ms":30000}`

func TestMCPItemsValidateAndKeepTheirName(t *testing.T) {
	for _, doc := range []string{mcpGood, mcpRemote, `{"name":"x","command":"c"}`, `{"name":"vx","transport":"http","url":"https://mcp.vulnetix.com/mcp","headers":{"Authorization":"vulnetix:cli"}}`} {
		it, err := Validate(MCP, []byte(doc))
		if err != nil {
			t.Errorf("%s: %v", doc, err)
			continue
		}
		d, err := ParseMCP(it.Doc)
		if err != nil || d.Name != it.Name {
			t.Errorf("ParseMCP: %+v %v", d, err)
		}
	}
	if it, _ := Validate(MCP, []byte(mcpRemote)); it.Name != "Docs" {
		t.Errorf("the name keeps its case: %q", it.Name)
	}
}

func TestMCPItemsRefuseWhatTheHostWouldNot(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"unknown key":      {`{"name":"x","command":"c","bogus":1}`, "bogus"},
		"clef":             {`{"name":"CLEF","command":"c"}`, "built-in"},
		"bad name":         {`{"name":"has space","command":"c"}`, "not valid"},
		"long name":        {`{"name":"` + strings.Repeat("a", 33) + `","command":"c"}`, "not valid"},
		"no command":       {`{"name":"x"}`, "needs a command"},
		"literal secret":   {`{"name":"x","command":"c","env":{"API_TOKEN":"abc"}}`, "looks like a secret"},
		"bare key name":    {`{"name":"x","command":"c","env":{"SSH_KEY":"abc"}}`, "looks like a secret"},
		"vault in env":     {`{"name":"x","command":"c","env":{"A":"vault:B"}}`, "http headers only"},
		"plain http":       {`{"name":"x","transport":"http","url":"http://example.com/mcp"}`, "https, or loopback"},
		"url secret":       {`{"name":"x","transport":"http","url":"https://e.com/m?token=1"}`, "query parameter"},
		"literal header":   {`{"name":"x","transport":"http","url":"https://e.com/m","headers":{"Authorization":"Bearer x"}}`, "looks like a secret"},
		"duplicate header": {`{"name":"x","transport":"http","url":"https://e.com/m","headers":{"X-A":"1","x-a":"2"}}`, "twice"},
		"dead binding":     {`{"name":"x","command":"c","secrets":{"token":"V"}}`, "no cred:token"},
		"wrong type":       {`{"name":"x","command":"c","args":"stdio"}`, "does not match"},
		"fraction":         {`{"name":"x","command":"c","timeout_ms":1.5}`, "does not match"},
		"mixed":            {`{"name":"x","command":"c","url":"https://e.com"}`, "belong to an http server"},
	}
	for name, c := range cases {
		_, err := Validate(MCP, []byte(c.doc))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

// The kind is stored as canonical JSON: a document hashes to the same bytes however
// its keys were ordered, so the library and the host compare equal.
func TestMCPDocumentHashIsOrderIndependent(t *testing.T) {
	a, err := Validate(MCP, []byte(`{"command":"c","name":"x","args":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Validate(MCP, []byte(`{"args":["a"],"name":"x","command":"c"}`))
	if err != nil || a.SHA256 != b.SHA256 {
		t.Fatalf("%v %s %s", err, a.SHA256, b.SHA256)
	}
}

func TestMCPNamesFollowTheServer(t *testing.T) {
	for _, n := range []string{"a", "Acme", "my_server", "a-b", strings.Repeat("A", 32)} {
		if !ValidName(MCP, n) {
			t.Errorf("ValidName(mcp, %q) = false", n)
		}
	}
	for _, n := range []string{"", "a b", "a.b", "a/b", strings.Repeat("a", 33), "café"} {
		if ValidName(MCP, n) {
			t.Errorf("ValidName(mcp, %q) = true", n)
		}
	}
}
