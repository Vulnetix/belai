package credentials

import (
	"errors"
	"fmt"

	"github.com/vulnetix/belai/internal/config"
)

// An MCP server's secrets are the user's own: each is one field of the
// credential-store provider mcp:<server> (config.MCPCredentialName), held in the
// keychain or the user's global credentials file and read back by a cred:KEY
// value in the server's env or headers. The environment, the repository's
// project file and netrc are never consulted, so a repository cannot supply or
// shadow one.

// mcpSpec marks the field a secret, so a file holding it inline is checked like
// any credential file.
func mcpSpec(key string) []Field { return []Field{{Name: key, Secret: true}} }

func validMCPSecret(server, key string) error {
	if !config.ValidMCPName(server) || !config.ValidMCPRefName(key) {
		return fmt.Errorf("mcp credential %q for server %q is not a legal name", key, server)
	}
	return nil
}

// MCPSecret returns one of a server's stored secrets.
func (r *Resolver) MCPSecret(server, key string) (string, error) {
	if err := validMCPSecret(server, key); err != nil {
		return "", err
	}
	name := config.MCPCredentialName(server)
	if v, ok, _ := r.userFile.read(name, key, mcpSpec(key)); ok {
		return v.Reveal(), nil
	}
	if r.keychainAvailable() {
		if v, err := r.keychain.Get(name + ":" + key); err == nil && v != "" {
			return v, nil
		}
	}
	return "", errors.New("not stored")
}

// HasMCPSecret reports whether a server's secret is stored.
func (r *Resolver) HasMCPSecret(server, key string) bool {
	v, err := r.MCPSecret(server, key)
	return err == nil && v != ""
}

// StoreMCPSecret writes a server's secret to the preferred backend (the
// keychain, else the user credentials file), replacing a copy in the other
// backend so exactly one holds it.
func (r *Resolver) StoreMCPSecret(server, key, secret string) error {
	if err := validMCPSecret(server, key); err != nil {
		return err
	}
	if secret == "" {
		return errors.New("an empty secret is not stored")
	}
	if len(secret) > 4096 {
		return errors.New("a secret is at most 4096 bytes")
	}
	for _, c := range secret {
		if c < 0x20 || c == 0x7f {
			return errors.New("a secret cannot hold a control character")
		}
	}
	r.ClearMCPSecret(server, key)
	_, err := r.StoreFallback(config.MCPCredentialName(server), key, secret, r.PreferredBackend())
	return err
}

// ClearMCPSecret removes a server's secret from every writable user backend.
func (r *Resolver) ClearMCPSecret(server, key string) {
	if validMCPSecret(server, key) != nil {
		return
	}
	name := config.MCPCredentialName(server)
	_ = r.userFile.delete(name, key)
	if r.keychainAvailable() {
		_ = r.keychain.Delete(name + ":" + key)
	}
}
