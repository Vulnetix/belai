package rc

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/vulnetix/belai/internal/config"
	"github.com/vulnetix/belai/internal/credentials"
	"github.com/vulnetix/belai/internal/libitem"
	"github.com/vulnetix/belai/internal/mcp"
	"github.com/vulnetix/belai/internal/sanitize"
	"github.com/vulnetix/belai/internal/sessionsync"
)

// MCP secrets (docs/library-items.md#mcp-secrets). A library item for an MCP server
// may bind a credential key, read by a cred:KEY value, to a Secrets Vault entry
// (`secrets`). Installing the item writes the server's entry into the settings with
// that binding and no value. To put the values on this host the website then sends
// an mcp_secrets_install request that names the server and its keys and nothing
// else; the daemon asks the library for them, over TLS, once, and stores each in the
// credentials store under mcp:<server>, in the keychain when there is one, else the
// user's credentials file at 0600.
//
// A key is accepted only when the installed server's own entry binds it, so a
// request cannot write an arbitrary credential, and a value is never written to
// settings.json, a log, an acknowledgement, an audit event, the session record or an
// error. A Pix Sandbox never takes the request (the library refuses it): it reads
// vault: header values from its vault lease.

// MCPKeyStore is where an MCP server's secrets land. *credentials.Resolver is the
// one in production.
type MCPKeyStore interface {
	StoreMCPSecret(server, key, secret string) error
	ClearMCPSecret(server, key string)
	HasMCPSecret(server, key string) bool
}

// openMCPStore opens the store for the user's own layers unless a test replaces it.
var openMCPStore = func() (MCPKeyStore, error) { return credentials.NewGlobalResolver() }

// mcpRequestTarget checks an mcp_secrets request: the server must be installed on
// this host (the name matches case-insensitively, as the library folds case) and
// every key must be one its entry binds. It returns the host's name for the server
// and the keys in order, or the reason it refused.
func mcpRequestTarget(r sessionsync.Dispatch) (server string, keys []string, why string) {
	if !config.ValidMCPName(r.Server) {
		return "", nil, "that is not an MCP server name"
	}
	s, err := config.LoadGlobal()
	if err != nil || s.MCP == nil {
		return "", nil, "no MCP server of that name is installed on this host"
	}
	var entry config.MCPServer
	for name, srv := range s.MCP.Servers {
		if strings.EqualFold(name, r.Server) {
			server, entry = name, srv
		}
	}
	if server == "" {
		return "", nil, "no MCP server of that name is installed on this host; install it first"
	}
	seen := map[string]bool{}
	for _, k := range r.Keys {
		if !config.ValidMCPRefName(k) {
			return "", nil, "that is not a credential key"
		}
		if _, bound := entry.Secrets[k]; !bound {
			return "", nil, "the installed server does not bind a credential named " + sanitize.Line(k, 40)
		}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		// An empty list means every key the entry binds.
		for k := range entry.Secrets {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 || len(keys) > sessionsync.MaxMCPSecrets {
		return "", nil, "a request names from 1 to 16 credential keys the server binds"
	}
	return server, keys, ""
}

// installMCPSecrets answers an mcp_secrets_install request. It returns the report
// for the acknowledgement (key names only), or the reason it refused.
func (d *Daemon) installMCPSecrets(ctx context.Context, r sessionsync.Dispatch) (string, string) {
	if !d.o.SyncItem(libitem.MCP) {
		return "", "sync.mcps is off on this host, so it takes no MCP secret requests"
	}
	server, keys, why := mcpRequestTarget(r)
	if why != "" {
		return "", why
	}
	got, missing, err := d.o.Client.MCPSecrets(ctx, d.o.HostID, r.ID)
	switch {
	case errors.Is(err, sessionsync.ErrNotFound):
		return "", "the library no longer has this request for this host, so nothing was sent"
	case errors.Is(err, sessionsync.ErrConflict):
		return "", "the library already gave out the secrets for this request; ask for them again"
	case errors.Is(err, sessionsync.ErrKeysNotOverTLS):
		return "", "the library only sends secrets over TLS, and this connection did not look like TLS to it"
	case errors.Is(err, sessionsync.ErrKeysUnavailable):
		return "", "the library could not release the secrets (its key storage is unavailable or could not read one)"
	case err != nil:
		return "", "could not read the secrets from the library: " + reason(err.Error())
	}
	byKey := map[string]sessionsync.MCPSecret{}
	for _, s := range got {
		byKey[s.Key] = s
	}
	absent := map[string]bool{}
	for _, m := range missing {
		absent[m] = true
	}
	store, err := openMCPStore()
	if err != nil {
		return "", "could not open the credential store: " + reason(err.Error())
	}
	var stored, skipped []string
	for _, k := range keys {
		s, ok := byKey[k]
		switch {
		case !ok && absent[k]:
			skipped = append(skipped, k+" (the library holds no usable value for it)")
		case !ok:
			skipped = append(skipped, k+" (the library sent no value for it)")
		default:
			if err := store.StoreMCPSecret(server, k, strings.TrimSpace(s.Reveal())); err != nil {
				skipped = append(skipped, k+" (could not store it: "+reason(credentials.Redact(err.Error(), []string{s.Reveal()}))+")")
				continue
			}
			stored = append(stored, k)
		}
	}
	for i := range skipped {
		skipped[i] = sanitize.Line(skipped[i], 160)
	}
	if len(stored) == 0 {
		return "", "no secret was stored: " + strings.Join(skipped, "; ")
	}
	// A server that failed for want of a credential starts now.
	if m := mcp.Active(); m != nil {
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			_ = m.Restart(rctx, server)
		}()
	}
	report := "stored secrets " + strings.Join(stored, ", ") + " for " + sanitize.Line(server, 40)
	if len(skipped) > 0 {
		report += "; not stored: " + strings.Join(skipped, "; ")
	}
	return report, ""
}

// removeMCPSecrets answers an mcp_secrets_remove request, the undo of an install:
// it clears the secrets this host holds for the named server and keys. The library
// is not asked anything.
func (d *Daemon) removeMCPSecrets(r sessionsync.Dispatch) (string, string) {
	if !d.o.SyncItem(libitem.MCP) {
		return "", "sync.mcps is off on this host, so it takes no MCP secret requests"
	}
	server, keys, why := mcpRequestTarget(r)
	if why != "" {
		return "", why
	}
	store, err := openMCPStore()
	if err != nil {
		return "", "could not open the credential store: " + reason(err.Error())
	}
	var cleared []string
	for _, k := range keys {
		store.ClearMCPSecret(server, k)
		if !store.HasMCPSecret(server, k) {
			cleared = append(cleared, k)
		}
	}
	if len(cleared) == 0 {
		return "", "no secret was cleared"
	}
	return "cleared secrets " + strings.Join(cleared, ", ") + " for " + sanitize.Line(server, 40), ""
}
