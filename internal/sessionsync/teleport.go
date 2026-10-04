package sessionsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/vulnetix/belai/internal/sanitize"
)

// The teleport routes (docs/teleport.md) sit beside the session routes and
// share the client, so they inherit its origin allowlist and credential
// handling. `belai -teleport` calls them to continue a session of the account
// on this host: it asks for a teleport, waits until the backend says it is
// ready, reads the transcript (frozen at the line the session had reached) and
// the profiles and crews it lacks, and tells the backend what it opened. The
// server answers each read only for the teleport that names this host.
//
// Everything that comes back is untrusted text from another host's session.
// This file moves it; internal/teleport checks it.

// Teleport states, equal to the server's (vdb-site belai_teleport.go).
const (
	TeleportRequested = "requested"
	TeleportBackingUp = "backing_up"
	TeleportReady     = "ready"
	TeleportCompleted = "completed"
	TeleportFailed    = "failed"
	TeleportExpired   = "expired"
)

const (
	// teleportMaxBody bounds one transcript page. The server stops a page near
	// 2 MiB but always sends at least one line, and a line may be large.
	teleportMaxBody = 24 << 20
	// TeleportPage is how many transcript lines one request asks for.
	TeleportPage = 100
	// maxTeleportReason bounds the reason text taken from the server.
	maxTeleportReason = 240
)

// Teleport is the persisted half of a teleport row.
type Teleport struct {
	ID              string  `json:"id"`
	OriginSessionID string  `json:"originSessionId"`
	OriginHostID    string  `json:"originHostId"`
	TargetHostID    string  `json:"targetHostId"`
	NewSessionID    *string `json:"newSessionId"`
	Status          string  `json:"status"`
	Reason          string  `json:"reason"`
	SnapshotSeq     int64   `json:"snapshotSeq"`
	ExpiresAt       int64   `json:"expiresAt"`
}

// TeleportCrew names a library crew version a teleport may fetch.
type TeleportCrew struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// TeleportManifest is what the target may install: the profile the session ran
// under, the library versions of it and of its crews' members, and the crews.
type TeleportManifest struct {
	Profile  string          `json:"profile,omitempty"`
	Profiles []CrewMemberRef `json:"profiles,omitempty"`
	Crews    []TeleportCrew  `json:"crews,omitempty"`
	Missing  []string        `json:"missing,omitempty"`
}

// TeleportOverrides are the session's own settings. They are hints: the target
// applies the model only if it already has that provider credentialed.
type TeleportOverrides struct {
	Mode     string `json:"mode"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
}

// TeleportState is a teleport as the target reads it. Git, Overrides and
// Manifest are set only once the status is ready.
type TeleportState struct {
	Teleport  Teleport          `json:"teleport"`
	Git       json.RawMessage   `json:"git"`
	Overrides TeleportOverrides `json:"overrides"`
	Manifest  TeleportManifest  `json:"manifest"`
}

// TeleportError is a refusal the server explained. Reason is cleaned text.
type TeleportError struct {
	Status int
	Reason string
}

func (e *TeleportError) Error() string {
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("the backend answered HTTP %d", e.Status)
}

// Is lets errors.Is match the package's sentinels by status.
func (e *TeleportError) Is(target error) bool {
	switch e.Status {
	case http.StatusNotFound:
		return target == ErrNotFound
	case http.StatusUnauthorized:
		return target == ErrUnauthorized
	case http.StatusConflict:
		return target == ErrConflict
	}
	return false
}

// teleportDo sends one request and decodes a 2xx answer into out. Any other
// status is a *TeleportError carrying the cleaned reason the server gave.
func (c *Client) teleportDo(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	status, data, err := c.roundTrip(ctx, method, path, in, timeout, false, teleportMaxBody)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &body)
		return &TeleportError{Status: status, Reason: sanitize.Line(body.Error, maxTeleportReason)}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func teleportPath(hostID, tid string, rest string) string {
	return "/hosts/" + url.PathEscape(hostID) + "/teleports" + tid + rest
}

// TeleportCreate asks to continue the session ref (a full id or a unique
// prefix) on hostID. Every call makes a new teleport.
func (c *Client) TeleportCreate(ctx context.Context, hostID, ref string) (Teleport, error) {
	var out struct {
		Teleport Teleport `json:"teleport"`
	}
	err := c.teleportDo(ctx, http.MethodPost, teleportPath(hostID, "", ""), map[string]string{"sessionId": ref}, &out, requestTimeout)
	return out.Teleport, err
}

// TeleportGet reads a teleport's status, and once it is ready its git facts,
// overrides and manifest.
func (c *Client) TeleportGet(ctx context.Context, hostID, tid string) (TeleportState, error) {
	var out TeleportState
	err := c.teleportDo(ctx, http.MethodGet, teleportPath(hostID, "/"+url.PathEscape(tid), ""), nil, &out, requestTimeout)
	return out, err
}

// TeleportEntries reads up to TeleportPage transcript lines after seq `after`
// (-1 for the first), none past the teleport's snapshot.
func (c *Client) TeleportEntries(ctx context.Context, hostID, tid string, after int64) ([]Entry, error) {
	var out struct {
		Entries []Entry `json:"entries"`
	}
	rest := fmt.Sprintf("/entries?after=%d&limit=%d", after, TeleportPage)
	err := c.teleportDo(ctx, http.MethodGet, teleportPath(hostID, "/"+url.PathEscape(tid), rest), nil, &out, 2*requestTimeout)
	return out.Entries, err
}

// TeleportProfile reads a profile version the manifest names.
func (c *Client) TeleportProfile(ctx context.Context, hostID, tid, profileID, version string) (string, []FileRef, error) {
	var out struct {
		Markdown string    `json:"markdown"`
		Files    []FileRef `json:"files"`
	}
	rest := "/profiles/" + url.PathEscape(profileID) + "/versions/" + url.PathEscape(version)
	if err := c.teleportDo(ctx, http.MethodGet, teleportPath(hostID, "/"+url.PathEscape(tid), rest), nil, &out, requestTimeout); err != nil {
		return "", nil, err
	}
	if len(out.Markdown) > MaxLibraryProfile {
		return "", nil, fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	return out.Markdown, out.Files, nil
}

// TeleportCrew reads a crew version the manifest names, with the member
// profile versions that go with it.
func (c *Client) TeleportCrew(ctx context.Context, hostID, tid, crewID, version string) (CrewFetched, error) {
	var out CrewFetched
	rest := "/crews/" + url.PathEscape(crewID) + "/versions/" + url.PathEscape(version)
	if err := c.teleportDo(ctx, http.MethodGet, teleportPath(hostID, "/"+url.PathEscape(tid), rest), nil, &out, requestTimeout); err != nil {
		return CrewFetched{}, err
	}
	if len(out.Crew) > MaxLibraryCrew {
		return CrewFetched{}, fmt.Errorf("sessionsync: the crew is larger than %d bytes", MaxLibraryCrew)
	}
	return out, nil
}

// TeleportFile reads a file one of the manifest's profile versions carries.
func (c *Client) TeleportFile(ctx context.Context, hostID, tid, sha string) ([]byte, error) {
	var out struct {
		Content string `json:"contentBase64"`
	}
	rest := "/files/" + url.PathEscape(sha)
	if err := c.teleportDo(ctx, http.MethodGet, teleportPath(hostID, "/"+url.PathEscape(tid), rest), nil, &out, requestTimeout); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return nil, errors.New("sessionsync: the backend sent a file that is not base64")
	}
	if len(b) > MaxLibraryFile {
		return nil, fmt.Errorf("sessionsync: a file is larger than %d bytes", MaxLibraryFile)
	}
	return b, nil
}

// TeleportAck tells the backend whether the target opened the session:
// status "completed" with the new session id, or "refused" with a reason.
// Either way the backend drops what it held for the coordination.
func (c *Client) TeleportAck(ctx context.Context, hostID, tid, status, sessionID, reason string) error {
	in := map[string]string{"status": status, "sessionId": sessionID, "reason": sanitize.Line(reason, maxTeleportReason)}
	return c.teleportDo(ctx, http.MethodPost, teleportPath(hostID, "/"+url.PathEscape(tid), "/ack"), in, nil, requestTimeout)
}
