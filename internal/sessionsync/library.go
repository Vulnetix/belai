package sessionsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// The agent library routes live beside the session routes under /v1/belai and
// share the client, so they inherit its origin allowlist, its credential
// handling and its response bounds unchanged. A host calls them only to answer
// a profile_backup, profile_install, crew_backup or crew_install request, and
// the server accepts each call only for the request that names it, while it is
// delivered to this host. The exception is the automatic sync at the end of this
// file: profile and crew definitions only, never file contents, and the server
// decides each time whether the host's copy may become the library's.

// Library bounds, equal to the server's (vdb-site belai_library_files.go and
// belai_crews_store.go).
const (
	// MaxLibraryProfile is the largest agent profile the library holds, in bytes.
	MaxLibraryProfile = 64 << 10
	// MaxLibraryFile is the largest file a profile carries, in bytes.
	MaxLibraryFile = 256 << 10
	// MaxLibraryCrew is the largest crew the library holds, in bytes.
	MaxLibraryCrew = 16 << 10
)

// FileRef is one file of a library version: the path the profile lists and the
// hash of the bytes the server keeps for it.
type FileRef struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int    `json:"sizeBytes,omitempty"`
}

// LibraryBackup uploads a profile this host exported, answering the
// profile_backup request dispatchID, and returns the version the library
// holds it as.
func (c *Client) LibraryBackup(ctx context.Context, hostID, dispatchID, markdown string) (version string, err error) {
	return c.LibraryBackupFiles(ctx, hostID, dispatchID, markdown, nil)
}

// LibraryBackupFiles is LibraryBackup with the files the host uploaded for the
// request (LibraryUploadFile). A nil files says nothing about them, which keeps
// the library's latest version's files; an empty one clears them.
func (c *Client) LibraryBackupFiles(ctx context.Context, hostID, dispatchID, markdown string, files []FileRef) (version string, err error) {
	if len(markdown) > MaxLibraryProfile {
		return "", fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	body := map[string]any{"dispatch": dispatchID, "markdown": markdown}
	if files != nil {
		body["files"] = files
	}
	var out struct {
		Version string `json:"version"`
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/backups", body, &out, requestTimeout)
	return out.Version, err
}

// LibraryUploadFile uploads one file's bytes under its hash, answering the
// profile_backup request dispatchID. The server checks the hash and that the
// bytes are bounded text.
func (c *Client) LibraryUploadFile(ctx context.Context, hostID, dispatchID, sha string, content []byte) error {
	if len(content) > MaxLibraryFile {
		return fmt.Errorf("sessionsync: a file is larger than %d bytes", MaxLibraryFile)
	}
	return c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID)+"/library/files/"+url.PathEscape(sha),
		map[string]string{"dispatch": dispatchID, "contentBase64": base64.StdEncoding.EncodeToString(content)}, nil, requestTimeout)
}

// LibraryFetch reads the library version a profile_install request names,
// answering request dispatchID. The server serves that one version and nothing
// else for it.
func (c *Client) LibraryFetch(ctx context.Context, hostID, profileID, version, dispatchID string) (markdown string, err error) {
	md, _, err := c.LibraryFetchFiles(ctx, hostID, profileID, version, dispatchID)
	return md, err
}

// LibraryFetchFiles is LibraryFetch with the version's file manifest. A server
// that predates agent files lists none.
func (c *Client) LibraryFetchFiles(ctx context.Context, hostID, profileID, version, dispatchID string) (markdown string, files []FileRef, err error) {
	var out struct {
		Markdown string    `json:"markdown"`
		Files    []FileRef `json:"files"`
	}
	path := fmt.Sprintf("/hosts/%s/library/profiles/%s/versions/%s?dispatch=%s",
		url.PathEscape(hostID), url.PathEscape(profileID), url.PathEscape(version), url.QueryEscape(dispatchID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return "", nil, err
	}
	if len(out.Markdown) > MaxLibraryProfile {
		return "", nil, fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	return out.Markdown, out.Files, nil
}

// LibraryFetchFile reads one file of a version a profile_install or
// crew_install request names, by its hash.
func (c *Client) LibraryFetchFile(ctx context.Context, hostID, sha, dispatchID string) ([]byte, error) {
	var out struct {
		Content string `json:"contentBase64"`
	}
	path := fmt.Sprintf("/hosts/%s/library/files/%s?dispatch=%s", url.PathEscape(hostID), url.PathEscape(sha), url.QueryEscape(dispatchID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(out.Content)
	if err != nil {
		return nil, errors.New("sessionsync: the library sent a file that is not base64")
	}
	if len(b) > MaxLibraryFile {
		return nil, fmt.Errorf("sessionsync: a file is larger than %d bytes", MaxLibraryFile)
	}
	return b, nil
}

// CrewFetched is a library crew version a crew_install request names.
type CrewFetched struct {
	Version   string          `json:"version"`
	Crew      json.RawMessage `json:"crew"`
	Overwrite bool            `json:"overwrite"`
	// Members are the profile versions to install before the crew is written.
	Members []CrewMemberRef `json:"members"`
}

// CrewBackup uploads a crew this host exported, answering the crew_backup
// request dispatchID, and returns the version the library holds it as.
func (c *Client) CrewBackup(ctx context.Context, hostID, dispatchID string, crew json.RawMessage) (version string, err error) {
	if len(crew) > MaxLibraryCrew {
		return "", fmt.Errorf("sessionsync: the crew is larger than %d bytes", MaxLibraryCrew)
	}
	var out struct {
		Version string `json:"version"`
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/crew-backups",
		map[string]any{"dispatch": dispatchID, "crew": crew}, &out, requestTimeout)
	return out.Version, err
}

// CrewFetch reads the library crew version a crew_install request names.
func (c *Client) CrewFetch(ctx context.Context, hostID, crewID, version, dispatchID string) (CrewFetched, error) {
	var out CrewFetched
	path := fmt.Sprintf("/hosts/%s/library/crews/%s/versions/%s?dispatch=%s",
		url.PathEscape(hostID), url.PathEscape(crewID), url.PathEscape(version), url.QueryEscape(dispatchID))
	if err := c.do(ctx, http.MethodGet, path, nil, &out, requestTimeout); err != nil {
		return CrewFetched{}, err
	}
	if len(out.Crew) > MaxLibraryCrew {
		return CrewFetched{}, fmt.Errorf("sessionsync: the crew is larger than %d bytes", MaxLibraryCrew)
	}
	return out, nil
}

// ── Automatic sync ───────────────────────────────────────────────────────

// SyncItem is one profile or crew this host holds: its id and the hash of the
// bytes the library would store for it.
type SyncItem struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
}

// Sync actions the server answers with.
const (
	SyncPush     = "push"     // the library has nothing newer: push this copy
	SyncCurrent  = "current"  // the library already holds this copy
	SyncDiverged = "diverged" // the library moved since this host last synced it
	SyncSkip     = "skip"     // deleted from the library, or not this account's
)

// SyncAnswer is the server's decision for one item.
type SyncAnswer struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Version string `json:"version,omitempty"`
}

// LibrarySync asks what to do with each profile and crew this host holds. It
// carries hashes only.
func (c *Client) LibrarySync(ctx context.Context, hostID string, agents, crews []SyncItem) (agentAnswers, crewAnswers []SyncAnswer, err error) {
	var out struct {
		Agents []SyncAnswer `json:"agents"`
		Crews  []SyncAnswer `json:"crews"`
	}
	err = c.do(ctx, http.MethodPost, "/hosts/"+url.PathEscape(hostID)+"/library/sync",
		map[string]any{"agents": agents, "crews": crews}, &out, requestTimeout)
	return out.Agents, out.Crews, err
}

// LibrarySyncProfile pushes a profile the sync told this host to push. A
// library that moved in the meantime answers ErrConflict and keeps its copy.
func (c *Client) LibrarySyncProfile(ctx context.Context, hostID, profileID, markdown string) (version string, err error) {
	if len(markdown) > MaxLibraryProfile {
		return "", fmt.Errorf("sessionsync: the profile is larger than %d bytes", MaxLibraryProfile)
	}
	var out struct {
		Version string `json:"version"`
	}
	err = c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID)+"/library/sync/profiles/"+url.PathEscape(profileID),
		map[string]string{"markdown": markdown}, &out, requestTimeout)
	return out.Version, err
}

// LibrarySyncCrew pushes a crew the sync told this host to push.
func (c *Client) LibrarySyncCrew(ctx context.Context, hostID, crewID string, crew json.RawMessage) (version string, err error) {
	if len(crew) > MaxLibraryCrew {
		return "", fmt.Errorf("sessionsync: the crew is larger than %d bytes", MaxLibraryCrew)
	}
	var out struct {
		Version string `json:"version"`
	}
	err = c.do(ctx, http.MethodPut, "/hosts/"+url.PathEscape(hostID)+"/library/sync/crews/"+url.PathEscape(crewID),
		map[string]any{"crew": crew}, &out, requestTimeout)
	return out.Version, err
}
