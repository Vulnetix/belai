// Package vulnetixcreds reads the Vulnetix CLI credential for the AI Firewall
// gateway. It mirrors the CLI's own load precedence without shelling out to a
// binary that cannot print its secret.
package vulnetixcreds

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Keychain abstracts the host keychain / secret service. It matches the
// shape of credentials.Keychain so callers can pass either the belai or
// vulnetix-scoped backend without an import cycle.
type Keychain interface {
	Name() string
	Available() bool
	Get(account string) (string, error)
	Set(account, secret string) error
	Delete(account string) error
}

// Credential is the gateway credential derived from the CLI's store.
type Credential struct {
	OrgUUID string
	APIKey  string
	Source  string
}

// ErrNoGatewayCredential is returned for a token-only credential that cannot
// authenticate to the AI Firewall gateway.
var ErrNoGatewayCredential = errors.New("logged in with an API token; the Firewall needs an API key")

// diskCredential matches the shape the Vulnetix CLI writes to
// .vulnetix/credentials.json (cli/pkg/auth/auth.go).
type diskCredential struct {
	OrgID           string `json:"org_id"`
	APIKey          string `json:"api_key"`
	Secret          string `json:"secret"`
	Token           string `json:"token"`
	Method          string `json:"method"`
	APIKeyInKeyring bool   `json:"api_key_in_keyring"`
	HMACInKeyring   bool   `json:"hmac_in_keyring"`
	TokenInKeyring  bool   `json:"token_in_keyring"`
}

// Load resolves the gateway credential using the CLI's precedence:
//
//  1. VULNETIX_API_KEY + VULNETIX_ORG_ID env.
//  2. VVD_ORG + VVD_SECRET env (APIKey = HMAC-SHA256(secret, org)).
//  3. .vulnetix/credentials.json in the workdir.
//  4. $VULNETIX_CREDENTIALS_DIR, else ~/.vulnetix/credentials.json.
//
// The keychain seam lets tests inject a fake; production callers pass the
// vulnetix-scoped keyring from credentials.NewKeyringBackend.
func Load(getenv func(string) string, home string, workdir string, kc Keychain) (Credential, error) {
	if key := getenv("VULNETIX_API_KEY"); key != "" {
		if org := getenv("VULNETIX_ORG_ID"); org != "" {
			return Credential{OrgUUID: org, APIKey: key, Source: "$VULNETIX_API_KEY"}, nil
		}
	}
	if org := getenv("VVD_ORG"); org != "" {
		if secret := getenv("VVD_SECRET"); secret != "" {
			return Credential{OrgUUID: org, APIKey: hmacKey(secret, org), Source: "$VVD_SECRET"}, nil
		}
	}
	if workdir != "" {
		if c, ok, err := loadFile(filepath.Join(workdir, ".vulnetix", "credentials.json"), kc); err != nil || ok {
			return c, err
		}
	}
	path := filepath.Join(home, ".vulnetix", "credentials.json")
	if dir := getenv("VULNETIX_CREDENTIALS_DIR"); dir != "" {
		path = filepath.Join(dir, "credentials.json")
	}
	if c, ok, err := loadFile(path, kc); err != nil || ok {
		return c, err
	}
	return Credential{}, errors.New("no Vulnetix credential found")
}

// AuthHeader returns the Authorization header value the Vulnetix CLI would
// send: "ApiKey <org>:<key>" for an API-key or SigV4 credential, or
// "Bearer <token>" for a token login. Unlike Load it accepts a token-only
// credential, because the Vulnetix API (and its MCP server) takes either.
// It never falls back to the CLI's shared Community credential.
func AuthHeader(getenv func(string) string, home, workdir string, kc Keychain) (string, error) {
	if tok := strings.TrimSpace(getenv("VULNETIX_API_TOKEN")); tok != "" {
		return "Bearer " + tok, nil
	}
	c, err := Load(getenv, home, workdir, kc)
	if err == nil {
		if c.OrgUUID == "" || c.APIKey == "" {
			return "", errors.New("the Vulnetix credential has no org id or key")
		}
		return "ApiKey " + c.OrgUUID + ":" + c.APIKey, nil
	}
	if !errors.Is(err, ErrNoGatewayCredential) {
		return "", err
	}
	for _, path := range credentialPaths(getenv, home, workdir) {
		if tok, ok := loadToken(path, kc); ok {
			return "Bearer " + tok, nil
		}
	}
	return "", err
}

// credentialPaths lists the CLI credential files in load order.
func credentialPaths(getenv func(string) string, home, workdir string) []string {
	var out []string
	if workdir != "" {
		out = append(out, filepath.Join(workdir, ".vulnetix", "credentials.json"))
	}
	path := filepath.Join(home, ".vulnetix", "credentials.json")
	if dir := getenv("VULNETIX_CREDENTIALS_DIR"); dir != "" {
		path = filepath.Join(dir, "credentials.json")
	}
	return append(out, path)
}

// loadToken reads a token-method credential's bearer token.
func loadToken(path string, kc Keychain) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var d diskCredential
	if json.Unmarshal(data, &d) != nil || d.Method != "token" {
		return "", false
	}
	tok := d.Token
	if d.TokenInKeyring && kc != nil {
		v, err := kc.Get("token:" + d.OrgID)
		if err != nil {
			return "", false
		}
		tok = v
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

func loadFile(path string, kc Keychain) (Credential, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credential{}, false, nil
		}
		return Credential{}, false, err
	}
	var d diskCredential
	if err := json.Unmarshal(data, &d); err != nil {
		return Credential{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	switch d.Method {
	case "apikey":
		key := d.APIKey
		if d.APIKeyInKeyring {
			v, err := kc.Get("apikey:" + d.OrgID)
			if err != nil {
				return Credential{}, false, fmt.Errorf("read apikey from keyring: %w", err)
			}
			key = v
		}
		return Credential{OrgUUID: d.OrgID, APIKey: stripOrgPrefix(d.OrgID, key), Source: path}, true, nil
	case "sigv4":
		secret := d.Secret
		if d.HMACInKeyring {
			v, err := kc.Get("hmac-secret:" + d.OrgID)
			if err != nil {
				return Credential{}, false, fmt.Errorf("read hmac-secret from keyring: %w", err)
			}
			secret = v
		}
		return Credential{OrgUUID: d.OrgID, APIKey: hmacKey(secret, d.OrgID), Source: path}, true, nil
	case "token":
		if d.APIKey == "" && d.Secret == "" {
			return Credential{}, false, ErrNoGatewayCredential
		}
		// Fall through to the default derivation below.
	}
	// Unknown or token-with-key: try the same derivations the CLI uses.
	key := stripOrgPrefix(d.OrgID, d.APIKey)
	if key != "" {
		return Credential{OrgUUID: d.OrgID, APIKey: key, Source: path}, true, nil
	}
	if d.Secret != "" {
		return Credential{OrgUUID: d.OrgID, APIKey: hmacKey(d.Secret, d.OrgID), Source: path}, true, nil
	}
	return Credential{}, false, ErrNoGatewayCredential
}

func stripOrgPrefix(org, key string) string {
	if org == "" {
		return key
	}
	return strings.TrimPrefix(key, org+":")
}

func hmacKey(secret, org string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(org))
	return hex.EncodeToString(mac.Sum(nil))
}
