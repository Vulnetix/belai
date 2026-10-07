package libitem

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/vulnetix/belai/internal/config"
)

// MCPDoc is one MCP server as a library item: the fields of config.MCPServer
// under a name. It never holds a secret value. An env or header value is a
// reference (env:NAME, cred:KEY, vault:NAME in headers, vulnetix:cli) or plain
// text for a name that does not read as a secret, and `secrets` binds a cred:
// key to the Secrets Vault entry the library pushes to the host on install (a
// name, never a value).
type MCPDoc struct {
	Name string `json:"name"`
	config.MCPServer
}

var mcpFields = []string{"name", "transport", "command", "args", "env", "url", "headers", "tools", "disabled", "sandbox", "timeout_ms", "secrets"}

func init() { register(MCP, validateMCP, nil) }

// ParseMCP validates a canonical MCP document and decodes it.
func ParseMCP(canonical []byte) (MCPDoc, error) {
	if _, err := validateMCP(canonical); err != nil {
		return MCPDoc{}, err
	}
	var d MCPDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return MCPDoc{}, refuse("the document does not match the schema")
	}
	return d, nil
}

// MCPDocument is the library document for a server of the host's settings.
func MCPDocument(name string, s config.MCPServer) ([]byte, error) {
	return json.Marshal(MCPDoc{Name: name, MCPServer: s})
}

// validateMCP checks a canonical MCP document and returns its name. The rules are
// the server's (vdb-site belai_items_validate_mcp.go), which are Belai's own
// config.ValidateMCPServer in strict form, so a document accepted here is one the
// host writes as a setting it loads.
func validateMCP(canonical []byte) (string, error) {
	m, err := object(canonical)
	if err != nil {
		return "", err
	}
	if err := onlyKeys(m, "mcp", mcpFields...); err != nil {
		return "", err
	}
	name, err := docName(m, func(n string) bool { return ValidName(MCP, n) }, "letters, digits, _ or -, 32 characters at most")
	if err != nil {
		return "", err
	}
	if strings.EqualFold(name, config.ClefMCPName) {
		return "", refuse("%q is the built-in decision server and cannot be an item", config.ClefMCPName)
	}
	// Every field has its type checked by decoding into the settings type; an
	// unknown key was refused above, and a number with a fraction cannot be an int.
	var d MCPDoc
	if err := json.Unmarshal(canonical, &d); err != nil {
		return "", refuse("the document does not match the schema: %s", clip(cleanForMessage(err.Error()), 160))
	}
	seen := map[string]bool{}
	var headerNames []string
	for k := range d.Headers {
		headerNames = append(headerNames, k)
	}
	sort.Strings(headerNames)
	for _, k := range headerNames {
		lk := strings.ToLower(k)
		if seen[lk] {
			return "", refuse("mcp.headers names %q twice (header names are not case sensitive)", cleanForMessage(k))
		}
		seen[lk] = true
	}
	if err := config.ValidateMCPServer(name, d.MCPServer, true); err != nil {
		return "", refuse("%s", clip(err.Error(), 240))
	}
	for k, v := range d.Env {
		if err := checkStr(v, "env."+k, "mcp", 4096, true); err != nil {
			return "", err
		}
	}
	for k, v := range d.Headers {
		if err := checkStr(v, "headers."+k, "mcp", 4096, true); err != nil {
			return "", err
		}
	}
	return name, nil
}
