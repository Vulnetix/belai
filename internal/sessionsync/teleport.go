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
	// TeleportSyncingCode is the row while the origin host is asked for its
	// uncommitted and unpushed changes (docs/teleport.md "Code").
	TeleportSyncingCode = "syncing_code"
	TeleportReady       = "ready"
	TeleportCompleted   = "completed"
	TeleportFailed      = "failed"
	TeleportExpired     = "expired"
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

// Code modes, equal to the server's (vdb-site belai_teleport_code.go).
const (
	// CodeNone: nothing to move, or the origin could not read its changes (Reason).
	CodeNone = "none"
	// CodeBranch: the origin pushed one branch and the target fetches it.
	CodeBranch = "branch"
	// CodeReplay: the forge was not used, so the origin sent a patch and its
	// model's hand-over, and the target replays them.
	CodeReplay = "replay"
)

// TeleportCodeFile is one file the patch changes, as the origin counted it.
type TeleportCodeFile struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// TeleportCodeSkip is a changed file left out of the patch, and why.
type TeleportCodeSkip struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// TeleportCode is the code half of a teleport: what the origin host did with
// the session's uncommitted and unpushed changes. Everything in it is third-party
// text from another host's working tree and model, so the target checks it
// (internal/teleport) before any of it reaches git or a model.
type TeleportCode struct {
	Mode   string `json:"mode"`
	Reason string `json:"reason,omitempty"`
	// Branch and Commit are the pushed teleport branch (mode branch).
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	// Base is the last pushed commit the changes build on, Head the commit the
	// origin had checked out and Tree the tree object of its final state.
	Base string `json:"base,omitempty"`
	Head string `json:"head,omitempty"`
	Tree string `json:"tree,omitempty"`
	// Patch, Summary and Instructions are a replay's hand-over (mode replay).
	Patch        string             `json:"patch,omitempty"`
	Summary      string             `json:"summary,omitempty"`
	Instructions string             `json:"instructions,omitempty"`
	Files        []TeleportCodeFile `json:"files,omitempty"`
	Skipped      []TeleportCodeSkip `json:"skipped,omitempty"`
}

// TeleportState is a teleport as the target reads it. Git, Overrides, Manifest
// and Code are set only once the status is ready.
type TeleportState struct {
	Teleport  Teleport          `json:"teleport"`
	Git       json.RawMessage   `json:"git"`
	Overrides TeleportOverrides `json:"overrides"`
	Manifest  TeleportManifest  `json:"manifest"`
	Code      *TeleportCode     `json:"code"`
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
// prefix) on hostID. Every call makes a new teleport. push is the user's
// agreement that the origin host may push a teleport branch to the forge.
func (c *Client) TeleportCreate(ctx context.Context, hostID, ref string, push bool) (Teleport, error) {
	var out struct {
		Teleport Teleport `json:"teleport"`
	}
	body := map[string]any{"sessionId": ref}
	if push {
		body["push"] = true
	}
	err := c.teleportDo(ctx, http.MethodPost, teleportPath(hostID, "", ""), body, &out, requestTimeout)
	return out.Teleport, err
}

// TeleportReplay tells the backend the target could not fetch the teleport
// branch, so the origin host is asked again to send a patch for a replay.
func (c *Client) TeleportReplay(ctx context.Context, hostID, tid, reason string) error {
	in := map[string]string{"reason": sanitize.Line(reason, maxTeleportReason)}
	return c.teleportDo(ctx, http.MethodPost, teleportPath(hostID, "/"+url.PathEscape(tid), "/replay"), in, nil, requestTimeout)
}

// TeleportCodePut is the origin host's answer to a teleport_code request:
// what it did with the session's changes. The backend takes it only for the
// request the host was handed (dispatch) and only from this host.
func (c *Client) TeleportCodePut(ctx context.Context, hostID, tid, dispatch string, code TeleportCode) error {
	rest := "/code?dispatch=" + url.QueryEscape(dispatch)
	return c.teleportDo(ctx, http.MethodPut, teleportPath(hostID, "/"+url.PathEscape(tid), rest), code, nil, 2*requestTimeout)
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
