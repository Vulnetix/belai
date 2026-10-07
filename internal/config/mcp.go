package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

// Built-in MCP servers and the references an MCP server entry may hold.
//
// Everything here sits under the user's own `mcp` key: the project layer cannot
// add a server, name a secret or change a built-in (resolve.go drops it).

const (
	// ClefMCPName is the built-in decision server's name. Its tools reach the
	// model as mcp__clef__<tool>. A settings entry or library item of the same
	// name is never used.
	ClefMCPName = "clef"

	// MCPDefaultSkipAskAt is the confidence at which an ask is answered by the
	// decision model instead of the user, when the user has not set one.
	MCPDefaultSkipAskAt = 0.90

	// MCPSecretMax bounds the credential bindings of one server entry.
	MCPSecretMax = 16
)

// References a server entry's env and header values may use in place of a secret.
const (
	// MCPEnvPrefix copies a variable of Belai's environment.
	MCPEnvPrefix = "env:"
	// MCPCredPrefix names a credential the user (or a library push) stored for
	// this server in the keychain or the global credentials file.
	MCPCredPrefix = "cred:"
	// MCPVaultPrefix names a Secrets Vault entry. It resolves from the host's
	// vault lease (a Pix Sandbox) and only in the headers of an http server.
	MCPVaultPrefix = "vault:"
)

// MCPBuiltin holds the switches of the servers Belai offers itself.
type MCPBuiltin struct {
	// Clef is the decision server.
	Clef *ClefMCPSettings `json:"clef,omitempty"`
	// Vulnetix is the hosted Vulnetix MCP server, offered by default once
	// Vulnetix credentials exist.
	Vulnetix *VulnetixMCPSettings `json:"vulnetix,omitempty"`
}

// ClefMCPSettings configures the built-in decision server.
type ClefMCPSettings struct {
	// Enabled turns the server on or off. nil means on. It still needs a
	// decision backend (the Pix Sandbox Worker, or Cloudflare Workers AI
	// credentials) to be offered.
	Enabled *bool `json:"enabled,omitempty"`
	// SkipAsk lets the decision model answer an ask that would go to the user
	// when it is confident enough. nil means on.
	SkipAsk *bool `json:"skip_ask,omitempty"`
	// SkipAskAt is the confidence above which the decision is used at once.
	// nil means MCPDefaultSkipAskAt. Above 0.5 and at most 1.
	SkipAskAt *float64 `json:"skip_ask_at,omitempty"`
}

// VulnetixMCPSettings configures the hosted Vulnetix MCP server.
type VulnetixMCPSettings struct {
	// Enabled turns it on or off. nil means on while Vulnetix credentials exist.
	Enabled *bool `json:"enabled,omitempty"`
}

// ClefMCPEnabled reports whether the user allows the decision server. Whether a
// backend exists to serve it is decided where the server is built.
func (s Settings) ClefMCPEnabled() bool {
	if s.MCP == nil || s.MCP.Builtin == nil || s.MCP.Builtin.Clef == nil || s.MCP.Builtin.Clef.Enabled == nil {
		return true
	}
	return *s.MCP.Builtin.Clef.Enabled
}

// ClefSkipAsk returns the confidence at which the decision model answers an ask,
// and whether it answers at all.
func (s Settings) ClefSkipAsk() (threshold float64, on bool) {
	c := (*ClefMCPSettings)(nil)
	if s.MCP != nil && s.MCP.Builtin != nil {
		c = s.MCP.Builtin.Clef
	}
	if !s.ClefMCPEnabled() || (c != nil && c.SkipAsk != nil && !*c.SkipAsk) {
		return MCPDefaultSkipAskAt, false
	}
	if c != nil && c.SkipAskAt != nil {
		return *c.SkipAskAt, true
	}
	return MCPDefaultSkipAskAt, true
}

// VulnetixMCPEnabled reports whether the user wants the hosted Vulnetix MCP
// server. Default on; it is only offered while Vulnetix credentials exist.
func (s Settings) VulnetixMCPEnabled() bool {
	if s.MCP == nil || s.MCP.Builtin == nil || s.MCP.Builtin.Vulnetix == nil || s.MCP.Builtin.Vulnetix.Enabled == nil {
		return true
	}
	return *s.MCP.Builtin.Vulnetix.Enabled
}

// mergeMCPBuiltin folds in over cur, field by field. It is only called for the
// user's own layers.
func mergeMCPBuiltin(cur, in *MCPBuiltin) *MCPBuiltin {
	if in == nil {
		return cur
	}
	out := &MCPBuiltin{}
	if cur != nil {
		*out = *cur
	}
	if in.Clef != nil {
		c := ClefMCPSettings{}
		if out.Clef != nil {
			c = *out.Clef
		}
		if in.Clef.Enabled != nil {
			c.Enabled = in.Clef.Enabled
		}
		if in.Clef.SkipAsk != nil {
			c.SkipAsk = in.Clef.SkipAsk
		}
		if in.Clef.SkipAskAt != nil {
			c.SkipAskAt = in.Clef.SkipAskAt
		}
		out.Clef = &c
	}
	if in.Vulnetix != nil {
		v := VulnetixMCPSettings{}
		if out.Vulnetix != nil {
			v = *out.Vulnetix
		}
		if in.Vulnetix.Enabled != nil {
			v.Enabled = in.Vulnetix.Enabled
		}
		out.Vulnetix = &v
	}
	return out
}

var (
	mcpNameRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	mcpRefNameRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	mcpSecretNameRE = regexp.MustCompile(`(?i)(secret|token|password|passwd|api[_-]?key|credential|private|authorization|cookie|auth)`)
)

// ValidMCPName reports whether name may name a server: letters, digits, `_` and
// `-`, at most 32 characters.
func ValidMCPName(name string) bool { return mcpNameRE.MatchString(name) }

// ValidMCPRefName reports whether name is shaped like an environment variable,
// the form every reference target and credential key takes.
func ValidMCPRefName(name string) bool { return mcpRefNameRE.MatchString(name) }

// MCPLooksSecret reports whether an environment variable or header name reads
// as a secret, so its value must be a reference and never a literal.
func MCPLooksSecret(name string) bool { return mcpSecretNameRE.MatchString(name) }

// MCPRefKind is the kind of reference an env or header value holds.
type MCPRefKind string

const (
	MCPRefNone     MCPRefKind = ""         // a literal value
	MCPRefEnv      MCPRefKind = "env"      // env:NAME
	MCPRefCred     MCPRefKind = "cred"     // cred:KEY
	MCPRefVault    MCPRefKind = "vault"    // vault:NAME
	MCPRefVulnetix MCPRefKind = "vulnetix" // vulnetix:cli
)

// ParseMCPRef reads an env or header value. kind is MCPRefNone for a literal.
// For a reference, name is its target and ok reports whether the target is a
// legal name; vulnetix:cli has no target.
func ParseMCPRef(v string) (kind MCPRefKind, name string, ok bool) {
	switch {
	case v == VulnetixCLIRef:
		return MCPRefVulnetix, "", true
	case strings.HasPrefix(v, MCPEnvPrefix):
		kind, name = MCPRefEnv, strings.TrimPrefix(v, MCPEnvPrefix)
	case strings.HasPrefix(v, MCPCredPrefix):
		kind, name = MCPRefCred, strings.TrimPrefix(v, MCPCredPrefix)
	case strings.HasPrefix(v, MCPVaultPrefix):
		kind, name = MCPRefVault, strings.TrimPrefix(v, MCPVaultPrefix)
	default:
		return MCPRefNone, "", true
	}
	return kind, name, ValidMCPRefName(name)
}

// MCPCredentialName is the credential-store provider name of a server's
// secrets: mcp:<server>, with one field per cred: key.
func MCPCredentialName(server string) string { return "mcp:" + server }

// ValidateMCP checks the built-in switches of the mcp block. Resolve runs it, so
// it refuses only a value Belai could not use: a server entry a person wrote by
// hand is still loaded (a bad one shows as failed in /mcp), and the per-server
// rules apply to what a form or the library writes (ValidateMCPServer).
func ValidateMCP(s Settings) error {
	if s.MCP == nil {
		return nil
	}
	if b := s.MCP.Builtin; b != nil && b.Clef != nil && b.Clef.SkipAskAt != nil {
		if at := *b.Clef.SkipAskAt; !(at > 0.5 && at <= 1) {
			return fmt.Errorf("mcp.builtin.clef.skip_ask_at must be above 0.5 and at most 1, got %v", at)
		}
	}
	return nil
}

// ValidateMCPServer checks one server entry. strict adds the rules for what is
// written by a form or the library: a name that looks secret holds a reference,
// the transport fields do not mix, an http URL is https or loopback, and every
// credential binding is used.
func ValidateMCPServer(name string, s MCPServer, strict bool) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("mcp server %q: %s", name, fmt.Sprintf(format, a...))
	}
	if !ValidMCPName(name) {
		return fmt.Errorf("mcp server name %q must be letters, digits, _ or - (at most 32)", name)
	}
	if strict && name == ClefMCPName {
		return bad("%q is the built-in decision server", ClefMCPName)
	}
	if s.TimeoutMS < 0 || s.TimeoutMS > 600000 {
		return bad("timeout_ms must be 0 to 600000")
	}
	if len(s.Tools) > 64 || len(s.Args) > 64 || len(s.Env) > 64 || len(s.Headers) > 32 {
		return bad("too many tools, args, env entries or headers")
	}
	for _, t := range s.Tools {
		if strings.TrimSpace(t) == "" {
			return bad("tools holds an empty name")
		}
	}
	switch s.Transport {
	case "", "stdio":
		if strings.TrimSpace(s.Command) == "" {
			return bad("a stdio server needs a command")
		}
		if strict && (s.URL != "" || len(s.Headers) > 0) {
			return bad("url and headers belong to an http server")
		}
	case "http":
		if err := checkMCPURL(s.URL, strict); err != nil {
			return bad("%v", err)
		}
		if strict && (s.Command != "" || len(s.Args) > 0 || len(s.Env) > 0 || s.Sandbox) {
			return bad("command, args, env and sandbox belong to a stdio server")
		}
	default:
		return bad("transport must be stdio or http")
	}
	used := map[string]bool{}
	for k, v := range s.Env {
		if !ValidMCPRefName(k) {
			return bad("env name %q is not a legal variable name", k)
		}
		kind, ref, ok := ParseMCPRef(v)
		switch {
		case !ok:
			return bad("env %s: %q is not a legal reference", k, v)
		case kind == MCPRefVault || kind == MCPRefVulnetix:
			return bad("env %s: %s references are for http headers only", k, kind)
		case kind == MCPRefCred:
			used[ref] = true
		case kind == MCPRefNone && strict && MCPLooksSecret(k):
			return bad("env %s looks like a secret: use env:NAME or cred:KEY, not a literal", k)
		}
	}
	for k, v := range s.Headers {
		if k == "" || strings.ContainsAny(k, " \t\r\n:") {
			return bad("header name %q is not valid", k)
		}
		kind, ref, ok := ParseMCPRef(v)
		switch {
		case !ok:
			return bad("header %s: %q is not a legal reference", k, v)
		case kind == MCPRefVulnetix && (!strings.EqualFold(k, "Authorization") || !mcpVulnetixURL(s.URL)):
			return bad("header %s: %s is only for Authorization on an https vulnetix.com URL", k, VulnetixCLIRef)
		case kind == MCPRefCred:
			used[ref] = true
		case kind == MCPRefNone && strict && MCPLooksSecret(k):
			return bad("header %s looks like a secret: use env:NAME, cred:KEY or vault:NAME, not a literal", k)
		}
	}
	if len(s.Secrets) > MCPSecretMax {
		return bad("secrets binds at most %d credentials", MCPSecretMax)
	}
	for key, vaultName := range s.Secrets {
		if !ValidMCPRefName(key) || !ValidMCPRefName(vaultName) {
			return bad("secrets: %q to %q must both be variable-shaped names", key, vaultName)
		}
		if strict && !used[key] {
			return bad("secrets binds %q but no cred:%s value uses it", key, key)
		}
	}
	return nil
}

func checkMCPURL(raw string, strict bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("an http server needs an http(s) url")
	}
	if u.User != nil {
		return fmt.Errorf("the url must not hold credentials")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if !strict || mcpLoopback(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("an http url must be https, or loopback")
	}
	return fmt.Errorf("an http server needs an http(s) url")
}

func mcpLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// mcpVulnetixURL reports whether raw is https on vulnetix.com or a subdomain.
func mcpVulnetixURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "vulnetix.com" || strings.HasSuffix(h, ".vulnetix.com")
}
