package sessionsync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

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
