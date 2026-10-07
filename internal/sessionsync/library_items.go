package sessionsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/vulnetix/belai/internal/libitem"
)

// The item library (skills, prompts and the other kinds in docs/library-items.md)
// rides the same client, origin allowlist, credential and response bounds as the
// agent library in library.go. A host calls these routes to answer an
// item_backup or item_install request, which the server accepts only for the
// request that names it while it is delivered to this host, and for the
// automatic sync at the end of this file, which carries a hash per item and, for
// an item the server tells the host to push, that one document.
//
// A document travels as the website API carries it: a string for the Markdown
// kinds and a JSON object for the others. The bound on it is the kind's own
// (libitem.Kind.MaxBytes), checked before it is sent and again before it is used.

// ItemSaved is the library's answer to a backup or a sync push.
type ItemSaved struct {
	// Version is the library version the document was stored as.
	Version string
	// Created is true when the item did not exist in the library before.
	Created bool
}

// ItemFetched is a library item version an item_install request names.
type ItemFetched struct {
	Version string
	// Name is the item's name in the library.
	Name string
	// Body is the document, as the library holds it.
	Body []byte
	// Overwrite is the request's replace flag as the server recorded it.
	Overwrite bool
}

// docField carries a document in a JSON body: a Markdown document is a string,
// a JSON document is an object written as it is.
func docField(kind libitem.Kind, body []byte) (any, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("sessionsync: %q is not a library item kind", string(kind))
	}
	if len(body) > kind.MaxBytes() {
		return nil, fmt.Errorf("sessionsync: the %s is larger than %d bytes", kind, kind.MaxBytes())
	}
	if kind.IsMarkdown() {
		return string(body), nil
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("sessionsync: the %s is not JSON", kind)
	}
	return json.RawMessage(body), nil
}

// docBytes reads a document out of a JSON body field, with the kind's bound.
func docBytes(kind libitem.Kind, raw json.RawMessage) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	var b []byte
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return nil, errors.New("sessionsync: the library sent no document")
	case kind.IsMarkdown():
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("sessionsync: the library sent a %s that is not text", kind)
		}
		b = []byte(s)
	default:
		if raw[0] != '{' {
			return nil, fmt.Errorf("sessionsync: the library sent a %s that is not an object", kind)
		}
		b = raw
	}
	if len(b) > kind.MaxBytes()*2 {
		return nil, fmt.Errorf("sessionsync: the %s is larger than %d bytes", kind, kind.MaxBytes())
	}
	return b, nil
}

// versionOf reads a version out of the field the server sends: the version
// string itself, or an object holding it.
func versionOf(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var o struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &o) == nil {
		return o.Version
	}
	return ""
}

// ItemBackup uploads an item this host exported, answering the item_backup
// request dispatchID.
func (c *Client) ItemBackup(ctx context.Context, hostID, dispatchID string, kind libitem.Kind, body []byte) (ItemSaved, error) {
	doc, err := docField(kind, body)
	if err != nil {
		return ItemSaved{}, err
	}
	var out struct {
		Version json.RawMessage `json:"version"`
		Created bool            `json:"created"`
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/item-backups",
		map[string]any{"dispatch": dispatchID, "kind": string(kind), "body": doc}, &out, requestTimeout)
	return ItemSaved{Version: versionOf(out.Version), Created: out.Created}, err
}

// ItemFetch reads the library item version an item_install request names,
// answering request dispatchID. The server serves that one version and nothing
// else for it.
func (c *Client) ItemFetch(ctx context.Context, hostID string, kind libitem.Kind, itemID, version, dispatchID string) (ItemFetched, error) {
	if !kind.Valid() {
		return ItemFetched{}, fmt.Errorf("sessionsync: %q is not a library item kind", string(kind))
	}
	var out struct {
		Version   json.RawMessage `json:"version"`
		Body      json.RawMessage `json:"body"`
		Overwrite bool            `json:"overwrite"`
		Name      string          `json:"name"`
	}
	path := fmt.Sprintf("/hosts/%s/library/items/%s/%s/versions/%s?dispatch=%s",
		url.PathEscape(hostID), url.PathEscape(kind.Segment()), url.PathEscape(itemID), url.PathEscape(version), url.QueryEscape(dispatchID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return ItemFetched{}, err
	}
	body, err := docBytes(kind, out.Body)
	if err != nil {
		return ItemFetched{}, err
	}
	return ItemFetched{Version: versionOf(out.Version), Name: out.Name, Body: body, Overwrite: out.Overwrite}, nil
}

// ── Automatic sync ───────────────────────────────────────────────────────

// SyncItemRef is one library item this host holds: its kind and name and the
// hash of its canonical document. An item has no id on the host; the library
// matches it by kind and name.
type SyncItemRef struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// SyncItemAnswer is the server's decision for one item.
type SyncItemAnswer struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Version string `json:"version,omitempty"`
}

// MaxSyncItems is how many items one sync request carries.
const MaxSyncItems = 200

// LibrarySyncAll is LibrarySync with the library items this host holds. It
// carries hashes only. A server that predates items answers none.
func (c *Client) LibrarySyncAll(ctx context.Context, hostID string, agents, crews []SyncItem, items []SyncItemRef) (agentAnswers, crewAnswers []SyncAnswer, itemAnswers []SyncItemAnswer, err error) {
	if len(items) > MaxSyncItems {
		return nil, nil, nil, fmt.Errorf("sessionsync: a sync names at most %d items", MaxSyncItems)
	}
	var out struct {
		Agents []SyncAnswer     `json:"agents"`
		Crews  []SyncAnswer     `json:"crews"`
		Items  []SyncItemAnswer `json:"items"`
	}
	body := map[string]any{"agents": agents, "crews": crews}
	if len(items) > 0 {
		body["items"] = items
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/sync", body, &out, requestTimeout)
	return out.Agents, out.Crews, out.Items, err
}

// LibrarySyncItem pushes an item the sync told this host to push. A library
// that moved in the meantime answers ErrConflict and keeps its copy.
func (c *Client) LibrarySyncItem(ctx context.Context, hostID string, kind libitem.Kind, name string, body []byte) (version string, err error) {
	doc, err := docField(kind, body)
	if err != nil {
		return "", err
	}
	var out struct {
		Version json.RawMessage `json:"version"`
	}
	err = c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID)+"/library/sync/items",
		map[string]any{"kind": string(kind), "name": name, "body": doc}, &out, requestTimeout)
	return versionOf(out.Version), err
}

// ── Provider keys ────────────────────────────────────────────────────────

// Provider key bounds, equal to the server's (belaiProviderKeysMax and
// validateProviderKey).
const (
	// MaxProviderKeys is how many providers one provider_keys_install request names.
	MaxProviderKeys = 16
	// MaxProviderKeyBytes is the longest key.
	MaxProviderKeyBytes = 4096
)

// Why the library would not release a request's keys, beyond not found (404) and
// already delivered (ErrConflict, 409).
var (
	// ErrKeysNotOverTLS is a 403 whose error text names TLS: the library only sends a
	// key over TLS, and it did not see this connection as TLS. Any other 403 is not
	// this error; it comes from something in front of the library.
	ErrKeysNotOverTLS = errors.New("sessionsync: the library only sends provider keys over TLS")
	// ErrKeysUnavailable is a 502 or 503: the library has no key storage, or could
	// not read a stored key.
	ErrKeysUnavailable = errors.New("sessionsync: the library could not release the provider keys")
)

// ProviderKey is one key the library released for a host. It is a secret: it prints
// as <redacted> through every fmt verb, marshals as <redacted>, and its value is
// read only with Reveal, at the one place that stores it.
type ProviderKey struct {
	// Provider is the catalogue slug.
	Provider string
	key      string
}

// Reveal returns the key itself.
func (k ProviderKey) Reveal() string { return k.key }

func (k ProviderKey) String() string { return k.Provider + ":<redacted>" }
func (k ProviderKey) GoString() string {
	return "sessionsync.ProviderKey{" + k.Provider + ":<redacted>}"
}

// Format makes every verb, %v, %+v, %#v and %s included, print the redacted form.
func (k ProviderKey) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(k.String())) }

// MarshalJSON never writes the key.
func (k ProviderKey) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"provider": k.Provider, "key": "<redacted>"})
}

// keysRefusedForTLS reports whether a 403 body is the library's own refusal to send
// a key over plain HTTP. That is the only 403 the route answers with, but a WAF or the
// egress gateway can answer 403 too, so the status alone says nothing about TLS: the
// {"error": ...} text has to name it. The body was read through roundTrip's cap and
// only the error text is looked at; nothing from it is kept or returned.
func keysRefusedForTLS(body []byte) bool {
	var refusal struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &refusal) != nil {
		return false
	}
	return strings.Contains(refusal.Error, "TLS")
}

// ProviderKeys reads the provider keys the provider_keys_install request
// dispatchID names. The server answers only while that request is delivered to this
// host, once, over TLS, and only for the slugs the request names; missing lists the
// ones it holds no usable key for. The response is read into memory and nowhere else:
// no key is written to a file, a log or an error here.
func (c *Client) ProviderKeys(ctx context.Context, hostID, dispatchID string) (keys []ProviderKey, missing []string, err error) {
	var out struct {
		Keys []struct {
			Provider string `json:"provider"`
			Key      string `json:"key"`
		} `json:"keys"`
		Missing []string `json:"missing"`
	}
	path := fmt.Sprintf("/hosts/%s/library/provider-keys?dispatch=%s", url.PathEscape(hostID), url.QueryEscape(dispatchID))
	status, data, err := c.roundTrip(ctx, http.MethodGet, path, nil, requestTimeout, false, defaultMaxBody)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, nil, ErrNotFound
	case status == http.StatusUnauthorized:
		return nil, nil, ErrUnauthorized
	case status == http.StatusConflict:
		return nil, nil, ErrConflict
	case status == http.StatusForbidden && keysRefusedForTLS(data):
		return nil, nil, ErrKeysNotOverTLS
	case status == http.StatusBadGateway, status == http.StatusServiceUnavailable:
		return nil, nil, ErrKeysUnavailable
	case status < 200 || status > 299:
		// Any other status, a 403 from a firewall or the edge among them: the text
		// names a route and a status, never the body, which could hold a value.
		return nil, nil, fmt.Errorf("sessionsync: GET %s: HTTP %d", path, status)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, nil, err
	}
	if len(out.Keys) > MaxProviderKeys || len(out.Missing) > MaxProviderKeys {
		return nil, nil, fmt.Errorf("sessionsync: the library sent more than %d provider keys", MaxProviderKeys)
	}
	for _, k := range out.Keys {
		if len(k.Key) > MaxProviderKeyBytes {
			return nil, nil, fmt.Errorf("sessionsync: the library sent a key longer than %d bytes", MaxProviderKeyBytes)
		}
		keys = append(keys, ProviderKey{Provider: k.Provider, key: k.Key})
	}
	return keys, out.Missing, nil
}

// ── MCP secrets ──────────────────────────────────────────────────────────

// MCP secret bounds, equal to the server's (belaiMCPSecretsMax).
const (
	// MaxMCPSecrets is how many credential keys one mcp_secrets_install request names.
	MaxMCPSecrets = 16
	// MaxMCPSecretBytes is the longest secret.
	MaxMCPSecretBytes = 4096
)

// MCPSecret is one secret the library released for an MCP server of a host. Like a
// ProviderKey it prints as <redacted> through every fmt verb, marshals as
// <redacted>, and its value is read only with Reveal, at the one place that
// stores it.
type MCPSecret struct {
	// Key is the credential key a cred:KEY value of the server reads.
	Key   string
	value string
}

// Reveal returns the secret itself.
func (s MCPSecret) Reveal() string { return s.value }

func (s MCPSecret) String() string   { return s.Key + ":<redacted>" }
func (s MCPSecret) GoString() string { return "sessionsync.MCPSecret{" + s.Key + ":<redacted>}" }

// Format makes every verb print the redacted form.
func (s MCPSecret) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }

// MarshalJSON never writes the value.
func (s MCPSecret) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"key": s.Key, "value": "<redacted>"})
}

// MCPSecrets reads the secrets the mcp_secrets_install request dispatchID names.
// The server answers only while that request is delivered to this host, once, over
// TLS, and only for the keys the request names (the ones the server's library item
// binds to a vault entry); missing lists the ones it holds no usable value for. The
// response is read into memory and nowhere else.
func (c *Client) MCPSecrets(ctx context.Context, hostID, dispatchID string) (secrets []MCPSecret, missing []string, err error) {
	var out struct {
		Secrets []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"secrets"`
		Missing []string `json:"missing"`
	}
	path := fmt.Sprintf("/hosts/%s/library/mcp-secrets?dispatch=%s", url.PathEscape(hostID), url.QueryEscape(dispatchID))
	status, data, err := c.roundTrip(ctx, http.MethodGet, path, nil, requestTimeout, false, defaultMaxBody)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case status == http.StatusNotFound:
		return nil, nil, ErrNotFound
	case status == http.StatusUnauthorized:
		return nil, nil, ErrUnauthorized
	case status == http.StatusConflict:
		return nil, nil, ErrConflict
	case status == http.StatusForbidden && keysRefusedForTLS(data):
		return nil, nil, ErrKeysNotOverTLS
	case status == http.StatusBadGateway, status == http.StatusServiceUnavailable:
		return nil, nil, ErrKeysUnavailable
	case status < 200 || status > 299:
		// The text names a route and a status, never the body, which could hold a value.
		return nil, nil, fmt.Errorf("sessionsync: GET %s: HTTP %d", path, status)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, nil, err
	}
	if len(out.Secrets) > MaxMCPSecrets || len(out.Missing) > MaxMCPSecrets {
		return nil, nil, fmt.Errorf("sessionsync: the library sent more than %d MCP secrets", MaxMCPSecrets)
	}
	for _, s := range out.Secrets {
		if len(s.Value) > MaxMCPSecretBytes {
			return nil, nil, fmt.Errorf("sessionsync: the library sent a secret longer than %d bytes", MaxMCPSecretBytes)
		}
		secrets = append(secrets, MCPSecret{Key: s.Key, value: s.Value})
	}
	return secrets, out.Missing, nil
}
